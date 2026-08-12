package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var fileEditLocks [64]sync.Mutex

const (
	maxEditableFileBytes = 2 << 20
	maxEditTextBytes     = 64 << 10
	maxEditTotalBytes    = 512 << 10
	maxFileEdits         = 64
	maxReturnedDiffBytes = 16 << 10
)

var (
	ErrPermissionDenied  = errors.New("permission denied")
	ErrRevisionConflict  = errors.New("file revision conflict")
	ErrEditNotUnique     = errors.New("edit target must match exactly once")
	ErrEditOverlap       = errors.New("edit ranges overlap")
	ErrFileAlreadyExists = errors.New("file already exists")
)

type FileRevisionError struct {
	Path     string
	Expected string
	Actual   string
}

func (e *FileRevisionError) Error() string { return ErrRevisionConflict.Error() }
func (e *FileRevisionError) Unwrap() error { return ErrRevisionConflict }

type fileTextEdit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type fileEditParams struct {
	Path               string         `json:"path"`
	PreviewOf          string         `json:"previewOf,omitempty"`
	Content            string         `json:"content,omitempty"`
	OldText            string         `json:"oldText,omitempty"`
	NewText            string         `json:"newText,omitempty"`
	Edits              []fileTextEdit `json:"edits,omitempty"`
	ExpectedFileSHA256 string         `json:"expectedFileSha256,omitempty"`
	ExpectedAbsent     *bool          `json:"expectedAbsent,omitempty"`
}

type fileEditResult struct {
	Success       bool           `json:"success"`
	Changed       bool           `json:"changed"`
	Path          string         `json:"path"`
	Operation     string         `json:"operation"`
	Preview       bool           `json:"preview,omitempty"`
	EditsApplied  int            `json:"editsApplied"`
	OldSHA256     string         `json:"oldSha256,omitempty"`
	NewSHA256     string         `json:"newSha256"`
	BytesChanged  int64          `json:"bytesChanged"`
	LineDelta     int            `json:"lineDelta"`
	Timing        fileEditTiming `json:"timing"`
	Warnings      []string       `json:"warnings,omitempty"`
	Diff          string         `json:"diff,omitempty"`
	DiffTruncated bool           `json:"diffTruncated,omitempty"`
}

type fileEditTiming struct {
	TotalMs int64 `json:"totalMs"`
}

type plannedFileEdit struct {
	target    string
	action    string
	before    []byte
	after     []byte
	beforeSHA string
	mode      os.FileMode
	changes   []fileTextEdit
}

// fileEdit is the sole planner and writer for legacy edit and file_edit 2.0 actions.
func (c *Client) fileEdit(ctx context.Context, action string, params map[string]any) (fileEditResult, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return fileEditResult{}, err
	}
	var input fileEditParams
	if err := decodeParams(params, &input); err != nil {
		return fileEditResult{}, fmt.Errorf("invalid params: %w", err)
	}
	preview := action == "preview"
	requested := action
	if action == "edit" {
		requested = "replace"
	}
	if preview {
		requested = input.PreviewOf
		if requested == "edit" {
			requested = "replace"
		}
	}
	if requested != "create" && requested != "replace" && requested != "editMany" {
		return fileEditResult{}, fmt.Errorf("action must be create, replace, editMany, or preview with previewOf")
	}
	lockTarget, err := resolveFileEditLockTarget(input.Path, requested)
	if err != nil {
		return fileEditResult{}, err
	}
	lock := fileEditLock(lockTarget)
	lock.Lock()
	defer lock.Unlock()

	plan, err := planFileEdit(input, requested)
	if err != nil {
		return fileEditResult{}, err
	}
	changed := !bytes.Equal(plan.before, plan.after) || requested == "create"
	if changed && !preview {
		if requested == "create" {
			err = writeAtomicCreatedFile(plan.target, plan.after, 0o600)
		} else {
			err = writeAtomicEditedFile(plan.target, plan.after, plan.mode, plan.beforeSHA)
		}
		if err != nil {
			return fileEditResult{}, err
		}
	}
	operation := requested
	editsApplied := 0
	if changed {
		editsApplied = len(plan.changes)
	}
	result := fileEditResult{
		Success: true, Changed: changed, Path: filepath.Clean(plan.target), Operation: operation,
		EditsApplied: editsApplied, OldSHA256: plan.beforeSHA, NewSHA256: sha256String(plan.after),
		BytesChanged: changedByteCount(plan.changes, changed), LineDelta: logicalLineCount(plan.after) - logicalLineCount(plan.before),
	}
	if preview {
		result.Preview = true
		result.Diff, result.DiffTruncated = boundedEditDiff(filepath.Base(plan.target), requested, plan.changes)
	}
	result.Timing.TotalMs = time.Since(started).Milliseconds()
	return result, nil
}

func resolveFileEditLockTarget(path, action string) (string, error) {
	clean := normalizeMachinePathInput(strings.TrimSpace(path))
	if clean == "" || strings.IndexByte(clean, 0) >= 0 || !filepath.IsAbs(clean) {
		return "", ErrAbsolutePathRequired
	}
	clean = filepath.Clean(clean)
	if action != "create" {
		return ResolveMachinePath(clean)
	}
	parent, err := ResolveMachinePath(filepath.Dir(clean))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(clean)), nil
}

func fileEditLock(path string) *sync.Mutex {
	key := filepath.Clean(normalizeMachinePathInput(strings.TrimSpace(path)))
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(key))
	return &fileEditLocks[hasher.Sum32()%uint32(len(fileEditLocks))]
}

func changedByteCount(edits []fileTextEdit, changed bool) int64 {
	if !changed {
		return 0
	}
	var total int64
	for _, edit := range edits {
		total += int64(len(edit.OldText) + len(edit.NewText))
	}
	return total
}

func newlineCount(value []byte) int { return bytes.Count(value, []byte{'\n'}) }

func logicalLineCount(value []byte) int {
	if len(value) == 0 {
		return 0
	}
	count := newlineCount(value)
	if value[len(value)-1] != '\n' {
		count++
	}
	return count
}

func planFileEdit(input fileEditParams, action string) (plannedFileEdit, error) {
	if strings.TrimSpace(input.Path) == "" {
		return plannedFileEdit{}, fmt.Errorf("path is required")
	}
	if action == "create" {
		return planFileCreate(input)
	}
	if input.ExpectedFileSHA256 == "" {
		return plannedFileEdit{}, fmt.Errorf("expectedFileSha256 is required")
	}
	target, err := ResolveMachinePath(input.Path)
	if err != nil {
		return plannedFileEdit{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return plannedFileEdit{}, err
	}
	if !info.Mode().IsRegular() {
		return plannedFileEdit{}, ErrNotRegularFile
	}
	if info.Size() > maxEditableFileBytes {
		return plannedFileEdit{}, ErrReadLimit
	}
	before, err := os.ReadFile(target)
	if err != nil {
		return plannedFileEdit{}, err
	}
	if !validEditableText(before) {
		return plannedFileEdit{}, ErrBinaryOrInvalidUTF8
	}
	beforeSHA := sha256String(before)
	if input.ExpectedFileSHA256 != beforeSHA {
		return plannedFileEdit{}, &FileRevisionError{Path: filepath.Clean(target), Expected: input.ExpectedFileSHA256, Actual: beforeSHA}
	}
	edits := input.Edits
	if action == "replace" {
		if len(input.Edits) != 0 || input.OldText == "" {
			return plannedFileEdit{}, fmt.Errorf("oldText is required and edits must be empty")
		}
		edits = []fileTextEdit{{OldText: input.OldText, NewText: input.NewText}}
	} else if len(edits) == 0 || input.OldText != "" || input.NewText != "" {
		return plannedFileEdit{}, fmt.Errorf("editMany requires edits only")
	}
	if len(edits) > maxFileEdits {
		return plannedFileEdit{}, fmt.Errorf("too many edits")
	}
	style := dominantNewline(before)
	type editRange struct {
		start, end int
		newText    []byte
		change     fileTextEdit
	}
	ranges := make([]editRange, 0, len(edits))
	total := 0
	for _, edit := range edits {
		if edit.OldText == "" || len(edit.OldText) > maxEditTextBytes || len(edit.NewText) > maxEditTextBytes {
			return plannedFileEdit{}, fmt.Errorf("edit text is empty or exceeds limit")
		}
		total += len(edit.OldText) + len(edit.NewText)
		if total > maxEditTotalBytes || !utf8.ValidString(edit.OldText) || !utf8.ValidString(edit.NewText) || strings.IndexByte(edit.OldText, 0) >= 0 || strings.IndexByte(edit.NewText, 0) >= 0 {
			return plannedFileEdit{}, fmt.Errorf("edit text exceeds limit or is not text")
		}
		oldText := normalizeNewlines(edit.OldText, style)
		newText := normalizeNewlines(edit.NewText, style)
		oldBytes := []byte(oldText)
		if bytes.Count(before, oldBytes) != 1 {
			return plannedFileEdit{}, ErrEditNotUnique
		}
		start := bytes.Index(before, oldBytes)
		ranges = append(ranges, editRange{start: start, end: start + len(oldBytes), newText: []byte(newText), change: fileTextEdit{OldText: oldText, NewText: newText}})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	for i := 1; i < len(ranges); i++ {
		if ranges[i].start < ranges[i-1].end {
			return plannedFileEdit{}, ErrEditOverlap
		}
	}
	after := make([]byte, 0, len(before))
	changes := make([]fileTextEdit, 0, len(ranges))
	cursor := 0
	for _, edit := range ranges {
		after = append(after, before[cursor:edit.start]...)
		after = append(after, edit.newText...)
		cursor = edit.end
		changes = append(changes, edit.change)
	}
	after = append(after, before[cursor:]...)
	if len(after) > maxEditableFileBytes {
		return plannedFileEdit{}, ErrReadLimit
	}
	return plannedFileEdit{target: target, action: action, before: before, after: after, beforeSHA: beforeSHA, mode: info.Mode(), changes: changes}, nil
}

func planFileCreate(input fileEditParams) (plannedFileEdit, error) {
	if input.ExpectedAbsent == nil || !*input.ExpectedAbsent {
		return plannedFileEdit{}, fmt.Errorf("expectedAbsent=true is required")
	}
	if input.ExpectedFileSHA256 != "" || input.OldText != "" || input.NewText != "" || len(input.Edits) != 0 {
		return plannedFileEdit{}, fmt.Errorf("create accepts only content and expectedAbsent")
	}
	data := []byte(input.Content)
	if len(data) > maxEditableFileBytes {
		return plannedFileEdit{}, ErrReadLimit
	}
	if !validEditableText(data) {
		return plannedFileEdit{}, ErrBinaryOrInvalidUTF8
	}
	clean := normalizeMachinePathInput(strings.TrimSpace(input.Path))
	if clean == "" || strings.IndexByte(clean, 0) >= 0 || !filepath.IsAbs(clean) {
		return plannedFileEdit{}, ErrAbsolutePathRequired
	}
	parent, err := ResolveMachinePath(filepath.Dir(filepath.Clean(clean)))
	if err != nil {
		return plannedFileEdit{}, err
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		if err != nil {
			return plannedFileEdit{}, err
		}
		return plannedFileEdit{}, ErrNotRegularFile
	}
	target := filepath.Join(parent, filepath.Base(clean))
	if _, err := os.Lstat(target); err == nil {
		return plannedFileEdit{}, ErrFileAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return plannedFileEdit{}, err
	}
	return plannedFileEdit{target: target, action: "create", after: data, changes: []fileTextEdit{{NewText: input.Content}}}, nil
}

func validEditableText(data []byte) bool {
	return bytes.IndexByte(data, 0) < 0 && utf8.Valid(data)
}

func dominantNewline(data []byte) string {
	crlf := bytes.Count(data, []byte("\r\n"))
	lf := bytes.Count(data, []byte("\n")) - crlf
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}

func normalizeNewlines(value, newline string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	if newline == "\r\n" {
		value = strings.ReplaceAll(value, "\n", "\r\n")
	}
	return value
}

func sha256String(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func boundedEditDiff(path, action string, edits []fileTextEdit) (string, bool) {
	var b strings.Builder
	appendDiff := func(value string) bool {
		remaining := maxReturnedDiffBytes - b.Len()
		if remaining <= 0 {
			return false
		}
		if len(value) > remaining {
			value = value[:remaining]
			for !utf8.ValidString(value) {
				value = value[:len(value)-1]
			}
			b.WriteString(value)
			return false
		}
		b.WriteString(value)
		return true
	}
	if !appendDiff("--- a/" + path + "\n+++ b/" + path + "\n") {
		return b.String(), true
	}
	for _, edit := range edits {
		if !appendDiff("@@ " + action + " @@\n") {
			return b.String(), true
		}
		if edit.OldText != "" && !appendDiffLines("-", edit.OldText, appendDiff) {
			return b.String(), true
		}
		if edit.NewText != "" && !appendDiffLines("+", edit.NewText, appendDiff) {
			return b.String(), true
		}
	}
	return b.String(), false
}

func appendDiffLines(prefix, value string, appendDiff func(string) bool) bool {
	for len(value) > 0 {
		line := value
		if index := strings.IndexByte(value, '\n'); index >= 0 {
			line, value = value[:index], value[index+1:]
		} else {
			value = ""
		}
		if !appendDiff(prefix + strings.TrimSuffix(line, "\r") + "\n") {
			return false
		}
	}
	return true
}
