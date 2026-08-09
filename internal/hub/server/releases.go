package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/isguang2024/fast-spider/internal/releaseinfo"
)

const maxReleaseVersionBytes = 128

func (s *Server) handleNodeReleaseManifest(w http.ResponseWriter, r *http.Request) {
	platform := strings.TrimSpace(r.PathValue("platform"))
	if !validReleaseSegment(platform) {
		http.NotFound(w, r)
		return
	}
	artifact := filepath.Join(s.service.ReleaseDir(), "node", platform, nodeReleaseFilename(platform))
	manifest, err := s.releaseManifest("node", "fast-spider-node", platform, artifact, filepath.Join(filepath.Dir(artifact), "version.txt"), "/api/v1/node/releases/"+platform+"/download")
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
	manifest, err := s.releaseManifest("component", componentID, platform, filepath.Join(root, "component.zip"), filepath.Join(root, "version.txt"), "/api/v1/node/components/"+componentID+"/"+platform+"/download")
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

func (s *Server) releaseManifest(kind, id, platform, artifactPath, versionPath, downloadPath string) (releaseinfo.Manifest, error) {
	rawVersion, err := os.ReadFile(versionPath)
	if err != nil {
		return releaseinfo.Manifest{}, err
	}
	if len(rawVersion) == 0 || len(rawVersion) > maxReleaseVersionBytes {
		return releaseinfo.Manifest{}, errors.New("release version file is invalid")
	}
	version := strings.TrimSpace(string(rawVersion))
	sha256Hex, sizeBytes, err := releaseinfo.FileSHA256(artifactPath)
	if err != nil {
		return releaseinfo.Manifest{}, err
	}
	manifest := releaseinfo.NewManifest(kind, id, platform, version, sha256Hex, sizeBytes, downloadPath)
	if err := releaseinfo.Sign(s.service.HubPrivateKey(), &manifest); err != nil {
		return releaseinfo.Manifest{}, err
	}
	return manifest, nil
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
