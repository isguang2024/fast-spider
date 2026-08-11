package nodeupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/releaseinfo"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	maxManifestBytes = 64 << 10
	maxNodeBytes     = 512 << 20
)

type Status struct {
	CurrentVersion string               `json:"currentVersion"`
	LatestVersion  string               `json:"latestVersion,omitempty"`
	Available      bool                 `json:"available"`
	Ready          bool                 `json:"ready"`
	Platform       string               `json:"platform"`
	SizeBytes      int64                `json:"sizeBytes,omitempty"`
	Manifest       releaseinfo.Manifest `json:"-"`
}

func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// CleanupConsumedCurrent removes the staging directory for the running Node
// version only after its ready marker has been consumed. A remaining ready
// marker makes the operation a no-op so an out-of-order caller cannot remove
// a pending update or its diagnostic evidence.
func CleanupConsumedCurrent(dataDir, currentVersion string) error {
	version := strings.TrimSpace(currentVersion)
	if _, err := releaseinfo.ParseVersion(version); err != nil {
		return fmt.Errorf("current Node version is invalid: %w", err)
	}
	updatesDir := filepath.Join(dataDir, "updates")
	if _, err := os.Lstat(filepath.Join(updatesDir, "ready.json")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	currentDir := filepath.Join(updatesDir, version)
	info, err := os.Lstat(currentDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	return os.RemoveAll(currentDir)
}

func CleanupStale(dataDir, currentVersion string) error {
	updatesDir := filepath.Join(dataDir, "updates")
	entries, err := os.ReadDir(updatesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := releaseinfo.ParseVersion(entry.Name()); err != nil {
			continue
		}
		comparison, err := releaseinfo.Compare(entry.Name(), currentVersion)
		if err != nil || comparison >= 0 {
			continue
		}
		if err := os.RemoveAll(filepath.Join(updatesDir, entry.Name())); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func cleanupStagedVersions(dataDir string, keepVersions ...string) error {
	updatesDir := filepath.Join(dataDir, "updates")
	entries, err := os.ReadDir(updatesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	keep := make(map[string]struct{}, len(keepVersions))
	for _, version := range keepVersions {
		keep[strings.TrimSpace(version)] = struct{}{}
	}
	var cleanupErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := keep[entry.Name()]; ok {
			continue
		}
		if _, err := releaseinfo.ParseVersion(entry.Name()); err != nil {
			continue
		}
		if err := os.RemoveAll(filepath.Join(updatesDir, entry.Name())); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func Check(ctx context.Context, hubURL, encodedHubPublicKey, currentVersion string) (Status, error) {
	platform := Platform()
	manifest, err := FetchManifest(ctx, hubURL, encodedHubPublicKey, "/api/v1/node/releases/"+platform+"/latest", "node", "fast-spider-node", platform)
	if err != nil {
		return Status{CurrentVersion: currentVersion, Platform: platform}, err
	}
	comparison, err := releaseinfo.Compare(currentVersion, manifest.Version)
	if err != nil {
		return Status{}, err
	}
	return Status{
		CurrentVersion: currentVersion,
		LatestVersion:  manifest.Version,
		Available:      comparison < 0,
		Platform:       platform,
		SizeBytes:      manifest.SizeBytes,
		Manifest:       manifest,
	}, nil
}

func Stage(ctx context.Context, dataDir, hubURL, encodedHubPublicKey, currentVersion string, status Status) (Status, string, error) {
	if status.Manifest.Version == "" {
		checked, err := Check(ctx, hubURL, encodedHubPublicKey, currentVersion)
		if err != nil {
			return status, "", err
		}
		status = checked
	}
	if !status.Available {
		return status, "", nil
	}
	if status.Manifest.SizeBytes > maxNodeBytes {
		return status, "", errors.New("node update exceeds size limit")
	}
	if err := cleanupStagedVersions(dataDir, currentVersion, status.Manifest.Version); err != nil {
		return status, "", fmt.Errorf("cleanup stale Node updates: %w", err)
	}
	dir := filepath.Join(dataDir, "updates", status.Manifest.Version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return status, "", err
	}
	artifact := filepath.Join(dir, nodeFilename())
	if matches, _ := fileMatchesManifest(artifact, status.Manifest); !matches {
		if err := DownloadVerified(ctx, hubURL, status.Manifest, artifact); err != nil {
			return status, "", err
		}
	}
	readyPath := filepath.Join(dataDir, "updates", "ready.json")
	raw, err := json.MarshalIndent(status.Manifest, "", "  ")
	if err != nil {
		return status, "", err
	}
	raw = append(raw, '\n')
	if err := writeAtomic(readyPath, raw, 0o600); err != nil {
		return status, "", err
	}
	status.Ready = true
	return status, artifact, nil
}

func Ready(dataDir, encodedHubPublicKey, currentVersion string) (Status, string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "updates", "ready.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Status{CurrentVersion: currentVersion, Platform: Platform()}, "", nil
	}
	if err != nil {
		return Status{}, "", err
	}
	var manifest releaseinfo.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Status{}, "", err
	}
	if err := verifyManifest(encodedHubPublicKey, manifest, "node", "fast-spider-node", Platform()); err != nil {
		return Status{}, "", err
	}
	comparison, err := releaseinfo.Compare(currentVersion, manifest.Version)
	if err != nil {
		return Status{}, "", err
	}
	artifact := filepath.Join(dataDir, "updates", manifest.Version, nodeFilename())
	matches, err := fileMatchesManifest(artifact, manifest)
	if comparison >= 0 {
		_ = os.Remove(filepath.Join(dataDir, "updates", "ready.json"))
		return Status{CurrentVersion: currentVersion, LatestVersion: manifest.Version, Available: false, Platform: Platform()}, "", err
	}
	if err != nil || !matches {
		return Status{CurrentVersion: currentVersion, LatestVersion: manifest.Version, Available: true, Platform: Platform()}, "", err
	}
	return Status{CurrentVersion: currentVersion, LatestVersion: manifest.Version, Available: true, Ready: true, Platform: Platform(), SizeBytes: manifest.SizeBytes, Manifest: manifest}, artifact, nil
}

func FetchManifest(ctx context.Context, hubURL, encodedHubPublicKey, manifestPath, expectedKind, expectedID, expectedPlatform string) (releaseinfo.Manifest, error) {
	endpoint, err := joinHubURL(hubURL, manifestPath)
	if err != nil {
		return releaseinfo.Manifest{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return releaseinfo.Manifest{}, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return releaseinfo.Manifest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return releaseinfo.Manifest{}, fmt.Errorf("release manifest HTTP status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return releaseinfo.Manifest{}, err
	}
	if len(raw) > maxManifestBytes {
		return releaseinfo.Manifest{}, errors.New("release manifest exceeds limit")
	}
	var manifest releaseinfo.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return releaseinfo.Manifest{}, err
	}
	if err := verifyManifest(encodedHubPublicKey, manifest, expectedKind, expectedID, expectedPlatform); err != nil {
		return releaseinfo.Manifest{}, err
	}
	return manifest, nil
}

func DownloadVerified(ctx context.Context, hubURL string, manifest releaseinfo.Manifest, destination string) error {
	endpoint, err := joinHubURL(hubURL, manifest.DownloadPath)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("release download HTTP status %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	tmp := destination + ".tmp"
	_ = os.Remove(tmp)
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, manifest.SizeBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != manifest.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return errors.New("release artifact verification failed")
	}
	if err := os.Chmod(tmp, 0o700); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(destination)
	if err := os.Rename(tmp, destination); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func verifyManifest(encodedHubPublicKey string, manifest releaseinfo.Manifest, expectedKind, expectedID, expectedPlatform string) error {
	publicKey, err := security.DecodePublicKey(encodedHubPublicKey)
	if err != nil {
		return err
	}
	if err := releaseinfo.Verify(publicKey, manifest); err != nil {
		return err
	}
	if manifest.Kind != expectedKind || manifest.ID != expectedID || manifest.Platform != expectedPlatform {
		return errors.New("release manifest identity mismatch")
	}
	return nil
}

func fileMatchesManifest(path string, manifest releaseinfo.Manifest) (bool, error) {
	sha256Hex, sizeBytes, err := releaseinfo.FileSHA256(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sizeBytes == manifest.SizeBytes && sha256Hex == manifest.SHA256, nil
}

func joinHubURL(hubURL, relativePath string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(hubURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("hub URL is invalid")
	}
	if !strings.HasPrefix(relativePath, "/") || strings.Contains(relativePath, "..") || strings.ContainsAny(relativePath, "?#") {
		return "", errors.New("release path is invalid")
	}
	return strings.TrimRight(parsed.String(), "/") + relativePath, nil
}

func nodeFilename() string {
	if runtime.GOOS == "windows" {
		return "fast-spider-node.exe"
	}
	return "fast-spider-node"
}

func writeAtomic(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
