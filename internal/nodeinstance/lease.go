package nodeinstance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrAlreadyRunning = errors.New("another Fast Spider Node instance is already running for this data directory")

type Lease struct {
	file *os.File
}

func Acquire(dataDir string) (*Lease, error) {
	if dataDir == "" {
		return nil, errors.New("node data directory is required")
	}
	runDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Node run directory: %w", err)
	}
	path := filepath.Join(runDir, "node.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Node instance lock: %w", err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, ErrAlreadyRunning) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock Node instance: %w", err)
	}
	return &Lease{file: file}, nil
}

func (l *Lease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := unlockFile(file)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
