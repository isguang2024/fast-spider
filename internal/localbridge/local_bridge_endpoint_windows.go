//go:build windows

package localbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Windows AF_UNIX accepts a shorter path in practice than the sockaddr
// constant exposed by the OS. Keep the normal endpoint under data-dir when it
// fits, use its 8.3 alias when available, and otherwise fall back to a stable
// per-data-dir name under the current user's home-directory ACL.
const maxWindowsLocalBridgeEndpoint = 56

func platformLocalBridgeEndpoint(dataDir string) string {
	dataDir = filepath.Clean(dataDir)
	endpoint := filepath.Join(dataDir, "local", "bridge.sock")
	if len(endpoint) <= maxWindowsLocalBridgeEndpoint {
		return endpoint
	}
	if shortDataDir := windowsShortPath(dataDir); shortDataDir != "" {
		shortEndpoint := filepath.Join(shortDataDir, "local", "bridge.sock")
		if len(shortEndpoint) <= maxWindowsLocalBridgeEndpoint {
			return shortEndpoint
		}
	}
	root := userHomeRoot()
	if shortRoot := windowsShortPath(root); shortRoot != "" {
		root = shortRoot
	}
	digest := sha256.Sum256([]byte(cleanDataDirKey(dataDir)))
	name := "fs-" + hex.EncodeToString(digest[:8]) + ".sock"
	if root != "" {
		return filepath.Join(root, name)
	}
	return filepath.Join(dataDir, name)
}

func userHomeRoot() string {
	if home, err := os.UserHomeDir(); err == nil {
		return strings.TrimSpace(home)
	}
	return strings.TrimSpace(os.Getenv("USERPROFILE"))
}

func cleanDataDirKey(path string) string {
	return strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
}

func windowsShortPath(path string) string {
	if path == "" {
		return ""
	}
	input, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	buffer := make([]uint16, windows.MAX_PATH*4)
	n, err := windows.GetShortPathName(input, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || int(n) >= len(buffer) {
		return ""
	}
	return windows.UTF16ToString(buffer[:n])
}
