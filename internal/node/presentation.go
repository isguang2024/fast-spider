package node

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const maxPresentationUploadSize int64 = 64 << 20

var ErrPresentationUpload = errors.New("presentation upload failed")

type presentationUploadResponse struct {
	PresentationID string    `json:"presentationId"`
	FileName       string    `json:"fileName"`
	ContentType    string    `json:"contentType"`
	SizeBytes      int64     `json:"sizeBytes"`
	SHA256         string    `json:"sha256"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

func (c *Client) publishPresentationFile(ctx context.Context, filePath, logicalName, contentType string) (map[string]any, error) {
	result, err := c.uploadPresentationFile(ctx, filePath, logicalName, contentType)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPresentationUpload, err)
	}
	return map[string]any{
		"presentationId": result.PresentationID,
		"fileName":       result.FileName,
		"contentType":    result.ContentType,
		"sizeBytes":      result.SizeBytes,
		"sha256":         result.SHA256,
		"expiresAt":      result.ExpiresAt,
	}, nil
}

func (c *Client) presentationPublishFile(ctx context.Context, params map[string]any) (map[string]any, error) {
	var input artifactControlParams
	if err := decodeParams(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if strings.TrimSpace(input.Path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	filePath, err := ResolveMachinePath(input.Path)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(input.LogicalName)
	if name == "" {
		name = filepath.Base(filePath)
	}
	contentType := strings.TrimSpace(input.ContentType)
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	return c.publishPresentationFile(ctx, filePath, name, contentType)
}

func (c *Client) uploadPresentationFile(ctx context.Context, filePath, logicalName, contentType string) (presentationUploadResponse, error) {
	if err := ctx.Err(); err != nil {
		return presentationUploadResponse{}, err
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return presentationUploadResponse{}, err
	}
	if !info.Mode().IsRegular() {
		return presentationUploadResponse{}, ErrNotRegularFile
	}
	if info.Size() <= 0 || info.Size() > maxPresentationUploadSize {
		return presentationUploadResponse{}, fmt.Errorf("presentation file size is outside the allowed range")
	}
	logicalName = safePresentationFileName(logicalName)
	contentType = strings.TrimSpace(contentType)
	if contentType == "" || len(contentType) > 256 {
		return presentationUploadResponse{}, fmt.Errorf("presentation content type is invalid")
	}
	sha, err := hashPresentationFile(filePath)
	if err != nil {
		return presentationUploadResponse{}, err
	}
	state, err := c.State()
	if err != nil {
		return presentationUploadResponse{}, err
	}
	state.HubURL, err = c.normalizeHubURL(state.HubURL)
	if err != nil {
		return presentationUploadResponse{}, fmt.Errorf("validate registered hub URL: %w", err)
	}
	tokenCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	deviceToken, err := c.issueDeviceToken(tokenCtx, state)
	cancel()
	if err != nil {
		return presentationUploadResponse{}, err
	}
	file, err := os.Open(filePath)
	if err != nil {
		return presentationUploadResponse{}, err
	}
	defer file.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, state.HubURL+"/node/v1/presentations", io.LimitReader(file, info.Size()))
	if err != nil {
		return presentationUploadResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+deviceToken.DeviceToken)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "fast-spider-node/"+c.cfg.Version)
	req.Header.Set("X-Fast-Spider-File-Name", base64.RawURLEncoding.EncodeToString([]byte(logicalName)))
	req.Header.Set("X-Fast-Spider-SHA256", sha)
	req.ContentLength = info.Size()

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return presentationUploadResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes+1))
	if err != nil {
		return presentationUploadResponse{}, err
	}
	if len(raw) > maxHTTPResponseBytes {
		return presentationUploadResponse{}, fmt.Errorf("hub response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr protocolAPIError
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Code != "" {
			return presentationUploadResponse{}, &HubAPIError{Code: apiErr.Error.Code, Message: apiErr.Error.Message, Retryable: apiErr.Error.Retryable}
		}
		return presentationUploadResponse{}, fmt.Errorf("hub HTTP status %d", resp.StatusCode)
	}
	var result presentationUploadResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return presentationUploadResponse{}, fmt.Errorf("decode presentation response: %w", err)
	}
	if !strings.HasPrefix(result.PresentationID, "prs_") || result.FileName != logicalName ||
		result.ContentType != contentType || result.SizeBytes != info.Size() || !strings.EqualFold(result.SHA256, sha) ||
		!result.ExpiresAt.After(time.Now().UTC()) {
		return presentationUploadResponse{}, fmt.Errorf("hub returned invalid presentation metadata")
	}
	return result, nil
}

func hashPresentationFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxPresentationUploadSize+1))
	if err != nil {
		return "", err
	}
	if written > maxPresentationUploadSize {
		return "", fmt.Errorf("presentation file exceeds size limit")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func safePresentationFileName(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	value = path.Base(value)
	value = strings.Map(func(char rune) rune {
		if char < 32 || char == 127 || char == '/' || char == '\\' {
			return -1
		}
		return char
	}, value)
	if value == "" || value == "." || value == ".." {
		return "file"
	}
	return value
}
