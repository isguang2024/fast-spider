package componentmgr

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/isguang2024/fast-spider/internal/nodeupdate"
	"github.com/isguang2024/fast-spider/internal/releaseinfo"
)

const (
	maxComponentArchiveBytes = 2 << 30
	maxComponentFiles        = 10000
	maxComponentExpanded     = 8 << 30
)

type Installed struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
	Path     string `json:"path"`
}

var (
	ErrComponentNotInstalled = errors.New("managed component is not installed")
	ErrComponentInvalid      = errors.New("managed component installation is invalid")
	componentLocks           [64]sync.RWMutex
)

func Root(dataDir string) string { return filepath.Join(dataDir, "components") }

// FindInstalled returns the newest verified managed component installation.
// It exposes no PATH-based or unverified installation fallback.
func FindInstalled(dataDir, componentID string) (Installed, error) {
	lock := componentLock(dataDir, componentID)
	lock.RLock()
	defer lock.RUnlock()
	return findInstalled(dataDir, componentID, nil)
}

// FindInstalledExecutable resolves an executable only from a verified managed
// component version directory. It never consults PATH or follows links.
func FindInstalledExecutable(dataDir, componentID, executableName string) (Installed, string, error) {
	if !validID(componentID) || strings.TrimSpace(executableName) == "" || filepath.Base(executableName) != executableName {
		return Installed{}, "", ErrComponentInvalid
	}
	lock := componentLock(dataDir, componentID)
	lock.RLock()
	defer lock.RUnlock()
	var executablePath string
	installed, err := findInstalled(dataDir, componentID, func(versionDir string) bool {
		candidate := filepath.Join(versionDir, executableName)
		info, statErr := os.Lstat(candidate)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
			return false
		}
		executablePath = candidate
		return true
	})
	return installed, executablePath, err
}

func findInstalled(dataDir, componentID string, accept func(string) bool) (Installed, error) {
	if !validID(componentID) {
		return Installed{}, ErrComponentInvalid
	}
	componentRoot := filepath.Join(Root(dataDir), componentID)
	entries, err := os.ReadDir(componentRoot)
	if errors.Is(err, os.ErrNotExist) {
		return Installed{}, ErrComponentNotInstalled
	}
	if err != nil {
		return Installed{}, fmt.Errorf("read managed component: %w", ErrComponentInvalid)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left, leftErr := releaseinfo.ParseVersion(entries[i].Name())
		right, rightErr := releaseinfo.ParseVersion(entries[j].Name())
		if leftErr == nil && rightErr == nil {
			if left.Major != right.Major {
				return left.Major > right.Major
			}
			if left.Minor != right.Minor {
				return left.Minor > right.Minor
			}
			if left.Patch != right.Patch {
				return left.Patch > right.Patch
			}
		}
		if leftErr == nil && rightErr != nil {
			return true
		}
		if leftErr != nil && rightErr == nil {
			return false
		}
		return entries[i].Name() < entries[j].Name()
	})
	platform := runtime.GOOS + "-" + runtime.GOARCH
	foundInvalid := false
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		versionDir := filepath.Join(componentRoot, entry.Name())
		versionInfo, statErr := os.Lstat(versionDir)
		if statErr != nil || !versionInfo.IsDir() || versionInfo.Mode()&os.ModeSymlink != 0 {
			foundInvalid = true
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(versionDir, ".fast-spider-component.json"))
		if readErr != nil {
			foundInvalid = true
			continue
		}
		var manifest releaseinfo.Manifest
		if json.Unmarshal(raw, &manifest) != nil || manifest.Kind != "component" || manifest.ID != componentID || manifest.Platform != platform || manifest.Version != entry.Name() {
			foundInvalid = true
			continue
		}
		if accept != nil && !accept(versionDir) {
			foundInvalid = true
			continue
		}
		installed := Installed{ID: componentID, Platform: platform, Version: manifest.Version, Path: versionDir}
		return installed, nil
	}
	if foundInvalid {
		return Installed{}, ErrComponentInvalid
	}
	return Installed{}, ErrComponentNotInstalled
}

func CleanupConfigured(dataDir, componentID, componentPath string) error {
	componentID = strings.TrimSpace(componentID)
	componentPath = strings.TrimSpace(componentPath)
	if !validID(componentID) || componentPath == "" {
		return nil
	}
	lock := componentLock(dataDir, componentID)
	lock.Lock()
	defer lock.Unlock()
	managedRoot, err := filepath.Abs(filepath.Join(Root(dataDir), componentID))
	if err != nil {
		return err
	}
	configuredPath, err := filepath.Abs(componentPath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(managedRoot, configuredPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.Contains(rel, string(filepath.Separator)) {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(configuredPath, ".fast-spider-component.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var manifest releaseinfo.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	if manifest.Kind != "component" || manifest.ID != componentID || manifest.Version != rel || strings.TrimSpace(manifest.Platform) == "" {
		return nil
	}
	return cleanupInstalled(dataDir, Installed{ID: manifest.ID, Platform: manifest.Platform, Version: manifest.Version, Path: configuredPath})
}

func CleanupInstalled(dataDir string, installed Installed) error {
	lock := componentLock(dataDir, installed.ID)
	lock.Lock()
	defer lock.Unlock()
	return cleanupInstalled(dataDir, installed)
}

func cleanupInstalled(dataDir string, installed Installed) error {
	if !validID(installed.ID) || strings.TrimSpace(installed.Version) == "" || strings.TrimSpace(installed.Platform) == "" {
		return errors.New("installed component metadata is invalid")
	}
	var cleanupErr error
	cacheDir := filepath.Join(dataDir, "cache", "components")
	if entries, err := os.ReadDir(cacheDir); err == nil {
		prefix := installed.ID + "-" + installed.Platform + "-"
		stagingPrefix := ".fast-spider-component-download-" + installed.ID + "-" + installed.Platform + "-"
		for _, entry := range entries {
			managedArchive := !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".zip")
			stagedArchive := !entry.IsDir() && strings.HasPrefix(entry.Name(), stagingPrefix) && (strings.HasSuffix(entry.Name(), ".zip") || strings.HasSuffix(entry.Name(), ".zip.tmp"))
			if !managedArchive && !stagedArchive {
				continue
			}
			if err := os.Remove(filepath.Join(cacheDir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		cleanupErr = errors.Join(cleanupErr, err)
	}

	componentRoot := filepath.Join(Root(dataDir), installed.ID)
	if entries, err := os.ReadDir(componentRoot); err == nil {
		for _, entry := range entries {
			if entry.Name() == installed.Version {
				continue
			}
			path := filepath.Join(componentRoot, entry.Name())
			if entry.IsDir() || strings.HasSuffix(entry.Name(), ".tmp") {
				if err := os.RemoveAll(path); err != nil {
					cleanupErr = errors.Join(cleanupErr, err)
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	return cleanupErr
}

func Ensure(ctx context.Context, dataDir, hubURL, encodedHubPublicKey, componentID string) (Installed, error) {
	componentID = strings.TrimSpace(componentID)
	if !validID(componentID) {
		return Installed{}, errors.New("component id is invalid")
	}
	lock := componentLock(dataDir, componentID)
	lock.Lock()
	defer lock.Unlock()
	platform := runtime.GOOS + "-" + runtime.GOARCH
	manifest, err := nodeupdate.FetchManifest(ctx, hubURL, encodedHubPublicKey,
		"/api/v1/node/components/"+componentID+"/"+platform+"/latest", "component", componentID, platform)
	if err != nil {
		return Installed{}, err
	}
	if manifest.SizeBytes > maxComponentArchiveBytes {
		return Installed{}, errors.New("component archive exceeds size limit")
	}
	componentRoot := filepath.Join(Root(dataDir), componentID)
	finalDir := filepath.Join(componentRoot, manifest.Version)
	if installedManifestMatches(finalDir, manifest) {
		return Installed{ID: componentID, Platform: platform, Version: manifest.Version, Path: finalDir}, nil
	}
	cacheDir := filepath.Join(dataDir, "cache", "components")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return Installed{}, err
	}
	archivePath := filepath.Join(cacheDir, componentID+"-"+platform+"-"+manifest.Version+".zip")
	archiveStaging, err := os.CreateTemp(cacheDir, ".fast-spider-component-download-"+componentID+"-"+platform+"-*.zip")
	if err != nil {
		return Installed{}, err
	}
	archiveStagingPath := archiveStaging.Name()
	if closeErr := archiveStaging.Close(); closeErr != nil {
		_ = os.Remove(archiveStagingPath)
		return Installed{}, closeErr
	}
	if err := os.Remove(archiveStagingPath); err != nil {
		return Installed{}, err
	}
	defer os.Remove(archiveStagingPath)
	if err := nodeupdate.DownloadVerified(ctx, hubURL, manifest, archiveStagingPath); err != nil {
		return Installed{}, err
	}
	if err := os.MkdirAll(componentRoot, 0o700); err != nil {
		return Installed{}, err
	}
	tmpDir, err := os.MkdirTemp(componentRoot, ".fast-spider-component-install-")
	if err != nil {
		return Installed{}, err
	}
	defer os.RemoveAll(tmpDir)
	if err := extractArchive(archiveStagingPath, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return Installed{}, err
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return Installed{}, err
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".fast-spider-component.json"), append(raw, '\n'), 0o600); err != nil {
		_ = os.RemoveAll(tmpDir)
		return Installed{}, err
	}
	if err := replaceComponentArchive(archiveStagingPath, archivePath); err != nil {
		return Installed{}, err
	}
	if err := publishComponentDir(tmpDir, finalDir); err != nil {
		return Installed{}, err
	}
	return Installed{ID: componentID, Platform: platform, Version: manifest.Version, Path: finalDir}, nil
}

func componentLock(dataDir, componentID string) *sync.RWMutex {
	root, err := filepath.Abs(Root(dataDir))
	if err != nil {
		root = filepath.Clean(Root(dataDir))
	}
	key := filepath.Clean(root) + "\x00" + strings.TrimSpace(componentID)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(key))
	return &componentLocks[hash.Sum64()%uint64(len(componentLocks))]
}

func publishComponentDir(stagingDir, finalDir string) error {
	if _, err := os.Lstat(finalDir); errors.Is(err, os.ErrNotExist) {
		return os.Rename(stagingDir, finalDir)
	} else if err != nil {
		return err
	}
	parent := filepath.Dir(finalDir)
	backupDir, err := os.MkdirTemp(parent, ".fast-spider-component-replaced-")
	if err != nil {
		return err
	}
	if err := os.Remove(backupDir); err != nil {
		return err
	}
	if err := os.Rename(finalDir, backupDir); err != nil {
		return err
	}
	if err := os.Rename(stagingDir, finalDir); err != nil {
		rollbackErr := os.Rename(backupDir, finalDir)
		return errors.Join(err, rollbackErr)
	}
	return os.RemoveAll(backupDir)
}

func replaceComponentArchive(stagingPath, finalPath string) error {
	if err := os.Remove(finalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(stagingPath, finalPath)
}

func installedManifestMatches(dir string, expected releaseinfo.Manifest) bool {
	raw, err := os.ReadFile(filepath.Join(dir, ".fast-spider-component.json"))
	if err != nil {
		return false
	}
	var current releaseinfo.Manifest
	if json.Unmarshal(raw, &current) != nil {
		return false
	}
	return current.Kind == expected.Kind && current.ID == expected.ID && current.Platform == expected.Platform && current.Version == expected.Version && current.SHA256 == expected.SHA256
}

func extractArchive(archivePath, destination string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	if len(archive.File) > maxComponentFiles {
		return errors.New("component archive has too many files")
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	var expanded uint64
	for _, entry := range archive.File {
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 || (!entry.FileInfo().Mode().IsRegular() && !entry.FileInfo().IsDir()) {
			return errors.New("component archive contains unsupported file type")
		}
		name := filepath.Clean(filepath.FromSlash(entry.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return errors.New("component archive contains unsafe path")
		}
		target := filepath.Join(destination, name)
		rel, err := filepath.Rel(destination, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("component archive escapes destination")
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		expanded += entry.UncompressedSize64
		if expanded > maxComponentExpanded {
			return errors.New("component archive expands beyond limit")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		mode := entry.Mode().Perm()
		if mode == 0 {
			mode = 0o600
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			input.Close()
			return err
		}
		written, copyErr := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
		closeOutErr := output.Close()
		closeInErr := input.Close()
		if copyErr != nil || closeOutErr != nil || closeInErr != nil || written != int64(entry.UncompressedSize64) {
			return fmt.Errorf("extract component file %q failed", entry.Name)
		}
	}
	return nil
}

func validID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			if i == 0 && (r == '-' || r == '_' || r == '.') {
				return false
			}
			continue
		}
		return false
	}
	return true
}
