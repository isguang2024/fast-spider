package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxArtifactUploadBytes = 100 << 20
	artifactUploadChunk    = 1 << 20
)

type artifactControlParams struct {
	Path        string `json:"path,omitempty"`
	JobID       string `json:"jobId,omitempty"`
	LogicalName string `json:"logicalName,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

type artifactCreateResponse struct {
	ArtifactID    string    `json:"artifactId"`
	UploadID      string    `json:"uploadId"`
	ChunkBytes    int64     `json:"chunkBytes"`
	ReceivedBytes int64     `json:"receivedBytes"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type artifactUploadStatusResponse struct {
	UploadID      string    `json:"uploadId"`
	ArtifactID    string    `json:"artifactId"`
	ReceivedBytes int64     `json:"receivedBytes"`
	ExpectedBytes int64     `json:"expectedBytes"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type artifactUploadResult struct {
	ArtifactID      string `json:"artifactId"`
	LogicalName     string `json:"logicalName"`
	ContentType     string `json:"contentType"`
	SizeBytes       int64  `json:"sizeBytes"`
	SHA256          string `json:"sha256"`
	JobLogTruncated bool   `json:"jobLogTruncated,omitempty"`
}

func (c *Client) artifactUploadFile(ctx context.Context, params map[string]any) (artifactUploadResult, error) {
	var input artifactControlParams
	if err := decodeParams(params, &input); err != nil {
		return artifactUploadResult{}, fmt.Errorf("invalid params: %w", err)
	}
	if input.Path == "" {
		return artifactUploadResult{}, fmt.Errorf("path is required")
	}
	path, err := ResolveMachinePath(input.Path)
	if err != nil {
		return artifactUploadResult{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return artifactUploadResult{}, err
	}
	if !info.Mode().IsRegular() {
		return artifactUploadResult{}, ErrNotRegularFile
	}
	name := input.LogicalName
	if name == "" {
		name = filepath.Base(path)
	}
	contentType := input.ContentType
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	result, err := c.uploadArtifactPath(ctx, "", name, contentType, path)
	return result, err
}

func (c *Client) artifactUploadJobLog(ctx context.Context, params map[string]any) (artifactUploadResult, error) {
	var input artifactControlParams
	if err := decodeParams(params, &input); err != nil {
		return artifactUploadResult{}, fmt.Errorf("invalid params: %w", err)
	}
	if input.JobID == "" {
		return artifactUploadResult{}, fmt.Errorf("jobId is required")
	}
	path, _, truncated, err := c.jobs.JobLog(input.JobID)
	if err != nil {
		return artifactUploadResult{}, err
	}
	name := input.LogicalName
	if name == "" {
		name = input.JobID + ".log"
	}
	result, err := c.uploadArtifactPath(ctx, input.JobID, name, "text/plain; charset=utf-8", path)
	result.JobLogTruncated = truncated
	return result, err
}

func (c *Client) uploadArtifactPath(ctx context.Context, jobID, logicalName, contentType, path string) (artifactUploadResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return artifactUploadResult{}, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return artifactUploadResult{}, err
	}
	if info.Size() < 0 || info.Size() > maxArtifactUploadBytes {
		file.Close()
		return artifactUploadResult{}, fmt.Errorf("artifact exceeds %d bytes", maxArtifactUploadBytes)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		file.Close()
		return artifactUploadResult{}, err
	}
	if err := file.Close(); err != nil {
		return artifactUploadResult{}, err
	}
	sha := "sha256:" + hex.EncodeToString(hash.Sum(nil))

	state, err := c.State()
	if err != nil {
		return artifactUploadResult{}, err
	}
	normalizedHubURL, err := c.normalizeHubURL(state.HubURL)
	if err != nil {
		return artifactUploadResult{}, fmt.Errorf("validate registered hub URL: %w", err)
	}
	state.HubURL = normalizedHubURL
	tokenCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	deviceToken, err := c.issueDeviceToken(tokenCtx, state)
	cancel()
	if err != nil {
		return artifactUploadResult{}, err
	}
	var created artifactCreateResponse
	if err := c.postJSON(ctx, state.HubURL+"/node/v1/artifacts", deviceToken.DeviceToken, map[string]any{
		"jobId": jobID, "logicalName": logicalName, "contentType": contentType,
		"sizeBytes": info.Size(), "sha256": sha,
	}, &created); err != nil {
		return artifactUploadResult{}, err
	}
	chunkSize := created.ChunkBytes
	if chunkSize <= 0 || chunkSize > artifactUploadChunk {
		_ = c.abortArtifact(context.Background(), state.HubURL, deviceToken.DeviceToken, created.UploadID)
		return artifactUploadResult{}, fmt.Errorf("hub returned invalid artifact chunk size")
	}

	file, err = os.Open(path)
	if err != nil {
		return artifactUploadResult{}, err
	}
	defer file.Close()
	if created.ReceivedBytes < 0 || created.ReceivedBytes > info.Size() {
		_ = c.abortArtifact(context.Background(), state.HubURL, deviceToken.DeviceToken, created.UploadID)
		return artifactUploadResult{}, fmt.Errorf("hub returned invalid artifact resume offset")
	}
	if _, err := file.Seek(created.ReceivedBytes, io.SeekStart); err != nil {
		return artifactUploadResult{}, err
	}
	buffer := make([]byte, chunkSize)
	offset := created.ReceivedBytes
	consecutiveFailures := 0
	for offset < info.Size() {
		n, readErr := file.Read(buffer)
		if n > 0 {
			endpoint := fmt.Sprintf("%s/node/v1/artifacts/%s/chunk?offset=%d", state.HubURL, created.UploadID, offset)
			if err := c.artifactRequest(ctx, http.MethodPut, endpoint, deviceToken.DeviceToken, buffer[:n], "application/octet-stream", nil); err != nil {
				consecutiveFailures++
				if consecutiveFailures > 3 {
					return artifactUploadResult{}, fmt.Errorf("artifact upload paused after repeated transport failures: %w", err)
				}
				status, statusErr := c.artifactUploadStatus(ctx, state.HubURL, deviceToken.DeviceToken, created.UploadID)
				if statusErr != nil {
					return artifactUploadResult{}, fmt.Errorf("artifact upload status after failure: %w", statusErr)
				}
				if status.ArtifactID != created.ArtifactID || status.ExpectedBytes != info.Size() || status.ReceivedBytes < 0 || status.ReceivedBytes > info.Size() {
					return artifactUploadResult{}, fmt.Errorf("hub returned inconsistent artifact resume state")
				}
				offset = status.ReceivedBytes
				if _, seekErr := file.Seek(offset, io.SeekStart); seekErr != nil {
					return artifactUploadResult{}, seekErr
				}
				continue
			}
			offset += int64(n)
			consecutiveFailures = 0
		}
		if readErr != nil && readErr != io.EOF {
			return artifactUploadResult{}, readErr
		}
		if readErr == io.EOF && offset < info.Size() {
			return artifactUploadResult{}, io.ErrUnexpectedEOF
		}
	}
	completeEndpoint := state.HubURL + "/node/v1/artifacts/" + created.UploadID + "/complete"
	var completeErr error
	for attempt := 0; attempt < 3; attempt++ {
		completeErr = c.artifactRequest(ctx, http.MethodPost, completeEndpoint, deviceToken.DeviceToken, []byte("{}"), "application/json", nil)
		if completeErr == nil {
			break
		}
		if apiErr, ok := completeErr.(*HubAPIError); ok && !apiErr.Retryable {
			return artifactUploadResult{}, completeErr
		}
		if ctx.Err() != nil {
			return artifactUploadResult{}, ctx.Err()
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return artifactUploadResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	if completeErr != nil {
		return artifactUploadResult{}, fmt.Errorf("complete artifact upload after retries: %w", completeErr)
	}
	return artifactUploadResult{ArtifactID: created.ArtifactID, LogicalName: logicalName, ContentType: contentType, SizeBytes: info.Size(), SHA256: sha}, nil
}

func (c *Client) artifactUploadStatus(ctx context.Context, hubURL, token, uploadID string) (artifactUploadStatusResponse, error) {
	var status artifactUploadStatusResponse
	if err := c.artifactRequest(ctx, http.MethodGet, hubURL+"/node/v1/artifacts/"+uploadID, token, nil, "", &status); err != nil {
		return artifactUploadStatusResponse{}, err
	}
	return status, nil
}

func (c *Client) abortArtifact(ctx context.Context, hubURL, token, uploadID string) error {
	abortCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.artifactRequest(abortCtx, http.MethodDelete, hubURL+"/node/v1/artifacts/"+uploadID, token, nil, "", nil)
}

func (c *Client) artifactRequest(ctx context.Context, method, endpoint, bearer string, body []byte, contentType string, output any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "fast-spider-node/"+c.cfg.Version)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := *c.http
	client.Timeout = 2 * time.Minute
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxHTTPResponseBytes {
		return fmt.Errorf("hub response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr protocolAPIError
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Code != "" {
			return &HubAPIError{Code: apiErr.Error.Code, Message: apiErr.Error.Message, Retryable: apiErr.Error.Retryable}
		}
		return fmt.Errorf("hub HTTP status %d", resp.StatusCode)
	}
	if output != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, output); err != nil {
			return fmt.Errorf("decode hub response: %w", err)
		}
	}
	return nil
}

func validArtifactLogicalName(value string) bool {
	return value != "" && filepath.Base(value) == value && !strings.ContainsAny(value, "\\/\r\n")
}
