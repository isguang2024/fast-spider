//go:build windows

package nodeupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func StartApply(stagedPath, targetPath, dataDir string, background bool) error {
	stagedPath, err := filepath.Abs(stagedPath)
	if err != nil {
		return err
	}
	targetPath, err = filepath.Abs(targetPath)
	if err != nil {
		return err
	}
	if samePath(stagedPath, targetPath) {
		return errors.New("staged update must be separate from the running executable")
	}
	preflight := targetPath + ".update-preflight"
	if err := os.WriteFile(preflight, []byte("ok"), 0o600); err != nil {
		return fmt.Errorf("current executable directory is not writable: %w", err)
	}
	_ = os.Remove(preflight)
	args := []string{"self-update-apply", "--target", targetPath, "--pid", strconv.Itoa(os.Getpid()), "--data-dir", dataDir}
	if background {
		args = append(args, "--background")
	}
	cmd := exec.Command(stagedPath, args...)
	cmd.Dir = filepath.Dir(stagedPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.Start()
}

func ApplyHelper(targetPath, dataDir string, oldPID int, background bool) error {
	if oldPID <= 0 || strings.TrimSpace(targetPath) == "" || strings.TrimSpace(dataDir) == "" {
		return errors.New("self update parameters are invalid")
	}
	targetPath, err := filepath.Abs(targetPath)
	if err != nil {
		return err
	}
	selfPath, err := os.Executable()
	if err != nil {
		return err
	}
	selfPath, err = filepath.Abs(selfPath)
	if err != nil {
		return err
	}
	if samePath(selfPath, targetPath) {
		return errors.New("self update helper cannot replace itself in place")
	}
	newPath := targetPath + ".new"
	previousPath := targetPath + ".previous"
	_ = os.Remove(newPath)
	if err := copyFile(selfPath, newPath); err != nil {
		return err
	}
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		_ = os.Remove(previousPath)
		if err := os.Rename(targetPath, previousPath); err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if err := os.Rename(newPath, targetPath); err != nil {
			_ = os.Rename(previousPath, targetPath)
			return fmt.Errorf("publish updated executable: %w", err)
		}
		args := []string{"ui", "--data-dir", dataDir}
		if background {
			args = append(args, "--background")
		}
		cmd := exec.Command(targetPath, args...)
		cmd.Dir = filepath.Dir(targetPath)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		if err := cmd.Start(); err != nil {
			_ = os.Remove(targetPath)
			_ = os.Rename(previousPath, targetPath)
			rollback := exec.Command(targetPath, args...)
			rollback.Dir = filepath.Dir(targetPath)
			rollback.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
			_ = rollback.Start()
			return fmt.Errorf("restart updated executable: %w", err)
		}
		return nil
	}
	_ = os.Remove(newPath)
	return fmt.Errorf("timed out waiting for old process %d to release executable: %w", oldPID, lastErr)
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return closeErr
	}
	return nil
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
