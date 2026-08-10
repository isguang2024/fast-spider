package node

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	saleSmartlyAPIBase        = "https://api.salesmartly.com"
	saleSmartlyOSSOrigin      = "https://mix-ads.oss-accelerate.aliyuncs.com"
	saleSmartlyPublicOrigin   = "https://assets.salesmartly.com"
	saleSmartlySourceURL      = "https://app.salesmartly.com/next/preview/"
	saleSmartlyPluginSignSalt = "9c2210efee9b603e09f8d742917bb538"
	saleSmartlyCacheTTL       = 20 * time.Minute
	saleSmartlyExpirySkew     = 30 * time.Second
	maxPresentationUploadSize = int64(200 << 20)
)

// These are public SaleSmartly plugin identifiers, not credentials. Keep the
// pool deliberately small: enough for simple failover without copying Tibbs'
// larger resource-provider health machinery into Fast Spider.
var saleSmartlyPluginCandidates = [...]string{"b87x3n", "g101rfh", "f11lv7v"}

var ErrPresentationUpload = errors.New("presentation upload failed")

type saleSmartlyPublisher struct {
	http        *http.Client
	apiBase     string
	ossOrigin   string
	assetOrigin string
	now         func() time.Time

	mu     sync.Mutex
	guests map[string]saleSmartlyGuestCache
	leases map[string]saleSmartlyLeaseCache
}

type saleSmartlyGuestCache struct {
	token      string
	validUntil time.Time
}

type saleSmartlyLeaseCache struct {
	lease      saleSmartlyLease
	validUntil time.Time
}

type saleSmartlyLease struct {
	PluginID        string
	Path            string
	Dews            any
	AccessKeyID     string
	AccessKeySecret string
	SecurityToken   string
	Expiration      string
	ExpiresAt       time.Time
}

type publishedResource struct {
	URL         string
	FileName    string
	ContentType string
	SizeBytes   int64
	SHA256      string
}

type saleSmartlyEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

func newSaleSmartlyPublisher() *saleSmartlyPublisher {
	return &saleSmartlyPublisher{
		http:        &http.Client{Timeout: 5 * time.Minute},
		apiBase:     saleSmartlyAPIBase,
		ossOrigin:   saleSmartlyOSSOrigin,
		assetOrigin: saleSmartlyPublicOrigin,
		now:         time.Now,
		guests:      make(map[string]saleSmartlyGuestCache),
		leases:      make(map[string]saleSmartlyLeaseCache),
	}
}

func (c *Client) publishPresentationFile(ctx context.Context, filePath, logicalName, contentType string) (map[string]any, error) {
	if c == nil || c.publisher == nil {
		return nil, ErrPresentationUpload
	}
	result, err := c.publisher.PublishFile(ctx, filePath, logicalName, contentType)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPresentationUpload, err)
	}
	return map[string]any{
		"publicUrl":   result.URL,
		"fileName":    result.FileName,
		"contentType": result.ContentType,
		"sizeBytes":   result.SizeBytes,
		"sha256":      result.SHA256,
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

func (p *saleSmartlyPublisher) PublishFile(ctx context.Context, filePath, logicalName, contentType string) (publishedResource, error) {
	if err := ctx.Err(); err != nil {
		return publishedResource{}, err
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return publishedResource{}, err
	}
	if !info.Mode().IsRegular() {
		return publishedResource{}, ErrNotRegularFile
	}
	if info.Size() <= 0 || info.Size() > maxPresentationUploadSize {
		return publishedResource{}, fmt.Errorf("presentation file size is outside the allowed range")
	}
	logicalName = safePresentationFileName(logicalName)
	if logicalName == "file" {
		logicalName = safePresentationFileName(filepathBase(filePath))
	}
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	sha, err := hashPresentationFile(filePath)
	if err != nil {
		return publishedResource{}, err
	}

	messageType := saleSmartlyMessageType(contentType, logicalName)
	var lastErr error
	for _, pluginID := range saleSmartlyPluginCandidates {
		lease, err := p.acquireLease(ctx, pluginID, messageType)
		if err != nil {
			lastErr = err
			continue
		}
		objectKey, err := p.objectKey(lease.Path, logicalName)
		if err != nil {
			lastErr = err
			p.invalidateLease(pluginID, messageType)
			continue
		}
		if err := p.uploadOSS(ctx, filePath, info.Size(), objectKey, lease); err != nil {
			lastErr = err
			p.invalidateLease(pluginID, messageType)
			continue
		}
		return publishedResource{
			URL:         publicAssetURL(p.assetOrigin, objectKey),
			FileName:    logicalName,
			ContentType: contentType,
			SizeBytes:   info.Size(),
			SHA256:      sha,
		}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no SaleSmartly plugin candidate is available")
	}
	return publishedResource{}, lastErr
}

func (p *saleSmartlyPublisher) acquireLease(ctx context.Context, pluginID, messageType string) (saleSmartlyLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now().UTC()
	cacheKey := pluginID + "|" + messageType
	if cached, ok := p.leases[cacheKey]; ok && now.Before(cached.validUntil) &&
		(cached.lease.ExpiresAt.IsZero() || now.Add(saleSmartlyExpirySkew).Before(cached.lease.ExpiresAt)) {
		return cached.lease, nil
	}
	delete(p.leases, cacheKey)

	guestToken, err := p.guestTokenLocked(ctx, pluginID, now)
	if err != nil {
		return saleSmartlyLease{}, err
	}
	lease, err := p.fetchLease(ctx, pluginID, guestToken, messageType)
	if err != nil {
		delete(p.guests, pluginID)
		return saleSmartlyLease{}, err
	}
	validUntil := now.Add(saleSmartlyCacheTTL)
	if !lease.ExpiresAt.IsZero() {
		expiresBefore := lease.ExpiresAt.Add(-saleSmartlyExpirySkew)
		if expiresBefore.Before(validUntil) {
			validUntil = expiresBefore
		}
	}
	if !validUntil.After(now) {
		return saleSmartlyLease{}, errors.New("SaleSmartly STS expires too soon")
	}
	p.leases[cacheKey] = saleSmartlyLeaseCache{lease: lease, validUntil: validUntil}
	return lease, nil
}

func (p *saleSmartlyPublisher) guestTokenLocked(ctx context.Context, pluginID string, now time.Time) (string, error) {
	if cached, ok := p.guests[pluginID]; ok && now.Before(cached.validUntil) {
		return cached.token, nil
	}
	delete(p.guests, pluginID)
	meta, _ := json.Marshal(map[string]string{"phone": "", "email": "", "description": ""})
	body := map[string]string{
		"source_url":        saleSmartlySourceURL,
		"language":          "zh-CN",
		"ua":                "Fast-Spider-Node/1.0",
		"user_id":           md5Text(fmt.Sprintf("%s+%d", saleSmartlySourceURL, now.UnixNano())),
		"data":              base64.StdEncoding.EncodeToString(meta),
		"is_sandbox":        "0",
		"before_source_url": saleSmartlySourceURL,
		"label_names":       "",
		"custom_fields_ext": "",
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := p.postSaleSmartlyForm(ctx, pluginID, "", "/chat/msg-user/create-user", body, &payload); err != nil {
		return "", err
	}
	token := strings.TrimSpace(payload.Token)
	if token == "" {
		return "", errors.New("SaleSmartly guest session token is missing")
	}
	p.guests[pluginID] = saleSmartlyGuestCache{token: token, validUntil: now.Add(saleSmartlyCacheTTL)}
	return token, nil
}

func (p *saleSmartlyPublisher) fetchLease(ctx context.Context, pluginID, guestToken, messageType string) (saleSmartlyLease, error) {
	body := map[string]string{
		"module":      "chat",
		"module_path": "plugin/" + pluginID + "/" + messageType,
		"plugin_id":   pluginID,
		"env":         "chat",
		"platform":    "pc0",
		"btype":       "mix_ads",
	}
	var payload struct {
		Path string `json:"path"`
		Dews any    `json:"dews"`
		STS  struct {
			AccessKeyID     string `json:"access_key_id"`
			AccessKeySecret string `json:"access_key_secret"`
			SecurityToken   string `json:"security_token"`
			Expiration      string `json:"expiration"`
		} `json:"sts_config"`
	}
	if err := p.postSaleSmartlyForm(ctx, pluginID, guestToken, "/sys/company/plugin/get-oss-config", body, &payload); err != nil {
		return saleSmartlyLease{}, err
	}
	if strings.TrimSpace(payload.Path) == "" || strings.TrimSpace(payload.STS.AccessKeyID) == "" ||
		strings.TrimSpace(payload.STS.AccessKeySecret) == "" || strings.TrimSpace(payload.STS.SecurityToken) == "" {
		return saleSmartlyLease{}, errors.New("SaleSmartly STS response is incomplete")
	}
	expiresAt := p.now().UTC().Add(time.Hour)
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.STS.Expiration)); err == nil {
		expiresAt = parsed.UTC()
	}
	return saleSmartlyLease{
		PluginID:        pluginID,
		Path:            payload.Path,
		Dews:            payload.Dews,
		AccessKeyID:     payload.STS.AccessKeyID,
		AccessKeySecret: payload.STS.AccessKeySecret,
		SecurityToken:   payload.STS.SecurityToken,
		Expiration:      payload.STS.Expiration,
		ExpiresAt:       expiresAt,
	}, nil
}

func (p *saleSmartlyPublisher) postSaleSmartlyForm(ctx context.Context, pluginID, token, endpoint string, body map[string]string, out any) error {
	query := url.Values{
		"plugin_sign": {saleSmartlySign(body)},
		"plugin_id":   {pluginID},
		"over_time":   {""},
		"env":         {"chat"},
		"_":           {strconv.FormatInt(p.now().UnixMilli(), 10)},
		"_lt":         {token},
		"_u":          {""},
		"_xma_":       {""},
	}
	form := url.Values{}
	for key, value := range body {
		form.Set(key, value)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.apiBase, "/")+endpoint+"?"+query.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxHTTPResponseBytes {
		return errors.New("SaleSmartly API response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("SaleSmartly API status %d", resp.StatusCode)
	}
	var envelope saleSmartlyEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return errors.New("SaleSmartly API response is invalid")
	}
	if envelope.Code != 0 {
		return fmt.Errorf("SaleSmartly API error %d", envelope.Code)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return errors.New("SaleSmartly API data is invalid")
	}
	return nil
}

func (p *saleSmartlyPublisher) uploadOSS(ctx context.Context, filePath string, size int64, objectKey string, lease saleSmartlyLease) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	expiration := strings.TrimSpace(lease.Expiration)
	if expiration == "" {
		expiration = p.now().UTC().Add(saleSmartlyCacheTTL).Format(time.RFC3339)
	}
	policyJSON, _ := json.Marshal(map[string]any{
		"expiration": expiration,
		"conditions": []any{[]any{"content-length-range", 0, maxPresentationUploadSize}},
	})
	policy := base64.StdEncoding.EncodeToString(policyJSON)
	mac := hmac.New(sha1.New, []byte(lease.AccessKeySecret))
	_, _ = mac.Write([]byte(policy))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	var preamble bytes.Buffer
	writer := multipart.NewWriter(&preamble)
	fields := []struct{ name, value string }{
		{"Signature", signature},
		{"OSSAccessKeyId", lease.AccessKeyID},
		{"policy", policy},
		{"x-oss-security-token", lease.SecurityToken},
		{"x-oss-object-acl", saleSmartlyACL(lease.Dews)},
		{"key", objectKey},
	}
	for _, field := range fields {
		if err := writer.WriteField(field.name, field.value); err != nil {
			return err
		}
	}
	if _, err := writer.CreateFormFile("file", safePresentationFileName(filepathBase(filePath))); err != nil {
		return err
	}
	closingBoundary := []byte("\r\n--" + writer.Boundary() + "--\r\n")
	body := io.MultiReader(bytes.NewReader(preamble.Bytes()), io.LimitReader(file, size), bytes.NewReader(closingBoundary))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.ossOrigin, "/"), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.ContentLength = int64(preamble.Len()) + size + int64(len(closingBoundary))
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("SaleSmartly OSS status %d", resp.StatusCode)
	}
	return nil
}

func (p *saleSmartlyPublisher) objectKey(prefix, logicalName string) (string, error) {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" || strings.Contains(prefix, "\\") || strings.Contains(prefix, "..") {
		return "", errors.New("SaleSmartly object prefix is invalid")
	}
	id, err := security.RandomOpaque("fs_")
	if err != nil {
		return "", err
	}
	day := p.now().UTC().Format("20060102")
	return strings.Join([]string{prefix, "fast-spider", day, id, safePresentationFileName(logicalName)}, "/"), nil
}

func (p *saleSmartlyPublisher) invalidateLease(pluginID, messageType string) {
	p.mu.Lock()
	delete(p.leases, pluginID+"|"+messageType)
	p.mu.Unlock()
}

func saleSmartlyMessageType(contentType, fileName string) string {
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	lowerName := strings.ToLower(strings.TrimSpace(fileName))
	switch {
	case strings.HasPrefix(mimeType, "image/"), hasPresentationSuffix(lowerName, ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".bmp", ".svg"):
		return "2"
	case strings.HasPrefix(mimeType, "video/"), hasPresentationSuffix(lowerName, ".mp4", ".mov", ".webm", ".m4v", ".avi"):
		return "6"
	default:
		return "4"
	}
}

func hasPresentationSuffix(value string, suffixes ...string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}

func saleSmartlySign(data map[string]string) string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+data[key])
	}
	return md5Text(saleSmartlyPluginSignSalt + "&" + strings.Join(parts, "&"))
}

func md5Text(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hashPresentationFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxPresentationUploadSize+1)); err != nil {
		return "", err
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

func publicAssetURL(origin, objectKey string) string {
	parts := strings.Split(strings.TrimLeft(objectKey, "/"), "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.TrimRight(strings.TrimSpace(origin), "/") + "/" + strings.Join(parts, "/")
}

func saleSmartlyACL(value any) string {
	number := 0
	switch current := value.(type) {
	case float64:
		number = int(current)
	case json.Number:
		number, _ = strconv.Atoi(current.String())
	case string:
		number, _ = strconv.Atoi(current)
	}
	switch number {
	case 2:
		return "public-read-write"
	case 3:
		return "public-read"
	case 4:
		return "private"
	default:
		return "default"
	}
}

func filepathBase(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	return path.Base(value)
}
