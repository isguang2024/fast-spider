//go:build !windows

package agent

import (
	"os"
	"path/filepath"
)

func replaceAgentFile(from, to string) error { return os.Rename(from, to) }

func syncAgentParentDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
