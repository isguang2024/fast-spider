package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxCloudResultBytes = 8 << 20
	maxCloudResultPage  = 1 << 20
)

type cloudResultCreateResponse struct {
	ResultID  string    `json:"resultId"`
	Revision  int64     `json:"revision"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type cloudResultManifestResponse struct {
	ResultID string          `json:"resultId"`
	Status   string          `json:"status"`
	Revision int64           `json:"revision"`
	Manifest json.RawMessage `json:"manifest,omitempty"`
}

// PublishCloudResult stores a bounded Cloud assistant result in the Hub Result
// Pool. The operation is idempotent by key+content hash and never returns page
// artifact identifiers to the agent callback path.
func (c *Client) PublishCloudResult(ctx context.Context, sourceSessionID, idempotencyKey, text string) (map[string]any, error) {
	if strings.TrimSpace(sourceSessionID) == "" || len(idempotencyKey) < 12 || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\x00\r\n") {
		return nil, fmt.Errorf("invalid cloud result identity")
	}
	data := []byte(text)
	if len(data) > maxCloudResultBytes {
		return nil, fmt.Errorf("cloud result exceeds %d bytes", maxCloudResultBytes)
	}
	sum := sha256.Sum256(data)
	sha := "sha256:" + hex.EncodeToString(sum[:])
	requestHash := sha
	state, err := c.State()
	if err != nil {
		return nil, err
	}
	hubURL, err := c.normalizeHubURL(state.HubURL)
	if err != nil {
		return nil, err
	}
	token, err := c.issueDeviceToken(ctx, state)
	if err != nil {
		return nil, err
	}
	var created cloudResultCreateResponse
	createErr := c.postJSON(ctx, hubURL+"/node/v1/results", token.DeviceToken, map[string]any{
		"idempotencyKey": idempotencyKey,
		"requestHash":    requestHash,
	}, &created)
	if createErr != nil {
		if recovered, lookupErr := c.lookupCloudResult(ctx, hubURL, token.DeviceToken, idempotencyKey, requestHash); lookupErr == nil {
			created = recovered
		} else {
			return nil, createErr
		}
	}
	if created.ResultID == "" {
		return nil, fmt.Errorf("hub returned invalid result metadata")
	}
	if created.Status == "ready" || created.Status == "failed" || created.Status == "aborted" {
		return cloudResultMetadata(created.ResultID, created.Status, int64(len(data)), sha, cloudResultPageCount(data)), nil
	}
	revision := created.Revision
	for pageNo, page := range cloudResultPages(data) {
		artifactID, err := c.uploadCloudResultPage(ctx, token.DeviceToken, page, pageNo, hubURL)
		if err != nil {
			return nil, err
		}
		body := map[string]any{"pageNo": pageNo, "artifactId": artifactID, "expectedRevision": revision}
		var attached cloudResultCreateResponse
		attachErr := c.postJSON(ctx, hubURL+"/node/v1/results/"+url.PathEscape(created.ResultID)+"/pages", token.DeviceToken, body, &attached)
		if attachErr != nil {
			manifest, lookupErr := c.lookupCloudResultByID(ctx, hubURL, token.DeviceToken, created.ResultID)
			if lookupErr != nil || manifest.Status != "open" || manifest.Revision <= revision {
				return nil, attachErr
			}
			revision = manifest.Revision
			continue
		}
		revision = attached.Revision
	}
	manifest, _ := json.Marshal(map[string]any{"bytes": len(data), "sha256": sha, "pageCount": cloudResultPageCount(data)})
	var committed cloudResultCreateResponse
	commitErr := c.postJSON(ctx, hubURL+"/node/v1/results/"+url.PathEscape(created.ResultID)+"/commit", token.DeviceToken, map[string]any{
		"manifest":         json.RawMessage(manifest),
		"expectedRevision": revision,
	}, &committed)
	if commitErr != nil {
		if recovered, lookupErr := c.lookupCloudResultByID(ctx, hubURL, token.DeviceToken, created.ResultID); lookupErr == nil && recovered.Status == "ready" {
			return cloudResultMetadata(created.ResultID, recovered.Status, int64(len(data)), sha, cloudResultPageCount(data)), nil
		}
		return nil, commitErr
	}
	return cloudResultMetadata(committed.ResultID, committed.Status, int64(len(data)), sha, cloudResultPageCount(data)), nil
}

func (c *Client) lookupCloudResult(ctx context.Context, hubURL, token, idempotencyKey, requestHash string) (cloudResultCreateResponse, error) {
	values := url.Values{}
	values.Set("idempotencyKey", idempotencyKey)
	values.Set("requestHash", requestHash)
	var response cloudResultManifestResponse
	if err := c.artifactRequest(ctx, http.MethodGet, hubURL+"/node/v1/results/lookup?"+values.Encode(), token, nil, "", &response); err != nil {
		return cloudResultCreateResponse{}, err
	}
	return cloudResultCreateResponse{ResultID: response.ResultID, Revision: response.Revision, Status: response.Status}, nil
}

func (c *Client) lookupCloudResultByID(ctx context.Context, hubURL, token, resultID string) (cloudResultManifestResponse, error) {
	var response cloudResultManifestResponse
	if err := c.artifactRequest(ctx, http.MethodGet, hubURL+"/node/v1/results/"+url.PathEscape(resultID)+"/manifest", token, nil, "", &response); err != nil {
		return cloudResultManifestResponse{}, err
	}
	return response, nil
}

func (c *Client) uploadCloudResultPage(ctx context.Context, token string, page []byte, pageNo int, hubURL string) (string, error) {
	dir := filepath.Join(c.cfg.DataDir, "agent-results")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "page-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(page); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	result, err := c.uploadArtifactPath(ctx, "", fmt.Sprintf("cloud-result-%06d.txt", pageNo), "text/plain; charset=utf-8", path)
	if err != nil {
		return "", err
	}
	return result.ArtifactID, nil
}

func cloudResultPages(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	pages := make([][]byte, 0, (len(data)+maxCloudResultPage-1)/maxCloudResultPage)
	for len(data) > 0 {
		cut := len(data)
		if cut > maxCloudResultPage {
			cut = maxCloudResultPage
			for cut > 0 && cut > maxCloudResultPage-3 && !utf8.Valid(data[:cut]) {
				cut--
			}
		}
		pages = append(pages, append([]byte(nil), data[:cut]...))
		data = data[cut:]
	}
	return pages
}

func cloudResultPageCount(data []byte) int { return len(cloudResultPages(data)) }

func cloudResultMetadata(resultID, status string, bytes int64, sha string, pages int) map[string]any {
	return map[string]any{"resultId": resultID, "status": status, "bytes": bytes, "sha256": sha, "pageCount": pages}
}
