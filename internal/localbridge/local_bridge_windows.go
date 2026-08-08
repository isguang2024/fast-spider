//go:build windows

package localbridge

// Windows 10/11 supports AF_UNIX. The socket lives under the current user's
// Fast Spider data directory and inherits that directory's Windows ACL, so the
// personal-use MVP does not maintain a second SID/SDDL policy layer here.
func hardenLocalBridgeSocket(string) error { return nil }
