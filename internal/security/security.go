package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const randomBytes = 24

func RandomOpaque(prefix string) (string, error) {
	buf := make([]byte, randomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func Fingerprint(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func EncodePublicKey(publicKey ed25519.PublicKey) string {
	return base64.RawStdEncoding.EncodeToString(publicKey)
}

func DecodePublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key length: %d", len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

func EncodeSignature(sig []byte) string {
	return base64.RawStdEncoding.EncodeToString(sig)
}

func DecodeSignature(encoded string) ([]byte, error) {
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	if len(raw) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid signature length: %d", len(raw))
	}
	return raw, nil
}

func LoadOrCreateEd25519(path string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	if raw, err := os.ReadFile(path); err == nil {
		decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, nil, fmt.Errorf("decode private key: %w", err)
		}
		if len(decoded) != ed25519.PrivateKeySize {
			return nil, nil, fmt.Errorf("invalid private key length: %d", len(decoded))
		}
		priv := ed25519.PrivateKey(decoded)
		pub := priv.Public().(ed25519.PublicKey)
		return pub, priv, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("read private key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create key directory: %w", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	encoded := base64.RawStdEncoding.EncodeToString(priv) + "\n"
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreateEd25519(path)
		}
		return nil, nil, fmt.Errorf("create private key: %w", err)
	}
	if _, err := file.WriteString(encoded); err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("write private key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, nil, fmt.Errorf("close private key: %w", err)
	}
	return pub, priv, nil
}
