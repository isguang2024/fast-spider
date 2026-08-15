package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/isguang2024/fast-spider/internal/releaseinfo"
)

const (
	maxReleaseVersionBytes  = 128
	maxReleaseManifestCache = 64
)

type releaseManifestStamp struct {
	artifactSize    int64
	artifactModNano int64
	versionSize     int64
	versionModNano  int64
	version         string
	artifactID      releaseFileIdentity
	versionID       releaseFileIdentity
}

type releaseFileState struct {
	info     os.FileInfo
	identity releaseFileIdentity
}

type releaseManifestCacheEntry struct {
	stamp    releaseManifestStamp
	manifest releaseinfo.Manifest
	err      error
	ready    chan struct{}
	cancel   context.CancelFunc
	waiters  int
	lastUsed uint64
}

type releaseManifestCacheStore struct {
	mu      sync.Mutex
	entries map[string]*releaseManifestCacheEntry
	clock   uint64
	max     int
}

var manifestCache = releaseManifestCacheStore{max: maxReleaseManifestCache}

func (c *releaseManifestCacheStore) get(ctx context.Context, key string, stamp releaseManifestStamp, load func(context.Context) (releaseinfo.Manifest, error)) (releaseinfo.Manifest, error) {
	for {
		c.mu.Lock()
		c.clock++
		if entry := c.entries[key]; entry != nil && entry.stamp == stamp {
			entry.lastUsed = c.clock
			if entry.ready == nil {
				manifest, err := entry.manifest, entry.err
				c.mu.Unlock()
				return manifest, err
			}
			entry.waiters++
			ready := entry.ready
			c.mu.Unlock()
			return c.wait(ctx, key, entry, ready)
		}
		if c.entries == nil {
			c.entries = make(map[string]*releaseManifestCacheEntry)
		}
		if c.max <= 0 {
			c.max = maxReleaseManifestCache
		}
		_, replacing := c.entries[key]
		if !replacing && len(c.entries) >= c.max {
			var oldestKey string
			var oldest uint64
			var waitFor chan struct{}
			for candidateKey, candidate := range c.entries {
				if candidate.ready == nil && (oldestKey == "" || candidate.lastUsed < oldest) {
					oldestKey, oldest = candidateKey, candidate.lastUsed
				}
				if waitFor == nil && candidate.ready != nil {
					waitFor = candidate.ready
				}
			}
			if oldestKey == "" {
				c.mu.Unlock()
				select {
				case <-waitFor:
					continue
				case <-ctx.Done():
					return releaseinfo.Manifest{}, ctx.Err()
				}
			}
			delete(c.entries, oldestKey)
		}
		loadCtx, cancel := context.WithCancel(context.Background())
		entry := &releaseManifestCacheEntry{
			stamp: stamp, ready: make(chan struct{}), cancel: cancel, waiters: 1, lastUsed: c.clock,
		}
		c.entries[key] = entry
		ready := entry.ready
		c.mu.Unlock()
		go c.load(key, entry, loadCtx, load)
		return c.wait(ctx, key, entry, ready)
	}
}

func (c *releaseManifestCacheStore) wait(ctx context.Context, key string, entry *releaseManifestCacheEntry, ready <-chan struct{}) (releaseinfo.Manifest, error) {
	select {
	case <-ready:
		return entry.manifest, entry.err
	case <-ctx.Done():
		c.mu.Lock()
		if entry.ready != nil {
			entry.waiters--
			if entry.waiters == 0 {
				entry.cancel()
				if c.entries[key] == entry {
					delete(c.entries, key)
				}
			}
		}
		c.mu.Unlock()
		return releaseinfo.Manifest{}, ctx.Err()
	}
}

func (c *releaseManifestCacheStore) load(key string, entry *releaseManifestCacheEntry, ctx context.Context, load func(context.Context) (releaseinfo.Manifest, error)) {
	manifest, err := load(ctx)
	entry.cancel()
	c.mu.Lock()
	entry.manifest = manifest
	entry.err = err
	close(entry.ready)
	entry.ready = nil
	entry.cancel = nil
	entry.waiters = 0
	if err != nil && c.entries[key] == entry {
		delete(c.entries, key)
	}
	c.mu.Unlock()
}

func (s *Server) handleNodeReleaseManifest(w http.ResponseWriter, r *http.Request) {
	platform := strings.TrimSpace(r.PathValue("platform"))
	if !validReleaseSegment(platform) {
		http.NotFound(w, r)
		return
	}
	artifact := filepath.Join(s.service.ReleaseDir(), "node", platform, nodeReleaseFilename(platform))
	manifest, err := s.releaseManifest(r.Context(), "node", "fast-spider-node", platform, artifact, filepath.Join(filepath.Dir(artifact), "version.txt"), "/api/v1/node/releases/"+platform+"/download")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "release unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) handleNodeReleaseDownload(w http.ResponseWriter, r *http.Request) {
	platform := strings.TrimSpace(r.PathValue("platform"))
	if !validReleaseSegment(platform) {
		http.NotFound(w, r)
		return
	}
	s.serveReleaseArtifact(w, r, filepath.Join(s.service.ReleaseDir(), "node", platform, nodeReleaseFilename(platform)), nodeReleaseFilename(platform))
}

func (s *Server) handleComponentReleaseManifest(w http.ResponseWriter, r *http.Request) {
	componentID := strings.TrimSpace(r.PathValue("componentId"))
	platform := strings.TrimSpace(r.PathValue("platform"))
	if !validReleaseSegment(componentID) || !validReleaseSegment(platform) {
		http.NotFound(w, r)
		return
	}
	root := filepath.Join(s.service.ReleaseDir(), "components", componentID, platform)
	manifest, err := s.releaseManifest(r.Context(), "component", componentID, platform, filepath.Join(root, "component.zip"), filepath.Join(root, "version.txt"), "/api/v1/node/components/"+componentID+"/"+platform+"/download")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "component unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) handleComponentReleaseDownload(w http.ResponseWriter, r *http.Request) {
	componentID := strings.TrimSpace(r.PathValue("componentId"))
	platform := strings.TrimSpace(r.PathValue("platform"))
	if !validReleaseSegment(componentID) || !validReleaseSegment(platform) {
		http.NotFound(w, r)
		return
	}
	s.serveReleaseArtifact(w, r, filepath.Join(s.service.ReleaseDir(), "components", componentID, platform, "component.zip"), componentID+"-"+platform+".zip")
}

func (s *Server) releaseManifest(ctx context.Context, kind, id, platform, artifactPath, versionPath, downloadPath string) (releaseinfo.Manifest, error) {
	if err := ctx.Err(); err != nil {
		return releaseinfo.Manifest{}, err
	}
	version, stamp, err := releaseManifestInputs(artifactPath, versionPath)
	if err != nil {
		return releaseinfo.Manifest{}, err
	}
	key := strings.Join([]string{s.service.HubFingerprint(), kind, id, platform, artifactPath, versionPath, downloadPath}, "\x00")
	return manifestCache.get(ctx, key, stamp, func(loadCtx context.Context) (releaseinfo.Manifest, error) {
		sha256Hex, sizeBytes, err := releaseinfo.FileSHA256Context(loadCtx, artifactPath)
		if err != nil {
			return releaseinfo.Manifest{}, err
		}
		artifactState, err := inspectReleaseFile(artifactPath)
		if err != nil {
			return releaseinfo.Manifest{}, err
		}
		versionState, err := inspectReleaseFile(versionPath)
		if err != nil {
			return releaseinfo.Manifest{}, err
		}
		if !stamp.matches(artifactState, versionState) || sizeBytes != stamp.artifactSize {
			return releaseinfo.Manifest{}, errors.New("release files changed while manifest was generated")
		}
		manifest := releaseinfo.NewManifest(kind, id, platform, version, sha256Hex, sizeBytes, downloadPath)
		if err := releaseinfo.Sign(s.service.HubPrivateKey(), &manifest); err != nil {
			return releaseinfo.Manifest{}, err
		}
		return manifest, nil
	})
}

func releaseManifestInputs(artifactPath, versionPath string) (string, releaseManifestStamp, error) {
	artifactState, err := inspectReleaseFile(artifactPath)
	if err != nil {
		return "", releaseManifestStamp{}, err
	}
	artifactInfo := artifactState.info
	if !artifactInfo.Mode().IsRegular() || artifactInfo.Size() <= 0 {
		return "", releaseManifestStamp{}, errors.New("release artifact is not a non-empty regular file")
	}
	versionFile, err := os.Open(versionPath)
	if err != nil {
		return "", releaseManifestStamp{}, err
	}
	versionInfo, err := versionFile.Stat()
	if err != nil {
		versionFile.Close()
		return "", releaseManifestStamp{}, err
	}
	versionIdentity, err := releaseFileIdentityForFile(versionFile, versionInfo)
	if err != nil {
		versionFile.Close()
		return "", releaseManifestStamp{}, err
	}
	rawVersion, readErr := io.ReadAll(io.LimitReader(versionFile, maxReleaseVersionBytes+1))
	closeErr := versionFile.Close()
	if readErr != nil {
		return "", releaseManifestStamp{}, readErr
	}
	if closeErr != nil {
		return "", releaseManifestStamp{}, closeErr
	}
	if len(rawVersion) == 0 || len(rawVersion) > maxReleaseVersionBytes || int64(len(rawVersion)) != versionInfo.Size() || !versionInfo.Mode().IsRegular() {
		return "", releaseManifestStamp{}, errors.New("release version file is invalid")
	}
	version := strings.TrimSpace(string(rawVersion))
	if version == "" {
		return "", releaseManifestStamp{}, errors.New("release version file is invalid")
	}
	stamp := releaseManifestStamp{
		artifactSize:    artifactInfo.Size(),
		artifactModNano: artifactInfo.ModTime().UnixNano(),
		versionSize:     versionInfo.Size(),
		versionModNano:  versionInfo.ModTime().UnixNano(),
		version:         version,
		artifactID:      artifactState.identity,
		versionID:       versionIdentity,
	}
	return version, stamp, nil
}

func (s releaseManifestStamp) matches(artifact, version releaseFileState) bool {
	return artifact.info.Mode().IsRegular() && version.info.Mode().IsRegular() &&
		artifact.info.Size() == s.artifactSize && artifact.info.ModTime().UnixNano() == s.artifactModNano && artifact.identity == s.artifactID &&
		version.info.Size() == s.versionSize && version.info.ModTime().UnixNano() == s.versionModNano && version.identity == s.versionID
}

func inspectReleaseFile(path string) (releaseFileState, error) {
	file, err := os.Open(path)
	if err != nil {
		return releaseFileState{}, err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return releaseFileState{}, statErr
	}
	identity, identityErr := releaseFileIdentityForFile(file, info)
	closeErr := file.Close()
	if identityErr != nil {
		return releaseFileState{}, identityErr
	}
	if closeErr != nil {
		return releaseFileState{}, closeErr
	}
	return releaseFileState{info: info, identity: identity}, nil
}

func (s *Server) serveReleaseArtifact(w http.ResponseWriter, r *http.Request, path, downloadName string) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "release unavailable", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() <= 0 {
		http.Error(w, "release unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+downloadName+`"`)
	w.Header().Set("Content-Length", strings.TrimSpace(int64String(stat.Size())))
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = io.Copy(w, file)
}

func validReleaseSegment(value string) bool {
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

func nodeReleaseFilename(platform string) string {
	if strings.HasPrefix(platform, "windows-") {
		return "fast-spider-node.exe"
	}
	return "fast-spider-node"
}

func int64String(value int64) string {
	if value == 0 {
		return "0"
	}
	var buf [32]byte
	pos := len(buf)
	for value > 0 {
		pos--
		buf[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[pos:])
}
