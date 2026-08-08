package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxEditableFileBytes = 2 << 20
	maxEditTextBytes     = 64 << 10
	maxReturnedDiffBytes = 64 << 10
)

var (
	ErrPermissionDenied = errors.New("workspace permission denied")
	ErrRevisionConflict = errors.New("file revision conflict")
	ErrEditNotUnique    = errors.New("edit target must match exactly once")
)

type fileEditParams struct {
	Path               string `json:"path"`
	OldText            string `json:"oldText"`
	NewText            string `json:"newText"`
	ExpectedFileSHA256 string `json:"expectedFileSha256"`
}

type fileEditResult struct {
	Path          string `json:"path"`
	BeforeSHA256  string `json:"beforeSha256"`
	AfterSHA256   string `json:"afterSha256"`
	Bytes         int64  `json:"bytes"`
	Diff          string `json:"diff"`
	DiffTruncated bool   `json:"diffTruncated"`
}

func (c *Client) fileEdit(ctx context.Context, workspaceID string, params map[string]any) (fileEditResult, error) {
	if err := ctx.Err(); err != nil {
		return fileEditResult{}, err
	}
	var input fileEditParams
	if err := decodeParams(params, &input); err != nil {
		return fileEditResult{}, fmt.Errorf("invalid params: %w", err)
	}
	if input.Path == "" || input.OldText == "" || input.ExpectedFileSHA256 == "" {
		return fileEditResult{}, fmt.Errorf("path, oldText and expectedFileSha256 are required")
	}
	if len(input.OldText) > maxEditTextBytes || len(input.NewText) > maxEditTextBytes {
		return fileEditResult{}, fmt.Errorf("edit text exceeds limit")
	}
	workspace, err := NewWorkspaceStore(c.cfg.DataDir).Resolve(workspaceID)
	if err != nil {
		return fileEditResult{}, err
	}
	if !workspace.Allows(WorkspacePermissionWrite) {
		return fileEditResult{}, ErrPermissionDenied
	}
	target, err := resolveWorkspacePath(workspace.Root, input.Path)
	if err != nil {
		return fileEditResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return fileEditResult{}, err
	}
	if !info.Mode().IsRegular() {
		return fileEditResult{}, ErrNotRegularFile
	}
	if info.Size() > maxEditableFileBytes {
		return fileEditResult{}, ErrReadLimit
	}
	before, err := os.ReadFile(target)
	if err != nil {
		return fileEditResult{}, err
	}
	if bytes.IndexByte(before, 0) >= 0 || !utf8.Valid(before) {
		return fileEditResult{}, ErrBinaryOrInvalidUTF8
	}
	beforeHash := sha256String(before)
	if input.ExpectedFileSHA256 != beforeHash {
		return fileEditResult{}, ErrRevisionConflict
	}
	if count := strings.Count(string(before), input.OldText); count != 1 {
		return fileEditResult{}, ErrEditNotUnique
	}
	after := []byte(strings.Replace(string(before), input.OldText, input.NewText, 1))
	if len(after) > maxEditableFileBytes {
		return fileEditResult{}, ErrReadLimit
	}
	if err := writeAtomicFile(target, after, info.Mode(), beforeHash); err != nil {
		return fileEditResult{}, err
	}
	diff, truncated := exactReplaceDiff(filepath.ToSlash(filepath.Clean(input.Path)), input.OldText, input.NewText)
	return fileEditResult{
		Path:          filepath.ToSlash(filepath.Clean(input.Path)),
		BeforeSHA256:  beforeHash,
		AfterSHA256:   sha256String(after),
		Bytes:         int64(len(after)),
		Diff:          diff,
		DiffTruncated: truncated,
	}, nil
}

func sha256String(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func exactReplaceDiff(path, oldText, newText string) (string, bool) {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
	var b strings.Builder
	b.WriteString("--- a/")
	b.WriteString(path)
	b.WriteString("\n+++ b/")
	b.WriteString(path)
	b.WriteString("\n@@ exact-replace @@\n")
	for _, line := range oldLines {
		b.WriteByte('-')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	for _, line := range newLines {
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	value := b.String()
	if len(value) <= maxReturnedDiffBytes {
		return value, false
	}
	return value[:maxReturnedDiffBytes], true
}
