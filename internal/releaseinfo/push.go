package releaseinfo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	NodeUpdatePushFormat = "fast-spider-node-update-push/v1"
	pushMarkerFileName   = "push.json"
	maxPushMarkerBytes   = 4096
)

type NodeUpdatePush struct {
	Format    string `json:"format"`
	PushID    string `json:"pushId"`
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	CreatedAt string `json:"createdAt"`
}

func CreateNodeUpdatePush(releaseDir, platform string, now time.Time) (NodeUpdatePush, error) {
	root, platform, err := validatePushRootAndPlatform(releaseDir, platform)
	if err != nil {
		return NodeUpdatePush{}, err
	}
	nodeDir := filepath.Join(root, "node", platform)
	versionRaw, err := os.ReadFile(filepath.Join(nodeDir, "version.txt"))
	if err != nil {
		return NodeUpdatePush{}, err
	}
	version := strings.TrimSpace(string(versionRaw))
	if _, err := ParseVersion(version); err != nil {
		return NodeUpdatePush{}, fmt.Errorf("node release version: %w", err)
	}
	sha256Hex, _, err := FileSHA256(filepath.Join(nodeDir, NodeReleaseFilename(platform)))
	if err != nil {
		return NodeUpdatePush{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	marker := NodeUpdatePush{
		Format:    NodeUpdatePushFormat,
		PushID:    fmt.Sprintf("%s-%d-%s", version, now.UnixNano(), sha256Hex[:12]),
		Platform:  platform,
		Version:   version,
		SHA256:    sha256Hex,
		CreatedAt: now.Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return NodeUpdatePush{}, err
	}
	raw = append(raw, '\n')
	if len(raw) > maxPushMarkerBytes {
		return NodeUpdatePush{}, errors.New("update push marker is too large")
	}
	path := filepath.Join(nodeDir, pushMarkerFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return NodeUpdatePush{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return NodeUpdatePush{}, err
	}
	return marker, nil
}

func ReadNodeUpdatePush(releaseDir, platform string) (NodeUpdatePush, error) {
	root, platform, err := validatePushRootAndPlatform(releaseDir, platform)
	if err != nil {
		return NodeUpdatePush{}, err
	}
	nodeDir := filepath.Join(root, "node", platform)
	raw, err := os.ReadFile(filepath.Join(nodeDir, pushMarkerFileName))
	if err != nil {
		return NodeUpdatePush{}, err
	}
	if len(raw) == 0 || len(raw) > maxPushMarkerBytes {
		return NodeUpdatePush{}, errors.New("update push marker is invalid")
	}
	var marker NodeUpdatePush
	if err := json.Unmarshal(raw, &marker); err != nil {
		return NodeUpdatePush{}, err
	}
	if marker.Format != NodeUpdatePushFormat || marker.Platform != platform || marker.PushID == "" || len(marker.PushID) > 128 {
		return NodeUpdatePush{}, errors.New("update push marker fields are invalid")
	}
	if _, err := ParseVersion(marker.Version); err != nil {
		return NodeUpdatePush{}, fmt.Errorf("update push version: %w", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, marker.CreatedAt); err != nil {
		return NodeUpdatePush{}, errors.New("update push createdAt is invalid")
	}
	if len(marker.SHA256) != 64 {
		return NodeUpdatePush{}, errors.New("update push sha256 is invalid")
	}
	return marker, nil
}

func NodeReleaseFilename(platform string) string {
	if strings.HasPrefix(platform, "windows-") {
		return "fast-spider-node.exe"
	}
	return "fast-spider-node"
}

func validatePushRootAndPlatform(releaseDir, platform string) (string, string, error) {
	root := filepath.Clean(strings.TrimSpace(releaseDir))
	if root == "." || !filepath.IsAbs(root) {
		return "", "", errors.New("release directory must be absolute")
	}
	platform = strings.TrimSpace(platform)
	if platform == "" || len(platform) > 64 {
		return "", "", errors.New("platform is invalid")
	}
	for i, r := range platform {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			if i == 0 && (r == '-' || r == '_' || r == '.') {
				return "", "", errors.New("platform is invalid")
			}
			continue
		}
		return "", "", errors.New("platform is invalid")
	}
	return root, platform, nil
}
