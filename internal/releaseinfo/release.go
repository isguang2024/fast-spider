package releaseinfo

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const ManifestVersion = 1

type Manifest struct {
	ManifestVersion int    `json:"manifestVersion"`
	Kind            string `json:"kind"`
	ID              string `json:"id"`
	Platform        string `json:"platform"`
	Version         string `json:"version"`
	SHA256          string `json:"sha256"`
	SizeBytes       int64  `json:"sizeBytes"`
	DownloadPath    string `json:"downloadPath"`
	Signature       string `json:"signature"`
}

func NewManifest(kind, id, platform, version, sha256Hex string, sizeBytes int64, downloadPath string) Manifest {
	return Manifest{
		ManifestVersion: ManifestVersion,
		Kind:            strings.TrimSpace(kind),
		ID:              strings.TrimSpace(id),
		Platform:        strings.TrimSpace(platform),
		Version:         strings.TrimSpace(version),
		SHA256:          strings.ToLower(strings.TrimSpace(sha256Hex)),
		SizeBytes:       sizeBytes,
		DownloadPath:    strings.TrimSpace(downloadPath),
	}
}

func (m Manifest) Validate() error {
	if m.ManifestVersion != ManifestVersion {
		return fmt.Errorf("unsupported release manifest version %d", m.ManifestVersion)
	}
	if !validIdentifier(m.Kind, 32) || !validIdentifier(m.ID, 64) || !validIdentifier(m.Platform, 64) {
		return errors.New("release manifest identifier is invalid")
	}
	if !validReleaseVersion(m.Version) {
		return errors.New("release version is invalid")
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return err
	}
	if len(m.SHA256) != 64 {
		return errors.New("release manifest sha256 is invalid")
	}
	if _, err := hex.DecodeString(m.SHA256); err != nil {
		return errors.New("release manifest sha256 is invalid")
	}
	if m.SizeBytes <= 0 || m.SizeBytes > 2<<30 {
		return errors.New("release manifest size is invalid")
	}
	if m.DownloadPath == "" || !strings.HasPrefix(m.DownloadPath, "/") || strings.Contains(m.DownloadPath, "..") || strings.ContainsAny(m.DownloadPath, "?#") {
		return errors.New("release manifest download path is invalid")
	}
	return nil
}

func validReleaseVersion(value string) bool {
	if value == "" || len(value) > 32 || strings.Count(value, ".") != 2 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validIdentifier(value string, max int) bool {
	if value == "" || len(value) > max {
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

func Payload(m Manifest) []byte {
	return []byte(fmt.Sprintf("fast-spider-release/v1\n%d\n%s\n%s\n%s\n%s\n%s\n%d\n%s\n",
		m.ManifestVersion, m.Kind, m.ID, m.Platform, m.Version, m.SHA256, m.SizeBytes, m.DownloadPath))
}

func Sign(privateKey ed25519.PrivateKey, manifest *Manifest) error {
	if manifest == nil {
		return errors.New("release manifest is required")
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	manifest.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, Payload(*manifest)))
	return nil
}

func Verify(publicKey ed25519.PublicKey, manifest Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	signature, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(manifest.Signature))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("release manifest signature is invalid")
	}
	if !ed25519.Verify(publicKey, Payload(manifest), signature) {
		return errors.New("release manifest signature verification failed")
	}
	return nil
}

func FileSHA256(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !stat.Mode().IsRegular() || stat.Size() <= 0 {
		return "", 0, errors.New("release artifact is not a non-empty regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), stat.Size(), nil
}

type Version struct {
	Major int
	Minor int
	Patch int
}

func ParseVersion(raw string) (Version, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 {
		return Version{}, errors.New("version is invalid")
	}
	if idx := strings.IndexByte(raw, '-'); idx >= 0 {
		raw = raw[:idx]
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Version{}, errors.New("version must use major.minor.patch")
	}
	values := make([]int, 3)
	for i, part := range parts {
		if part == "" || len(part) > 6 {
			return Version{}, errors.New("version is invalid")
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || value > 999999 {
			return Version{}, errors.New("version is invalid")
		}
		values[i] = value
	}
	return Version{Major: values[0], Minor: values[1], Patch: values[2]}, nil
}

func Compare(a, b string) (int, error) {
	left, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	right, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	lv := []int{left.Major, left.Minor, left.Patch}
	rv := []int{right.Major, right.Minor, right.Patch}
	for i := range lv {
		if lv[i] < rv[i] {
			return -1, nil
		}
		if lv[i] > rv[i] {
			return 1, nil
		}
	}
	return 0, nil
}
