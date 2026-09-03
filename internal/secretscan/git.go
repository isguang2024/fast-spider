package secretscan

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const maxGitMetadataBytes = 64 << 20

type gitBlob struct {
	oid string
	loc location
}

// ScanRepository scans tracked and untracked nonignored worktree files plus all
// index stages. When history is true, it additionally scans every object reachable
// from the release candidate at HEAD. Other branches, tags, tool-private refs, and
// dangling objects are excluded because they are not part of the candidate release.
func ScanRepository(ctx context.Context, repository string, history bool, options Options) ([]Finding, error) {
	root, err := gitRoot(ctx, repository)
	if err != nil {
		return nil, err
	}
	explicitMarkerFile := options.MarkerFile != ""
	if !explicitMarkerFile {
		candidate := filepath.Join(root, ".local", "public-private-markers.txt")
		if _, statErr := os.Stat(candidate); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			options.MarkerFile = candidate
		}
	}
	s, err := newScanner(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := s.scanWorktree(root); err != nil {
		return nil, err
	}
	indexBlobs, err := listIndexBlobs(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := s.scanGitBlobs(root, indexBlobs); err != nil {
		return nil, err
	}
	if history {
		historyBlobs, err := listReleaseObjects(ctx, root, s.limits.maxFiles)
		if err != nil {
			return nil, err
		}
		markers := s.markers
		if !explicitMarkerFile {
			s.markers = nil
		}
		err = s.scanGitBlobs(root, historyBlobs)
		s.markers = markers
		if err != nil {
			return nil, err
		}
	}
	return s.results(), nil
}

func gitRoot(ctx context.Context, repository string) (string, error) {
	if strings.TrimSpace(repository) == "" {
		repository = "."
	}
	output, err := runGitOutput(ctx, repository, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("resolve Git repository root")
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", errors.New("Git repository root is empty")
	}
	root, err = filepath.Abs(filepath.FromSlash(root))
	if err != nil {
		return "", errors.New("resolve absolute Git repository root")
	}
	return root, nil
}

func (s *scanner) scanWorktree(root string) error {
	output, err := runGitOutput(s.ctx, root, "ls-files", "-c", "-o", "--exclude-standard", "-z")
	if err != nil {
		return errors.New("enumerate worktree files")
	}
	for _, rawPath := range bytes.Split(output, []byte{0}) {
		if len(rawPath) == 0 {
			continue
		}
		path := string(rawPath)
		fullPath, err := containedPath(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(fullPath)
		if errors.Is(err, os.ErrNotExist) {
			deleted, checkErr := trackedPathDeleted(s.ctx, root, path)
			if checkErr != nil {
				return checkErr
			}
			if deleted {
				continue
			}
			return fmt.Errorf("worktree path disappeared: %s", s.safeLocator(filepath.ToSlash(path)))
		}
		if err != nil {
			return fmt.Errorf("inspect worktree path: %s", s.safeLocator(filepath.ToSlash(path)))
		}
		loc := location{source: "worktree", path: filepath.ToSlash(path)}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(fullPath)
			if err != nil {
				return fmt.Errorf("read worktree symlink: %s", s.safeLocator(loc.path))
			}
			if err := s.scanBytes(loc, []byte(target), 0); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported worktree entry: %s", s.safeLocator(loc.path))
		}
		data, err := readRegularFile(fullPath, info, s.limits.maxBlobBytes)
		if err != nil {
			return fmt.Errorf("read worktree path: %s", s.safeLocator(loc.path))
		}
		if err := s.scanBytes(loc, data, 0); err != nil {
			return err
		}
	}
	return nil
}

func trackedPathDeleted(ctx context.Context, root, path string) (bool, error) {
	output, err := runGitOutput(ctx, root, "ls-files", "--deleted", "-z", "--", path)
	if err != nil {
		return false, errors.New("verify deleted worktree path")
	}
	return len(output) > 0, nil
}

func containedPath(root, gitPath string) (string, error) {
	if strings.IndexByte(gitPath, 0) >= 0 {
		return "", errors.New("Git path contains NUL")
	}
	full := filepath.Join(root, filepath.FromSlash(gitPath))
	rel, err := filepath.Rel(root, full)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("Git path escaped repository root")
	}
	return full, nil
}

func listIndexBlobs(ctx context.Context, root string) ([]gitBlob, error) {
	output, err := runGitOutput(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, errors.New("enumerate Git index")
	}
	var blobs []gitBlob
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			return nil, errors.New("parse Git index record")
		}
		fields := bytes.Fields(record[:tab])
		if len(fields) != 3 {
			return nil, errors.New("parse Git index metadata")
		}
		oid := string(fields[1])
		if string(fields[0]) == "160000" {
			continue
		}
		if safeObjectID(oid) == "<invalid-object>" {
			return nil, errors.New("Git index contains an invalid object ID")
		}
		path := filepath.ToSlash(string(record[tab+1:]))
		blobs = append(blobs, gitBlob{oid: oid, loc: location{source: "index", path: path, objectID: oid}})
	}
	return blobs, nil
}

func listReleaseObjects(ctx context.Context, root string, maxObjects int) ([]gitBlob, error) {
	cmd := gitCommand(ctx, root, "rev-list", "--objects", "HEAD")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("open Git object inventory")
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, errors.New("start Git object inventory")
	}
	limited := &io.LimitedReader{R: stdout, N: maxGitMetadataBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4<<10), 1<<20)
	var blobs []gitBlob
	objects := 0
	for scanner.Scan() {
		objects++
		if objects > maxObjects {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, errors.New("Git object count limit exceeded")
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 1 {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, errors.New("parse Git object inventory")
		}
		if safeObjectID(fields[0]) == "<invalid-object>" {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, errors.New("Git object inventory contains an invalid object ID")
		}
		blobs = append(blobs, gitBlob{oid: fields[0], loc: location{source: "history", objectID: fields[0]}})
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("read Git object inventory")
	}
	if limited.N == 0 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("Git object inventory byte limit exceeded")
	}
	if err := cmd.Wait(); err != nil {
		return nil, errors.New("Git object inventory failed")
	}
	return blobs, nil
}

func (s *scanner) scanGitBlobs(root string, blobs []gitBlob) error {
	if len(blobs) == 0 {
		return nil
	}
	var input strings.Builder
	for _, blob := range blobs {
		input.WriteString(blob.oid)
		input.WriteByte('\n')
	}
	cmd := gitCommand(s.ctx, root, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(input.String())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("open Git blob reader")
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return errors.New("start Git blob reader")
	}
	reader := bufio.NewReaderSize(stdout, 64<<10)
	abort := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	for _, blob := range blobs {
		header, err := reader.ReadString('\n')
		if err != nil {
			abort()
			return errors.New("read Git blob header")
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || !strings.EqualFold(fields[0], blob.oid) {
			abort()
			return errors.New("Git blob reader returned unexpected metadata")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > s.limits.maxBlobBytes {
			abort()
			return fmt.Errorf("Git blob exceeds byte limit: %s", safeObjectID(blob.oid))
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			abort()
			return errors.New("read Git blob content")
		}
		separator, err := reader.ReadByte()
		if err != nil || separator != '\n' {
			abort()
			return errors.New("Git blob reader returned invalid framing")
		}
		if err := s.scanBytes(blob.loc, data, 0); err != nil {
			abort()
			return err
		}
	}
	if err := cmd.Wait(); err != nil {
		return errors.New("Git blob reader failed")
	}
	return nil
}

func runGitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := gitCommand(ctx, root, args...)
	stdout := boundedOutput{limit: maxGitMetadataBytes}
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if stdout.exceeded {
		return nil, errors.New("Git metadata output limit exceeded")
	}
	return stdout.buffer.Bytes(), nil
}

type boundedOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		write := len(p)
		if write > remaining {
			write = remaining
		}
		_, _ = w.buffer.Write(p[:write])
	}
	if len(p) > remaining {
		w.exceeded = true
	}
	return len(p), nil
}

func gitCommand(ctx context.Context, root string, args ...string) *exec.Cmd {
	gitArgs := []string{"--no-replace-objects", "-C", root}
	gitArgs = append(gitArgs, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	return cmd
}
