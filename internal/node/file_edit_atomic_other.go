//go:build !windows

package node

import "os"

func atomicReplaceEditedFile(from, to string) error { return os.Rename(from, to) }

func atomicCreateEditedFile(from, to string) error {
	if err := os.Link(from, to); err != nil {
		return err
	}
	return os.Remove(from)
}

func syncEditedParent(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
