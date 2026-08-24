// Package secretscan implements the repository secret gate used by release tooling.
// Findings contain locations and rule identifiers only; there is no field for the
// matched bytes, and diagnostic formatting redacts sensitive locator text.
package secretscan

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultMaxBlobBytes       = int64(64 << 20)
	defaultMaxTotalBytes      = int64(4 << 30)
	defaultMaxFiles           = 200_000
	defaultMaxFindings        = 10_000
	defaultMaxZIPEntries      = 2_000
	defaultMaxZIPExpanded     = int64(128 << 20)
	defaultMaxZIPEntryBytes   = int64(32 << 20)
	defaultMaxZIPNestingDepth = 2
)

// Finding deliberately has no matched-value field.
type Finding struct {
	Source   string
	Path     string
	ObjectID string
	Line     int
	Rule     string
}

// Options controls scanner resource bounds and private marker input. Zero values
// select conservative defaults. Limits are fail-closed rather than skip limits.
type Options struct {
	MarkerFile         string
	MaxBlobBytes       int64
	MaxTotalBytes      int64
	MaxFiles           int
	MaxFindings        int
	MaxZIPEntries      int
	MaxZIPExpanded     int64
	MaxZIPEntryBytes   int64
	MaxZIPNestingDepth int
}

type limits struct {
	maxBlobBytes       int64
	maxTotalBytes      int64
	maxFiles           int
	maxFindings        int
	maxZIPEntries      int
	maxZIPExpanded     int64
	maxZIPEntryBytes   int64
	maxZIPNestingDepth int
}

type scanner struct {
	ctx        context.Context
	limits     limits
	markers    [][]byte
	findings   []Finding
	seen       map[string]struct{}
	totalBytes int64
	files      int
	matches    int
}

func normalizedLimits(options Options) (limits, error) {
	l := limits{
		maxBlobBytes:       options.MaxBlobBytes,
		maxTotalBytes:      options.MaxTotalBytes,
		maxFiles:           options.MaxFiles,
		maxFindings:        options.MaxFindings,
		maxZIPEntries:      options.MaxZIPEntries,
		maxZIPExpanded:     options.MaxZIPExpanded,
		maxZIPEntryBytes:   options.MaxZIPEntryBytes,
		maxZIPNestingDepth: options.MaxZIPNestingDepth,
	}
	if l.maxBlobBytes == 0 {
		l.maxBlobBytes = defaultMaxBlobBytes
	}
	if l.maxTotalBytes == 0 {
		l.maxTotalBytes = defaultMaxTotalBytes
	}
	if l.maxFiles == 0 {
		l.maxFiles = defaultMaxFiles
	}
	if l.maxFindings == 0 {
		l.maxFindings = defaultMaxFindings
	}
	if l.maxZIPEntries == 0 {
		l.maxZIPEntries = defaultMaxZIPEntries
	}
	if l.maxZIPExpanded == 0 {
		l.maxZIPExpanded = defaultMaxZIPExpanded
	}
	if l.maxZIPEntryBytes == 0 {
		l.maxZIPEntryBytes = defaultMaxZIPEntryBytes
	}
	if l.maxZIPNestingDepth == 0 {
		l.maxZIPNestingDepth = defaultMaxZIPNestingDepth
	}
	if l.maxBlobBytes < 1 || l.maxTotalBytes < 1 || l.maxFiles < 1 || l.maxFindings < 1 ||
		l.maxZIPEntries < 1 || l.maxZIPExpanded < 1 || l.maxZIPEntryBytes < 1 || l.maxZIPNestingDepth < 1 {
		return limits{}, errors.New("all scanner limits must be positive")
	}
	return l, nil
}

func newScanner(ctx context.Context, options Options) (*scanner, error) {
	l, err := normalizedLimits(options)
	if err != nil {
		return nil, err
	}
	markers, err := readMarkers(options.MarkerFile, l.maxBlobBytes)
	if err != nil {
		return nil, err
	}
	return &scanner{ctx: ctx, limits: l, markers: markers, seen: make(map[string]struct{})}, nil
}

// ScanTree scans the exact filesystem tree rooted at root, including hidden and
// ignored files. Symlinks are not followed; their link text is scanned instead.
func ScanTree(ctx context.Context, root string, options Options) ([]Finding, error) {
	s, err := newScanner(ctx, options)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, errors.New("resolve tree root")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, errors.New("inspect tree root")
	}
	if !info.IsDir() {
		return nil, errors.New("tree root must be a directory")
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("walk tree")
		}
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if path == root || entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("tree entry escaped root")
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read symlink %s", s.safeLocator(rel))
			}
			return s.scanBytes(location{source: "tree", path: rel}, []byte(target), 0)
		}
		// Use Lstat instead of the cached DirEntry.Info result. On provider-
		// mounted Windows workspaces, the cached FileInfo can fail os.SameFile
		// against the handle opened below even when the file did not change.
		entryInfo, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect tree entry %s", s.safeLocator(rel))
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("unsupported tree entry %s", s.safeLocator(rel))
		}
		data, err := readRegularFile(path, entryInfo, s.limits.maxBlobBytes)
		if err != nil {
			return fmt.Errorf("read tree entry %s", s.safeLocator(rel))
		}
		return s.scanBytes(location{source: "tree", path: rel}, data, 0)
	})
	if err != nil {
		return nil, err
	}
	return s.results(), nil
}

func readRegularFile(path string, before fs.FileInfo, maxBytes int64) ([]byte, error) {
	if !before.Mode().IsRegular() || before.Size() > maxBytes {
		return nil, errors.New("file is not regular or exceeds the byte limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	afterOpen, err := f.Stat()
	if err != nil || !afterOpen.Mode().IsRegular() || !os.SameFile(before, afterOpen) || afterOpen.Size() > maxBytes {
		return nil, errors.New("file changed during validation")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, errors.New("file read failed or exceeded the byte limit")
	}
	afterRead, err := f.Stat()
	if err != nil || afterRead.Size() != int64(len(data)) || !os.SameFile(afterOpen, afterRead) {
		return nil, errors.New("file changed while being read")
	}
	return data, nil
}

type location struct {
	source   string
	path     string
	objectID string
}

func (s *scanner) scanBytes(loc location, data []byte, zipDepth int) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if int64(len(data)) > s.limits.maxBlobBytes {
		return fmt.Errorf("input %s exceeds blob byte limit", s.safeLocation(loc))
	}
	s.files++
	if s.files > s.limits.maxFiles {
		return errors.New("scanner file limit exceeded")
	}
	s.totalBytes += int64(len(data))
	if s.totalBytes > s.limits.maxTotalBytes {
		return errors.New("scanner total byte limit exceeded")
	}

	if loc.path != "" {
		if err := s.scanContent(loc, []byte(loc.path)); err != nil {
			return err
		}
	}
	if rule := sensitiveFilenameRule(loc.path); rule != "" {
		if err := s.add(loc, 1, rule); err != nil {
			return err
		}
	}
	if err := s.scanContent(loc, data); err != nil {
		return err
	}
	if looksLikeZIP(loc.path, data) {
		if zipDepth >= s.limits.maxZIPNestingDepth {
			return fmt.Errorf("ZIP nesting limit exceeded at %s", s.safeLocation(loc))
		}
		if err := s.scanZIP(loc, data, zipDepth+1); err != nil {
			return err
		}
	}
	return nil
}

func (s *scanner) scanZIP(loc location, data []byte, depth int) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("invalid ZIP at %s", s.safeLocation(loc))
	}
	if len(zr.File) > s.limits.maxZIPEntries {
		return fmt.Errorf("ZIP entry limit exceeded at %s", s.safeLocation(loc))
	}
	var expanded int64
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		entryLoc := loc
		entryLoc.path = loc.path + "!" + filepath.ToSlash(entry.Name)
		if entry.UncompressedSize64 > uint64(s.limits.maxZIPEntryBytes) {
			return fmt.Errorf("ZIP entry byte limit exceeded at %s", s.safeLocation(entryLoc))
		}
		reader, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open ZIP entry at %s", s.safeLocation(entryLoc))
		}
		entryData, readErr := io.ReadAll(io.LimitReader(reader, s.limits.maxZIPEntryBytes+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || int64(len(entryData)) > s.limits.maxZIPEntryBytes {
			return fmt.Errorf("read ZIP entry at %s", s.safeLocation(entryLoc))
		}
		expanded += int64(len(entryData))
		if expanded > s.limits.maxZIPExpanded {
			return fmt.Errorf("ZIP expanded byte limit exceeded at %s", s.safeLocation(entryLoc))
		}
		if err := s.scanBytes(entryLoc, entryData, depth); err != nil {
			return err
		}
	}
	return nil
}

func looksLikeZIP(path string, data []byte) bool {
	if strings.EqualFold(filepath.Ext(path), ".zip") {
		return true
	}
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' &&
		((data[2] == 3 && data[3] == 4) || (data[2] == 5 && data[3] == 6) || (data[2] == 7 && data[3] == 8))
}

func (s *scanner) add(loc location, line int, rule string) error {
	if line < 1 {
		line = 1
	}
	finding := Finding{
		Source:   loc.source,
		Path:     s.safeLocator(loc.path),
		ObjectID: loc.objectID,
		Line:     line,
		Rule:     rule,
	}
	pathHash := sha256.Sum256([]byte(loc.path))
	key := finding.Source + "\x00" + hex.EncodeToString(pathHash[:]) + "\x00" + finding.ObjectID + "\x00" + fmt.Sprint(finding.Line) + "\x00" + finding.Rule
	if _, exists := s.seen[key]; exists {
		return nil
	}
	if len(s.findings) >= s.limits.maxFindings {
		return errors.New("scanner finding limit exceeded")
	}
	s.seen[key] = struct{}{}
	s.findings = append(s.findings, finding)
	return nil
}

func (s *scanner) consumeMatch() error {
	if s.matches >= s.limits.maxFindings {
		return errors.New("scanner finding limit exceeded")
	}
	s.matches++
	return nil
}

func (s *scanner) boundedFindAllIndex(pattern interface {
	FindAllIndex([]byte, int) [][]int
}, data []byte) ([][]int, error) {
	remaining := s.limits.maxFindings - s.matches
	matches := pattern.FindAllIndex(data, remaining+1)
	if len(matches) > remaining {
		return nil, errors.New("scanner finding limit exceeded")
	}
	s.matches += len(matches)
	return matches, nil
}

func (s *scanner) boundedFindAllSubmatchIndex(pattern interface {
	FindAllSubmatchIndex([]byte, int) [][]int
}, data []byte) ([][]int, error) {
	remaining := s.limits.maxFindings - s.matches
	matches := pattern.FindAllSubmatchIndex(data, remaining+1)
	if len(matches) > remaining {
		return nil, errors.New("scanner finding limit exceeded")
	}
	s.matches += len(matches)
	return matches, nil
}

func (s *scanner) results() []Finding {
	sort.SliceStable(s.findings, func(i, j int) bool {
		a, b := s.findings[i], s.findings[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.ObjectID != b.ObjectID {
			return a.ObjectID < b.ObjectID
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Rule < b.Rule
	})
	return append([]Finding(nil), s.findings...)
}

func readMarkers(path string, maxBytes int64) ([][]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("private marker file is unreadable or exceeds the byte limit")
	}
	data, err := readRegularFile(path, info, maxBytes)
	if err != nil {
		return nil, errors.New("read private marker file")
	}
	var markers [][]byte
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(bytes.TrimSuffix(line, []byte{'\r'}))
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		if len(line) < 4 || len(line) > 4_096 {
			return nil, errors.New("private marker length is outside the allowed range")
		}
		markers = append(markers, append([]byte(nil), line...))
		if len(markers) > 1_000 {
			return nil, errors.New("private marker count limit exceeded")
		}
	}
	return markers, nil
}
