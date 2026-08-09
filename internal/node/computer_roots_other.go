//go:build !windows

package node

// Fast Spider 0.3 no longer enumerates or stores filesystem roots. The Node
// uses the current OS user's normal filesystem permissions directly.
