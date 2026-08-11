package nodeupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const legacyNodeExecutableName = "fast-spider-node.exe"

var (
	legacyNodeTempNamePattern   = regexp.MustCompile(`(?i)^\.fast-spider-node\.new-[0-9a-f]{32}\.tmp$`)
	legacyNodeBackupNamePattern = regexp.MustCompile(`(?i)^fast-spider-node-(?:pre-[0-9]+\.[0-9]+\.[0-9]+-)?([0-9]{8}T[0-9]{6}Z)\.exe$`)
)

type legacyReparseCheck func(string) (bool, error)

func isLegacyNodeTempName(name string) bool {
	return legacyNodeTempNamePattern.MatchString(name)
}

func isLegacyNodeBackupName(name string) bool {
	matches := legacyNodeBackupNamePattern.FindStringSubmatch(name)
	if len(matches) != 2 {
		return false
	}
	_, err := time.Parse("20060102T150405Z", strings.ToUpper(matches[1]))
	return err == nil
}

func cleanupLegacyWindowsInstallArtifacts(executablePath string, isReparse legacyReparseCheck) error {
	if !strings.EqualFold(filepath.Base(strings.TrimSpace(executablePath)), legacyNodeExecutableName) {
		return nil
	}
	if !filepath.IsAbs(executablePath) {
		return errors.New("current Node executable path must be absolute")
	}
	executablePath = filepath.Clean(executablePath)
	binDir := filepath.Dir(executablePath)
	if err := requireLegacyCleanupRoot(executablePath, binDir, isReparse); err != nil {
		return err
	}

	entries, err := os.ReadDir(binDir)
	if err != nil {
		return fmt.Errorf("read legacy Node install directory: %w", err)
	}
	var cleanupErr error
	for _, entry := range entries {
		name := entry.Name()
		if strings.EqualFold(name, legacyNodeExecutableName) || strings.EqualFold(name, legacyNodeExecutableName+".previous") {
			continue
		}
		path := filepath.Join(binDir, name)
		switch {
		case strings.EqualFold(name, ".node-update-backup-path"), isLegacyNodeTempName(name):
			if err := removeLegacyRegularFile(path, isReparse); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		case strings.EqualFold(name, "backups"):
			if err := cleanupLegacyBackupDirectory(path, isReparse); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
	}
	return cleanupErr
}

func requireLegacyCleanupRoot(executablePath, binDir string, isReparse legacyReparseCheck) error {
	executableInfo, err := os.Lstat(executablePath)
	if err != nil {
		return fmt.Errorf("inspect current Node executable: %w", err)
	}
	if !executableInfo.Mode().IsRegular() {
		return errors.New("current Node executable is not a regular file")
	}
	reparse, err := isReparse(executablePath)
	if err != nil {
		return fmt.Errorf("inspect current Node executable attributes: %w", err)
	}
	if reparse {
		return errors.New("current Node executable must not be a reparse point")
	}
	binInfo, err := os.Lstat(binDir)
	if err != nil {
		return fmt.Errorf("inspect current Node install directory: %w", err)
	}
	if !binInfo.IsDir() {
		return errors.New("current Node install directory is not a directory")
	}
	reparse, err = isReparse(binDir)
	if err != nil {
		return fmt.Errorf("inspect current Node install directory attributes: %w", err)
	}
	if reparse {
		return errors.New("current Node install directory must not be a reparse point")
	}
	return nil
}

func removeLegacyRegularFile(path string, isReparse legacyReparseCheck) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy Node file: %w", err)
	}
	reparse, err := isReparse(path)
	if err != nil {
		return fmt.Errorf("inspect legacy Node file attributes: %w", err)
	}
	if reparse {
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove legacy Node file: %w", err)
	}
	return nil
}

func cleanupLegacyBackupDirectory(path string, isReparse legacyReparseCheck) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy Node backup directory: %w", err)
	}
	reparse, err := isReparse(path)
	if err != nil {
		return fmt.Errorf("inspect legacy Node backup directory attributes: %w", err)
	}
	if reparse {
		return nil
	}
	if !info.IsDir() {
		return nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read legacy Node backup directory: %w", err)
	}
	var cleanupErr error
	for _, entry := range entries {
		if !isLegacyNodeBackupName(entry.Name()) {
			continue
		}
		if err := removeLegacyRegularFile(filepath.Join(path, entry.Name()), isReparse); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	remaining, err := os.ReadDir(path)
	if err != nil {
		return errors.Join(cleanupErr, fmt.Errorf("re-read legacy Node backup directory: %w", err))
	}
	if len(remaining) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove empty legacy Node backup directory: %w", err))
		}
	}
	return cleanupErr
}
