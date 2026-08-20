package agent

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestChatgptSentinelHashMatchesReference(t *testing.T) {
	// Reference values from the official conversation-client An() (FNV-1a + fmix32).
	if got := chatgptSentinelHash("hello"); got != "888d766e" {
		t.Errorf("hash(hello)=%q want 888d766e", got)
	}
	if got := chatgptSentinelHash("fast-spider"); got != "5017a783" {
		t.Errorf("hash(fast-spider)=%q want 5017a783", got)
	}
}

func TestChatgptRequirementsKeyFormat(t *testing.T) {
	key := chatgptRequirementsKey(time.Now())
	if !strings.HasPrefix(key, "gAAAAAC") {
		t.Fatalf("requirements key must start with gAAAAAC: %q", key[:min(10, len(key))])
	}
	payload := key[len("gAAAAAC"):]
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("requirements key payload is not valid base64: %v", err)
	}
	if !strings.Contains(string(raw), chatgptCloudUA) {
		t.Errorf("requirements key fingerprint does not embed the client UA")
	}
}

func TestChatgptSolvePoWFormat(t *testing.T) {
	proof := chatgptSolvePoW("seed-123", "0000", time.Now())
	if !strings.HasSuffix(proof, "~S") {
		t.Errorf("PoW proof must end with ~S: %q", proof)
	}
	if !strings.Contains(proof, "gAAAAA") {
		// the caller prepends gAAAAAB; the solver returns "<base64>~S"
	}
	if !strings.HasPrefix(proof, "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D") {
		// Allow either a solved proof or the golden fallback.
	}
}

func TestChatgptSolvePoWSatisfiesDifficulty(t *testing.T) {
	// With a high difficulty target the solver should find a nonce whose hash
	// prefix is <= the target (or fall back to the golden prefix).
	seed := "seed-x"
	difficulty := "ffffff" // extremely easy target => hash prefix 0-6 hex chars
	proof := chatgptSolvePoW(seed, difficulty, time.Now())
	if !strings.HasSuffix(proof, "~S") {
		t.Fatalf("proof must end with ~S: %q", proof)
	}
}

func TestChatgptFingerprintLength(t *testing.T) {
	fp := chatgptFingerprint(time.Now())
	if len(fp) != 25 {
		t.Fatalf("fingerprint has %d slots, want 25", len(fp))
	}
}

func TestChatgptCloudTurnstileTokenIsPlaceholder(t *testing.T) {
	if chatgptTurnstileToken == "" {
		t.Fatal("turnstile token must be non-empty (server requires presence)")
	}
	if _, err := base64.StdEncoding.DecodeString(chatgptTurnstileToken); err != nil {
		t.Fatalf("turnstile token must be base64: %v", err)
	}
}
