//go:build !windows

package node

import "os"

func workingPathIsLink(_ string, info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink != 0
}
