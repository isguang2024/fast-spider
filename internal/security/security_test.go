package security

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestKeyRoundTripAndEncodingValidation(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "secrets", "hub.key")
	publicKey, privateKey, err := LoadOrCreateEd25519(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(publicKey) != ed25519.PublicKeySize || len(privateKey) != ed25519.PrivateKeySize {
		t.Fatalf("unexpected key sizes: public=%d private=%d", len(publicKey), len(privateKey))
	}
	publicKeyAgain, privateKeyAgain, err := LoadOrCreateEd25519(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(publicKey) != string(publicKeyAgain) || string(privateKey) != string(privateKeyAgain) {
		t.Fatal("loading an existing key changed its identity")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(keyPath); err != nil {
			t.Fatal(err)
		} else if info.Mode().Perm() != 0o600 {
			t.Fatalf("key permissions=%o, want 600", info.Mode().Perm())
		}
	}

	encoded := EncodePublicKey(publicKey)
	decoded, err := DecodePublicKey(encoded)
	if err != nil || string(decoded) != string(publicKey) {
		t.Fatalf("public key round trip failed: err=%v", err)
	}
	if _, err := DecodePublicKey("invalid"); err == nil {
		t.Fatal("DecodePublicKey accepted invalid input")
	}
	if _, err := DecodeSignature(EncodeSignature(make([]byte, ed25519.SignatureSize-1))); err == nil {
		t.Fatal("DecodeSignature accepted an invalid signature length")
	}
}
