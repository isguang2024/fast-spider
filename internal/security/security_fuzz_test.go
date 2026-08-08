package security

import (
	"crypto/ed25519"
	"testing"
)

func FuzzEd25519EncodingDecoders(f *testing.F) {
	for _, seed := range []string{"", "AAAA", EncodePublicKey(make(ed25519.PublicKey, ed25519.PublicKeySize)), EncodeSignature(make([]byte, ed25519.SignatureSize))} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, encoded string) {
		if key, err := DecodePublicKey(encoded); err == nil && len(key) != ed25519.PublicKeySize {
			t.Fatalf("DecodePublicKey accepted length %d", len(key))
		}
		if signature, err := DecodeSignature(encoded); err == nil && len(signature) != ed25519.SignatureSize {
			t.Fatalf("DecodeSignature accepted length %d", len(signature))
		}
	})
}
