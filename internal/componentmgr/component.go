package componentmgr

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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

func Root(dataDir string) string { return filepath.Join(dataDir, "components") }

func CleanupConfigured(dataDir, componentID, componentPath string) error {
	componentID = strings.TrimSpace(componentID)
	componentPath = strings.TrimSpace(componentPath)
	if !validID(componentID) || componentPath == "" {
		return nil
	}
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
	return CleanupInstalled(dataDir, Installed{ID: manifest.ID, Platform: manifest.Platform, Version: manifest.Version, Path: configuredPath})
}

func CleanupInstalled(dataDir string, installed Installed) error {
	if !validID(installed.ID) || strings.TrimSpace(installed.Version) == "" || strings.TrimSpace(installed.Platform) == "" {
		return errors.New("installed component metadata is invalid")
	}
	var cleanupErr error
	cacheDir := filepath.Join(dataDir, "cache", "components")
	if entries, err := os.ReadDir(cacheDir); err == nil {
		prefix := installed.ID + "-" + installed.Platform + "-"
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".zip") {
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
	platform := runtime.GOOS + "-" + runtime.GOARCH
	manifest, err := nodeupdate.FetchManifest(ctx, hubURL, encodedHubPublicKey,
		"/api/v1/node/components/"+componentID+"/"+platform+"/latest", "component", componentID, platform)
	if err != nil {
		return Installed{}, err
	}
	if manifest.SizeBytes > maxComponentArchiveBytes {
		return Installed{}, errors.New("component archive exceeds size limit")
	}
	finalDir := filepath.Join(Root(dataDir), componentID, manifest.Version)
	if installedManifestMatches(finalDir, manifest) {
		return Installed{ID: componentID, Platform: platform, Version: manifest.Version, Path: finalDir}, nil
	}
	cacheDir := filepath.Join(dataDir, "cache", "components")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return Installed{}, err
	}
	archivePath := filepath.Join(cacheDir, componentID+"-"+platform+"-"+manifest.Version+".zip")
	if err := nodeupdate.DownloadVerified(ctx, hubURL, manifest, archivePath); err != nil {
		return Installed{}, err
	}
	tmpDir := finalDir + ".tmp"
	_ = os.RemoveAll(tmpDir)
	if err := extractArchive(archivePath, tmpDir); err != nil {
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
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o700); err != nil {
		_ = os.RemoveAll(tmpDir)
		return Installed{}, err
	}
	_ = os.RemoveAll(finalDir)
	if err := os.Rename(tmpDir, finalDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return Installed{}, err
	}
	return Installed{ID: componentID, Platform: platform, Version: manifest.Version, Path: finalDir}, nil
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
