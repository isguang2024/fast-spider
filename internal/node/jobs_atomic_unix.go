//go:build !windows

package node

import "os"

func replaceJobStoreFile(source, target string) error {
	return os.Rename(source, target)
}
