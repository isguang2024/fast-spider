//go:build !windows

package nodeupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	if filepath.Clean(stagedPath) == filepath.Clean(targetPath) {
		return errors.New("staged update must be separate from the running executable")
	}
	preflight := targetPath + ".update-preflight"
	if err := os.WriteFile(preflight, []byte("ok"), 0o600); err != nil {
		return fmt.Errorf("current executable directory is not writable: %w", err)
	}
	_ = os.Remove(preflight)
	args := []string{"self-update-apply", "--target", targetPath, "--pid", fmt.Sprint(os.Getpid()), "--data-dir", dataDir}
	if background {
		args = append(args, "--background")
	}
	cmd := exec.Command(stagedPath, args...)
	cmd.Dir = filepath.Dir(stagedPath)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

func ApplyHelper(targetPath, dataDir string, oldPID int, background bool) error {
	if oldPID <= 0 || targetPath == "" || dataDir == "" {
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
	if filepath.Clean(selfPath) == filepath.Clean(targetPath) {
		return errors.New("self update helper cannot replace itself in place")
	}
	newPath := targetPath + ".new"
	previousPath := targetPath + ".previous"
	_ = os.Remove(newPath)
	if err := copyFile(selfPath, newPath); err != nil {
		return err
	}
	if err := waitForProcessExit(oldPID, 60*time.Second); err != nil {
		_ = os.Remove(newPath)
		return err
	}
	if err := os.Remove(previousPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(newPath)
		return err
	}
	if err := os.Rename(targetPath, previousPath); err != nil {
		_ = os.Remove(newPath)
		return err
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
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(targetPath)
		_ = os.Rename(previousPath, targetPath)
		rollback := exec.Command(targetPath, args...)
		rollback.Dir = filepath.Dir(targetPath)
		rollback.Stdout = io.Discard
		rollback.Stderr = io.Discard
		rollback.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		_ = rollback.Start()
		return fmt.Errorf("restart updated executable: %w", err)
	}
	return nil
}

func waitForProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for old process %d to exit", pid)
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
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	return nil
}
