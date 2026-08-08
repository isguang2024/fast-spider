//go:build !windows

package localbridge

import "os"

func hardenLocalBridgeSocket(endpoint string) error {
	return os.Chmod(endpoint, 0o600)
}
