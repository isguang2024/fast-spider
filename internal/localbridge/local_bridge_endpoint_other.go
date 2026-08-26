//go:build !windows

package localbridge

import "path/filepath"

func platformLocalBridgeEndpoint(dataDir string) string {
	return filepath.Join(dataDir, "local", "bridge.sock")
}
