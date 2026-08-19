package browserext

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	maxArchiveBytes  int64 = 256 << 20
	maxExpandedBytes int64 = 512 << 20
	maxFileBytes     int64 = 64 << 20
	maxArchiveFiles        = 4096
	maxManifestBytes       = 1 << 20

	managedMetadataName = ".fast-spider-browser-extension.json"
)

var (
	ErrExtensionNotFound = errors.New("browser extension is not installed")
)

// Installed is the public, path-free description of an extension installed in
// the Node data directory. Path is intentionally omitted from JSON responses;
// it is only used inside the local browser manager.
type Installed struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Version         string    `json:"version"`
	ManifestVersion int       `json:"manifestVersion"`
	InstalledAt     time.Time `json:"installedAt"`
	Path            string    `json:"-"`
}

type extensionManifest struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ManifestVersion int    `json:"manifest_version"`
	Key             string `json:"key"`
}

type storedMetadata struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Version         string    `json:"version"`
	ManifestVersion int       `json:"manifestVersion"`
	SourceSHA256    string    `json:"sourceSha256"`
	StorageName     string    `json:"storageName"`
	InstalledAt     time.Time `json:"installedAt"`
}

// InstallArchive imports a Chrome extension ZIP into the Node's managed
// browser directory. The archive is never loaded directly: it is bounded,
// path-checked, extracted into a private staging directory, and published by
// an extension identity plus source hash.
func InstallArchive(dataDir, archivePath string) (Installed, error) {
	root, err := extensionRoot(dataDir)
	if err != nil {
		return Installed{}, err
	}
	absoluteArchive, err := filepath.Abs(filepath.Clean(strings.TrimSpace(archivePath)))
	if err != nil || absoluteArchive == "" {
		return Installed{}, fmt.Errorf("extension archive path is invalid")
	}
	archiveInfo, err := os.Lstat(absoluteArchive)
	if err != nil {
		return Installed{}, fmt.Errorf("stat extension archive: %w", err)
	}
	if archiveInfo.Mode()&os.ModeSymlink != 0 || !archiveInfo.Mode().IsRegular() {
		return Installed{}, fmt.Errorf("extension archive must be a regular file")
	}
	if archiveInfo.Size() < 1 || archiveInfo.Size() > maxArchiveBytes {
		return Installed{}, fmt.Errorf("extension archive exceeds the %d MiB limit", maxArchiveBytes>>20)
	}

	sourceSHA256, err := fileSHA256(absoluteArchive)
	if err != nil {
		return Installed{}, fmt.Errorf("hash extension archive: %w", err)
	}

	reader, err := zip.OpenReader(absoluteArchive)
	if err != nil {
		return Installed{}, fmt.Errorf("open extension archive: %w", err)
	}
	defer reader.Close()

	stageID, err := security.RandomOpaque("stage_")
	if err != nil {
		return Installed{}, err
	}
	stage := filepath.Join(root, "."+stageID)
	if err := os.Mkdir(stage, 0o700); err != nil {
		return Installed{}, fmt.Errorf("create extension staging directory: %w", err)
	}
	defer os.RemoveAll(stage)

	rawRoot := filepath.Join(stage, "raw")
	if err := os.Mkdir(rawRoot, 0o700); err != nil {
		return Installed{}, fmt.Errorf("create extension extraction directory: %w", err)
	}
	if err := extractArchive(reader.File, rawRoot); err != nil {
		return Installed{}, err
	}
	extensionRootPath, err := findExtensionRoot(rawRoot)
	if err != nil {
		return Installed{}, err
	}
	manifest, err := readManifest(filepath.Join(extensionRootPath, "manifest.json"))
	if err != nil {
		return Installed{}, err
	}

	id := extensionID(manifest)
	storageName := sourceSHA256[:16]
	idDir := filepath.Join(root, id)
	if err := ensureManagedDirectory(idDir); err != nil {
		return Installed{}, err
	}
	finalPath := filepath.Join(idDir, storageName)
	if info, statErr := os.Lstat(finalPath); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Installed{}, fmt.Errorf("managed extension version path is not a directory")
		}
		metadata, readErr := readStoredExtensionVersion(root, id, storageName)
		if readErr == nil && metadata.SourceSHA256 == sourceSHA256 {
			// The source was already extracted. Re-select it in case a newer
			// version of the same extension is currently selected.
			if err := writeCurrentMetadata(idDir, metadata); err != nil {
				return Installed{}, fmt.Errorf("select installed browser extension: %w", err)
			}
			return installedFromMetadata(metadata, finalPath), nil
		}
		return Installed{}, fmt.Errorf("managed extension version already exists but metadata is invalid")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Installed{}, fmt.Errorf("check managed extension version: %w", statErr)
	}

	payload := filepath.Join(stage, "payload")
	if err := os.Mkdir(payload, 0o700); err != nil {
		return Installed{}, fmt.Errorf("create extension payload directory: %w", err)
	}
	if err := moveDirectoryContents(extensionRootPath, payload); err != nil {
		return Installed{}, err
	}
	installedAt := time.Now().UTC()
	metadata := storedMetadata{
		ID: id, Name: manifest.Name, Version: manifest.Version,
		ManifestVersion: manifest.ManifestVersion, SourceSHA256: sourceSHA256,
		StorageName: storageName, InstalledAt: installedAt,
	}
	if err := writeJSONFile(filepath.Join(payload, managedMetadataName), metadata); err != nil {
		return Installed{}, fmt.Errorf("write extension metadata: %w", err)
	}
	if err := publishExtensionDirectory(payload, finalPath); err != nil {
		// Another local import may have published the same content between the
		// existence check and the rename. Reuse that verified directory rather
		// than turning a harmless concurrent import into a failure.
		if existing, readErr := readStoredExtensionVersion(root, id, storageName); readErr == nil && existing.SourceSHA256 == sourceSHA256 {
			if currentErr := writeCurrentMetadata(idDir, existing); currentErr != nil {
				return Installed{}, fmt.Errorf("select installed browser extension: %w", currentErr)
			}
			return installedFromMetadata(existing, finalPath), nil
		}
		return Installed{}, fmt.Errorf("publish browser extension: %w", err)
	}
	if err := writeCurrentMetadata(idDir, metadata); err != nil {
		return Installed{}, fmt.Errorf("select installed browser extension: %w", err)
	}
	return Installed{
		ID: id, Name: manifest.Name, Version: manifest.Version,
		ManifestVersion: manifest.ManifestVersion, InstalledAt: installedAt, Path: finalPath,
	}, nil
}

// List returns the currently selected version for each installed extension.
// It intentionally omits absolute paths from the JSON representation.
func List(dataDir string) ([]Installed, error) {
	root, err := extensionRoot(dataDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []Installed{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read installed browser extensions: %w", err)
	}
	result := make([]Installed, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validExtensionID(entry.Name()) {
			continue
		}
		installed, readErr := readStoredExtension(root, entry.Name(), nil)
		if errors.Is(readErr, ErrExtensionNotFound) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, installed)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// Resolve returns one installed extension by its path-free ID.
func Resolve(dataDir, id string) (Installed, error) {
	if !validExtensionID(id) {
		return Installed{}, ErrExtensionNotFound
	}
	root, err := extensionRoot(dataDir)
	if err != nil {
		return Installed{}, err
	}
	return readStoredExtension(root, id, nil)
}

func extensionRoot(dataDir string) (string, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return "", errors.New("browser extension data directory is required")
	}
	root := filepath.Join(dataDir, "browser", "extensions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create browser extension directory: %w", err)
	}
	return root, nil
}

func ensureManagedDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed browser extension directory is invalid")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Mkdir(path, 0o700)
}

func extractArchive(files []*zip.File, rawRoot string) error {
	if len(files) == 0 || len(files) > maxArchiveFiles {
		return fmt.Errorf("extension archive must contain between 1 and %d files", maxArchiveFiles)
	}
	seen := make(map[string]struct{}, len(files))
	var expandedBytes int64
	for _, file := range files {
		relative, err := safeArchivePath(file.Name)
		if err != nil {
			return err
		}
		if relative == "" {
			continue
		}
		if filepath.Base(filepath.FromSlash(relative)) == managedMetadataName {
			return fmt.Errorf("extension archive contains a reserved metadata file")
		}
		if _, exists := seen[relative]; exists {
			return fmt.Errorf("extension archive contains duplicate path %q", relative)
		}
		seen[relative] = struct{}{}
		destination := filepath.Join(rawRoot, filepath.FromSlash(relative))
		if !pathWithin(rawRoot, destination) {
			return fmt.Errorf("extension archive path escapes the staging directory")
		}
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("extension archive symlinks are not allowed")
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0o700); err != nil {
				return fmt.Errorf("create extension directory: %w", err)
			}
			continue
		}
		if mode != 0 && !mode.IsRegular() {
			return fmt.Errorf("extension archive contains an unsupported file type")
		}
		if file.UncompressedSize64 > uint64(maxFileBytes) {
			return fmt.Errorf("extension file %q exceeds the %d MiB limit", relative, maxFileBytes>>20)
		}
		if expandedBytes > maxExpandedBytes-int64(file.UncompressedSize64) {
			return fmt.Errorf("extension archive expands beyond the %d MiB limit", maxExpandedBytes>>20)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return fmt.Errorf("create extension file directory: %w", err)
		}
		input, err := file.Open()
		if err != nil {
			return fmt.Errorf("read extension archive entry: %w", err)
		}
		output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = input.Close()
			return fmt.Errorf("create extracted extension file: %w", err)
		}
		copied, copyErr := io.Copy(output, io.LimitReader(input, maxFileBytes+1))
		closeInputErr := input.Close()
		closeOutputErr := output.Close()
		if copyErr != nil || closeInputErr != nil || closeOutputErr != nil {
			_ = os.Remove(destination)
			return fmt.Errorf("extract extension file %q: %w", relative, errors.Join(copyErr, closeInputErr, closeOutputErr))
		}
		if copied > maxFileBytes || copied != int64(file.UncompressedSize64) {
			_ = os.Remove(destination)
			return fmt.Errorf("extension file %q has an invalid expanded size", relative)
		}
		expandedBytes += copied
	}
	return nil
}

func safeArchivePath(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, '\x00') || strings.Contains(raw, "\\") {
		return "", fmt.Errorf("extension archive contains an invalid path")
	}
	clean := pathpkg.Clean(raw)
	if clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, ":") {
		return "", fmt.Errorf("extension archive path %q is outside the archive root", raw)
	}
	return clean, nil
}

func findExtensionRoot(rawRoot string) (string, error) {
	if regularFile(filepath.Join(rawRoot, "manifest.json")) {
		return rawRoot, nil
	}
	entries, err := os.ReadDir(rawRoot)
	if err != nil {
		return "", fmt.Errorf("inspect extracted extension: %w", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return "", errors.New("extension archive must contain manifest.json at its root or under one top-level directory")
	}
	candidate := filepath.Join(rawRoot, entries[0].Name())
	if !regularFile(filepath.Join(candidate, "manifest.json")) {
		return "", errors.New("extension manifest.json was not found")
	}
	return candidate, nil
}

func readManifest(path string) (extensionManifest, error) {
	info, err := os.Stat(path)
	if err != nil {
		return extensionManifest{}, errors.New("extension manifest.json was not found")
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxManifestBytes {
		return extensionManifest{}, errors.New("extension manifest.json is invalid")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return extensionManifest{}, fmt.Errorf("read extension manifest: %w", err)
	}
	raw = bytesTrimUTF8BOM(raw)
	var manifest extensionManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return extensionManifest{}, fmt.Errorf("decode extension manifest: %w", err)
	}
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Version = strings.TrimSpace(manifest.Version)
	if manifest.Name == "" || len(manifest.Name) > 256 || manifest.Version == "" || len(manifest.Version) > 64 {
		return extensionManifest{}, errors.New("extension manifest name or version is invalid")
	}
	if manifest.ManifestVersion != 2 && manifest.ManifestVersion != 3 {
		return extensionManifest{}, errors.New("only Chrome Manifest V2 and V3 extensions are supported")
	}
	return manifest, nil
}

func moveDirectoryContents(source, destination string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("read extracted extension root: %w", err)
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return fmt.Errorf("normalize extracted extension root: %w", err)
		}
	}
	return nil
}

func publishExtensionDirectory(source, destination string) error {
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		if err := os.Rename(source, destination); err == nil {
			return nil
		} else {
			lastErr = err
			if !errors.Is(err, os.ErrPermission) {
				break
			}
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	return lastErr
}

func readStoredExtension(root, id string, expected *storedMetadata) (Installed, error) {
	if !validExtensionID(id) {
		return Installed{}, ErrExtensionNotFound
	}
	idDir := filepath.Join(root, id)
	raw, err := os.ReadFile(filepath.Join(idDir, "current.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Installed{}, ErrExtensionNotFound
	}
	if err != nil {
		return Installed{}, fmt.Errorf("read installed browser extension: %w", err)
	}
	var metadata storedMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return Installed{}, fmt.Errorf("decode installed browser extension metadata: %w", err)
	}
	if metadata.ID != id || !validStorageName(metadata.StorageName) || len(metadata.SourceSHA256) != 64 || !isLowerHex(metadata.SourceSHA256) {
		return Installed{}, errors.New("installed browser extension metadata is invalid")
	}
	if expected != nil && metadata.SourceSHA256 != expected.SourceSHA256 {
		return Installed{}, ErrExtensionNotFound
	}
	stored, err := readStoredExtensionVersion(root, id, metadata.StorageName)
	if err != nil {
		return Installed{}, err
	}
	if stored.SourceSHA256 != metadata.SourceSHA256 {
		return Installed{}, errors.New("installed browser extension metadata is inconsistent")
	}
	return installedFromMetadata(stored, filepath.Join(idDir, stored.StorageName)), nil
}

func readStoredExtensionVersion(root, id, storageName string) (storedMetadata, error) {
	if !validExtensionID(id) || !validStorageName(storageName) {
		return storedMetadata{}, ErrExtensionNotFound
	}
	path := filepath.Join(root, id, storageName)
	if !pathWithin(root, path) || !regularDirectory(path) {
		return storedMetadata{}, errors.New("installed browser extension path is invalid")
	}
	raw, err := os.ReadFile(filepath.Join(path, managedMetadataName))
	if err != nil {
		return storedMetadata{}, errors.New("installed browser extension metadata is missing")
	}
	var metadata storedMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return storedMetadata{}, fmt.Errorf("decode installed browser extension metadata: %w", err)
	}
	if metadata.ID != id || metadata.StorageName != storageName || len(metadata.SourceSHA256) != 64 || !isLowerHex(metadata.SourceSHA256) || !strings.HasPrefix(metadata.SourceSHA256, storageName) || metadata.Name == "" || metadata.Version == "" || (metadata.ManifestVersion != 2 && metadata.ManifestVersion != 3) || metadata.InstalledAt.IsZero() || !regularFile(filepath.Join(path, "manifest.json")) {
		return storedMetadata{}, errors.New("installed browser extension metadata is invalid")
	}
	return metadata, nil
}

func installedFromMetadata(metadata storedMetadata, path string) Installed {
	return Installed{
		ID: metadata.ID, Name: metadata.Name, Version: metadata.Version,
		ManifestVersion: metadata.ManifestVersion, InstalledAt: metadata.InstalledAt, Path: path,
	}
}

func writeCurrentMetadata(idDir string, metadata storedMetadata) error {
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(idDir, ".current-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	current := filepath.Join(idDir, "current.json")
	_ = os.Remove(current)
	return os.Rename(tmpPath, current)
}

func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o600)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxArchiveBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extensionID(manifest extensionManifest) string {
	identity := manifest.Key
	if identity == "" {
		identity = manifest.Name
	}
	sum := sha256.Sum256([]byte(identity))
	return "ext_" + hex.EncodeToString(sum[:8])
}

func validExtensionID(value string) bool {
	return strings.HasPrefix(value, "ext_") && len(value) == len("ext_")+16 && isLowerHex(value[len("ext_"):])
}

func validStorageName(value string) bool {
	return len(value) == 16 && isLowerHex(value)
}

func isLowerHex(value string) bool {
	for _, character := range value {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func regularDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func pathWithin(root, candidate string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(candidateAbs))
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func bytesTrimUTF8BOM(raw []byte) []byte {
	if len(raw) >= 3 && raw[0] == 0xef && raw[1] == 0xbb && raw[2] == 0xbf {
		return raw[3:]
	}
	return raw
}
