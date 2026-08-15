package opsbackup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	FormatV1           = "fast-spider-hub-backup/v1"
	manifestName       = "manifest.json"
	archivePrefix      = "data/"
	maxBackupFiles     = 10_000
	maxBackupBytes     = int64(8 << 30)
	maxManifestBytes   = int64(8 << 20)
	maxBackupPathBytes = 4096
)

type Manifest struct {
	Format            string      `json:"format"`
	CreatedAt         string      `json:"createdAt"`
	FastSpiderVersion string      `json:"fastSpiderVersion"`
	Files             []FileEntry `json:"files"`
}

type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

type sourceFile struct {
	absolute string
	relative string
	mode     os.FileMode
}

func Create(ctx context.Context, dataDir, outputPath, appVersion string) (Manifest, error) {
	root, err := realDirectory(dataDir)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve data directory: %w", err)
	}
	output, err := resolveTargetPath(outputPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve backup output: %w", err)
	}
	if pathWithin(root, output) {
		return Manifest{}, fmt.Errorf("backup output must be outside the Hub data directory")
	}
	unlockDirectory := lockReleaseBackupDirectory(filepath.Dir(output))
	defer unlockDirectory()
	if _, err := os.Lstat(output); err == nil {
		return Manifest{}, fmt.Errorf("backup output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("stat backup output: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create backup output directory: %w", err)
	}

	files, err := collectFiles(ctx, root)
	if err != nil {
		return Manifest{}, err
	}
	if len(files) == 0 {
		return Manifest{}, fmt.Errorf("Hub data directory is empty")
	}

	tmp, err := os.CreateTemp(filepath.Dir(output), ".fast-spider-backup-*.tmp")
	if err != nil {
		return Manifest{}, fmt.Errorf("create backup temp file: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Chmod(0o600)
	published := false
	defer func() {
		_ = tmp.Close()
		if !published {
			_ = os.Remove(tmpPath)
		}
	}()

	zw := zip.NewWriter(tmp)
	manifest := Manifest{
		Format:            FormatV1,
		CreatedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		FastSpiderVersion: strings.TrimSpace(appVersion),
		Files:             make([]FileEntry, 0, len(files)),
	}
	var archivedBytes int64
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			_ = zw.Close()
			return Manifest{}, err
		}
		entry, err := addFile(ctx, zw, file, maxBackupBytes-archivedBytes)
		if err != nil {
			_ = zw.Close()
			return Manifest{}, err
		}
		archivedBytes += entry.Size
		manifest.Files = append(manifest.Files, entry)
	}

	// A second pass makes a live/idle backup practical without pretending it is
	// atomic: any DB/WAL/artifact change during the copy invalidates the backup.
	if err := verifySourceUnchanged(ctx, root, manifest.Files); err != nil {
		_ = zw.Close()
		return Manifest{}, err
	}
	if err := writeManifest(zw, manifest); err != nil {
		_ = zw.Close()
		return Manifest{}, err
	}
	if err := zw.Close(); err != nil {
		return Manifest{}, fmt.Errorf("finalize backup archive: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return Manifest{}, fmt.Errorf("sync backup archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close backup archive: %w", err)
	}
	if err := os.Rename(tmpPath, output); err != nil {
		return Manifest{}, fmt.Errorf("publish backup archive: %w", err)
	}
	published = true
	return manifest, nil
}

func Verify(ctx context.Context, backupPath string) (Manifest, error) {
	zr, err := zip.OpenReader(backupPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open backup archive: %w", err)
	}
	defer zr.Close()
	return verifyArchive(ctx, &zr.Reader)
}

func Restore(ctx context.Context, backupPath, dataDir string) (Manifest, error) {
	zr, err := zip.OpenReader(backupPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open backup archive: %w", err)
	}
	defer zr.Close()
	manifest, err := verifyArchive(ctx, &zr.Reader)
	if err != nil {
		return Manifest{}, err
	}
	target, err := resolveTargetPath(dataDir)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve restore target: %w", err)
	}
	if err := requireEmptyTarget(target); err != nil {
		return Manifest{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create restore parent: %w", err)
	}

	tmpDir, err := os.MkdirTemp(filepath.Dir(target), ".fast-spider-restore-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create restore temp directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	files := make(map[string]*zip.File, len(zr.File))
	for _, file := range zr.File {
		files[file.Name] = file
	}
	for _, entry := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		file := files[archivePrefix+entry.Path]
		if file == nil {
			return Manifest{}, fmt.Errorf("backup entry is missing: %s", entry.Path)
		}
		if err := extractFile(ctx, tmpDir, file, entry); err != nil {
			return Manifest{}, err
		}
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		if err := os.Remove(target); err != nil {
			return Manifest{}, fmt.Errorf("remove empty restore target: %w", err)
		}
	}
	if err := os.Rename(tmpDir, target); err != nil {
		return Manifest{}, fmt.Errorf("publish restored data directory: %w", err)
	}
	published = true
	return manifest, nil
}

func collectFiles(ctx context.Context, root string) ([]sourceFile, error) {
	var files []sourceFile
	var totalBytes int64
	portablePaths := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("Hub data directory contains an unsupported file: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel, err = archivePathFromLocal(rel)
		if err != nil {
			return err
		}
		// SQLite shared-memory indexes are transient and are rebuilt from the DB/WAL.
		if rel == "hub.db-shm" {
			return nil
		}
		if err := addPortablePath(portablePaths, rel); err != nil {
			return err
		}
		if len(files) >= maxBackupFiles {
			return fmt.Errorf("Hub data directory contains more than %d backup files", maxBackupFiles)
		}
		if info.Size() < 0 || totalBytes > maxBackupBytes-info.Size() {
			return fmt.Errorf("Hub data directory exceeds the %d byte backup safety limit", maxBackupBytes)
		}
		totalBytes += info.Size()
		files = append(files, sourceFile{absolute: path, relative: rel, mode: info.Mode().Perm()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan Hub data directory: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].relative < files[j].relative })
	return files, nil
}

func addFile(ctx context.Context, zw *zip.Writer, file sourceFile, remainingBytes int64) (FileEntry, error) {
	input, err := os.Open(file.absolute)
	if err != nil {
		return FileEntry{}, fmt.Errorf("open backup source %s: %w", file.relative, err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err != nil {
			return FileEntry{}, fmt.Errorf("stat backup source %s: %w", file.relative, err)
		}
		return FileEntry{}, fmt.Errorf("backup source changed type: %s", file.relative)
	}
	header := &zip.FileHeader{Name: archivePrefix + file.relative, Method: zip.Store}
	header.SetMode(file.mode)
	header.SetModTime(info.ModTime())
	output, err := zw.CreateHeader(header)
	if err != nil {
		return FileEntry{}, fmt.Errorf("create backup entry %s: %w", file.relative, err)
	}
	hash := sha256.New()
	written, err := copyWithContextLimit(ctx, io.MultiWriter(output, hash), input, remainingBytes)
	if err != nil {
		return FileEntry{}, fmt.Errorf("copy backup source %s: %w", file.relative, err)
	}
	return FileEntry{Path: file.relative, Size: written, SHA256: hex.EncodeToString(hash.Sum(nil)), Mode: fmt.Sprintf("%04o", file.mode)}, nil
}

func writeManifest(zw *zip.Writer, manifest Manifest) error {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup manifest: %w", err)
	}
	if int64(len(raw)) > maxManifestBytes {
		return fmt.Errorf("backup manifest exceeds the %d byte safety limit", maxManifestBytes)
	}
	header := &zip.FileHeader{Name: manifestName, Method: zip.Store}
	header.SetMode(0o600)
	header.SetModTime(time.Now().UTC())
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create backup manifest entry: %w", err)
	}
	if _, err := writer.Write(raw); err != nil {
		return fmt.Errorf("write backup manifest: %w", err)
	}
	return nil
}

func verifySourceUnchanged(ctx context.Context, root string, expected []FileEntry) error {
	current, err := collectFiles(ctx, root)
	if err != nil {
		return err
	}
	if len(current) != len(expected) {
		return changedError("")
	}
	for i, file := range current {
		entry := expected[i]
		if file.relative != entry.Path {
			return changedError("")
		}
		hash, size, err := hashFile(ctx, file.absolute)
		if err != nil {
			return fmt.Errorf("verify backup source %s: %w", entry.Path, err)
		}
		if size != entry.Size || hash != entry.SHA256 {
			return changedError(entry.Path)
		}
	}
	return nil
}

func changedError(path string) error {
	if path == "" {
		return fmt.Errorf("Hub data changed during backup; retry when the Hub is idle or stopped")
	}
	return fmt.Errorf("Hub data changed during backup at %s; retry when the Hub is idle or stopped", path)
}

func verifyArchive(ctx context.Context, zr *zip.Reader) (Manifest, error) {
	if len(zr.File) > maxBackupFiles+1 {
		return Manifest{}, fmt.Errorf("backup contains too many entries")
	}
	var manifest Manifest
	manifestSeen := false
	data := make(map[string]*zip.File)
	portableDataPaths := make(map[string]string)
	var totalBytes int64
	for _, file := range zr.File {
		switch {
		case file.Name == manifestName:
			if manifestSeen {
				return Manifest{}, fmt.Errorf("backup contains duplicate manifest")
			}
			if file.UncompressedSize64 > uint64(maxManifestBytes) {
				return Manifest{}, fmt.Errorf("backup manifest is too large")
			}
			manifestSeen = true
			rc, err := file.Open()
			if err != nil {
				return Manifest{}, err
			}
			decoder := json.NewDecoder(io.LimitReader(rc, maxManifestBytes+1))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(&manifest)
			if err == nil {
				var extra any
				if extraErr := decoder.Decode(&extra); !errors.Is(extraErr, io.EOF) {
					err = fmt.Errorf("backup manifest must contain one JSON object")
				}
			}
			_ = rc.Close()
			if err != nil {
				return Manifest{}, fmt.Errorf("decode backup manifest: %w", err)
			}
		case strings.HasPrefix(file.Name, archivePrefix):
			rawRel := strings.TrimPrefix(file.Name, archivePrefix)
			rel, err := validateArchivePath(rawRel)
			if err != nil || rel != rawRel || file.FileInfo().IsDir() || file.Mode()&os.ModeSymlink != 0 {
				return Manifest{}, fmt.Errorf("backup contains unsafe or non-canonical entry %q", file.Name)
			}
			if file.UncompressedSize64 > uint64(maxBackupBytes) || totalBytes > maxBackupBytes-int64(file.UncompressedSize64) {
				return Manifest{}, fmt.Errorf("backup exceeds the %d byte restore safety limit", maxBackupBytes)
			}
			totalBytes += int64(file.UncompressedSize64)
			if _, exists := data[rel]; exists {
				return Manifest{}, fmt.Errorf("backup contains duplicate entry %q", rel)
			}
			if err := addPortablePath(portableDataPaths, rel); err != nil {
				return Manifest{}, err
			}
			data[rel] = file
		default:
			return Manifest{}, fmt.Errorf("backup contains unexpected entry %q", file.Name)
		}
	}
	if !manifestSeen || manifest.Format != FormatV1 || len(manifest.Files) == 0 {
		return Manifest{}, fmt.Errorf("backup manifest is missing or unsupported")
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		return Manifest{}, fmt.Errorf("invalid backup creation time: %w", err)
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	portableManifestPaths := make(map[string]string, len(manifest.Files))
	hasDatabase := false
	for _, entry := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		rel, err := validateArchivePath(entry.Path)
		if err != nil || rel != entry.Path {
			return Manifest{}, fmt.Errorf("invalid backup manifest path %q", entry.Path)
		}
		if _, exists := seen[entry.Path]; exists {
			return Manifest{}, fmt.Errorf("duplicate backup manifest path %q", entry.Path)
		}
		if err := addPortablePath(portableManifestPaths, entry.Path); err != nil {
			return Manifest{}, err
		}
		seen[entry.Path] = struct{}{}
		if entry.Size < 0 {
			return Manifest{}, fmt.Errorf("invalid size for %s", entry.Path)
		}
		if len(entry.SHA256) != 64 {
			return Manifest{}, fmt.Errorf("invalid SHA-256 for %s", entry.Path)
		}
		if _, err := hex.DecodeString(entry.SHA256); err != nil {
			return Manifest{}, fmt.Errorf("invalid SHA-256 for %s", entry.Path)
		}
		if _, err := parseMode(entry.Mode); err != nil {
			return Manifest{}, fmt.Errorf("invalid mode for %s: %w", entry.Path, err)
		}
		if entry.Path == "hub.db" {
			hasDatabase = true
		}
		file := data[entry.Path]
		if file == nil {
			return Manifest{}, fmt.Errorf("backup data entry is missing: %s", entry.Path)
		}
		if file.UncompressedSize64 != uint64(entry.Size) {
			return Manifest{}, fmt.Errorf("backup size metadata does not match ZIP entry: %s", entry.Path)
		}
		rc, err := file.Open()
		if err != nil {
			return Manifest{}, err
		}
		hash, size, err := hashReader(ctx, rc)
		_ = rc.Close()
		if err != nil || size != entry.Size || hash != entry.SHA256 {
			return Manifest{}, fmt.Errorf("backup integrity verification failed: %s", entry.Path)
		}
	}
	if !hasDatabase {
		return Manifest{}, fmt.Errorf("backup does not contain hub.db")
	}
	if len(data) != len(manifest.Files) {
		return Manifest{}, fmt.Errorf("backup contains data not listed in the manifest")
	}
	return manifest, nil
}

func extractFile(ctx context.Context, root string, file *zip.File, entry FileEntry) error {
	rel, err := validateArchivePath(entry.Path)
	if err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	if !pathWithin(root, target) {
		return fmt.Errorf("restore path escapes target: %s", entry.Path)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	mode, err := parseMode(entry.Mode)
	if err != nil {
		return fmt.Errorf("invalid mode for %s: %w", entry.Path, err)
	}
	input, err := file.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := copyWithContext(ctx, io.MultiWriter(output, hash), input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return fmt.Errorf("restored file failed integrity verification: %s", entry.Path)
	}
	return nil
}

func requireEmptyTarget(target string) error {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("restore target must not be a symlink or junction")
	}
	if !info.IsDir() {
		return fmt.Errorf("restore target exists and is not a directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("restore target must be empty")
	}
	return nil
}

func hashFile(ctx context.Context, path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	return hashReader(ctx, file)
}

func hashReader(ctx context.Context, reader io.Reader) (string, int64, error) {
	hash := sha256.New()
	written, err := copyWithContext(ctx, hash, reader)
	if err != nil {
		return "", written, err
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	return copyWithContextLimit(ctx, dst, src, -1)
}

func copyWithContextLimit(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if limit >= 0 && total >= limit {
			var probe [1]byte
			for {
				if err := ctx.Err(); err != nil {
					return total, err
				}
				n, readErr := src.Read(probe[:])
				if n > 0 {
					return total, fmt.Errorf("backup data exceeds the %d byte safety limit", maxBackupBytes)
				}
				if errors.Is(readErr, io.EOF) {
					return total, nil
				}
				if readErr != nil {
					return total, readErr
				}
			}
		}
		readBuffer := buffer
		if limit >= 0 {
			remaining := limit - total
			if remaining < int64(len(readBuffer)) {
				readBuffer = readBuffer[:remaining]
			}
		}
		n, readErr := src.Read(readBuffer)
		if n > 0 {
			written, writeErr := dst.Write(readBuffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func parseMode(raw string) (os.FileMode, error) {
	if len(raw) != 4 {
		return 0, fmt.Errorf("mode must contain four octal digits")
	}
	value, err := strconv.ParseUint(raw, 8, 32)
	if err != nil || value > 0o777 {
		return 0, fmt.Errorf("mode must be between 0000 and 0777")
	}
	return os.FileMode(value), nil
}

func archivePathFromLocal(relative string) (string, error) {
	return validateArchivePath(filepath.ToSlash(relative))
}

func validateArchivePath(raw string) (string, error) {
	if raw == "" || !utf8.ValidString(raw) || len(raw) > maxBackupPathBytes || strings.IndexByte(raw, 0) >= 0 || strings.HasPrefix(raw, "/") || strings.Contains(raw, "\\") || strings.ContainsAny(raw, `<>:"|?*`) {
		return "", fmt.Errorf("backup path is not portable: %q", raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != raw {
		return "", fmt.Errorf("backup path is unsafe or non-canonical: %q", raw)
	}
	for _, part := range strings.Split(raw, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || windowsReservedName(part) {
			return "", fmt.Errorf("backup path is not portable: %q", raw)
		}
		for _, r := range part {
			if r < 0x20 {
				return "", fmt.Errorf("backup path is not portable: %q", raw)
			}
		}
	}
	return raw, nil
}

func windowsReservedName(part string) bool {
	base := part
	if index := strings.IndexByte(base, '.'); index >= 0 {
		base = base[:index]
	}
	base = strings.ToUpper(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

func addPortablePath(seen map[string]string, value string) error {
	key := strings.ToLower(value)
	if previous, exists := seen[key]; exists && previous != value {
		return fmt.Errorf("backup contains paths that collide on Windows: %q and %q", previous, value)
	}
	seen[key] = value
	return nil
}

func realDirectory(input string) (string, error) {
	absolute, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	return filepath.EvalSymlinks(absolute)
}

func resolveTargetPath(input string) (string, error) {
	absolute, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(absolute)
	var missing []string
	for {
		_, err := os.Lstat(parent)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("no existing parent directory")
		}
		missing = append(missing, filepath.Base(parent))
		parent = next
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		realParent = filepath.Join(realParent, missing[i])
	}
	return filepath.Join(realParent, filepath.Base(absolute)), nil
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
