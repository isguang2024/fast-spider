package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	if proof == "" || !strings.HasSuffix(proof, "~S") {
		t.Errorf("PoW proof must end with ~S: %q", proof)
	}
	if strings.HasPrefix(proof, "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D") {
		t.Errorf("PoW solver returned an unverified fallback: %q", proof)
	}
}

func TestChatgptSolvePoWSatisfiesDifficulty(t *testing.T) {
	// With a high difficulty target the solver should find a nonce whose hash
	// prefix is <= the target.
	seed := "seed-x"
	difficulty := "ffffff" // extremely easy target => hash prefix 0-6 hex chars
	fingerprint := chatgptRequirementsFingerprint(time.Now())
	proof, err := chatgptSolvePoWContext(context.Background(), fingerprint, seed, difficulty, time.Now())
	if err != nil || !strings.HasSuffix(proof, "~S") {
		t.Fatalf("proof must end with ~S: %q", proof)
	}
	payload := strings.TrimSuffix(proof, "~S")
	hash := chatgptSentinelHash(seed + payload)
	if hash[:len(difficulty)] > difficulty {
		t.Fatalf("proof hash=%s does not satisfy difficulty=%s", hash, difficulty)
	}
}

func TestChatgptSentinelUsesOneFingerprintForRequirementsAndProof(t *testing.T) {
	now := time.UnixMilli(1725000000123)
	fingerprint := chatgptRequirementsFingerprint(now)
	key := chatgptRequirementsKeyForFingerprint(fingerprint)
	keyPayload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(key, "gAAAAAC"))
	if err != nil {
		t.Fatal(err)
	}
	var requirements []any
	if err := json.Unmarshal(keyPayload, &requirements); err != nil {
		t.Fatal(err)
	}
	proof, err := chatgptSolvePoWContext(context.Background(), fingerprint, "seed-consistency", "ffffffff", now)
	if err != nil {
		t.Fatal(err)
	}
	proofPayload, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(proof, "~S"))
	if err != nil {
		t.Fatal(err)
	}
	var proofFingerprint []any
	if err := json.Unmarshal(proofPayload, &proofFingerprint); err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 25 || len(proofFingerprint) != 25 {
		t.Fatalf("fingerprint lengths requirements=%d proof=%d", len(requirements), len(proofFingerprint))
	}
	for i := range requirements {
		if i == 3 || i == 9 {
			continue
		}
		if got, want := proofFingerprint[i], requirements[i]; !sameJSONValue(got, want) {
			t.Fatalf("fingerprint slot %d changed: proof=%#v requirements=%#v", i, got, want)
		}
	}
	if requirements[3] != float64(1) || requirements[9] != float64(0) {
		t.Fatalf("requirements slots nonce/elapsed=%#v/%#v", requirements[3], requirements[9])
	}
}

func TestChatgptSolvePoWHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := chatgptSolvePoWContext(ctx, chatgptRequirementsFingerprint(time.Now()), "seed-cancel", "00000000", time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v, want context.Canceled", err)
	}
}

func TestChatgptSolvePoWRejectsUnsolvedOrInvalidChallengeWithoutFallback(t *testing.T) {
	_, err := chatgptSolvePoWContext(context.Background(), chatgptRequirementsFingerprint(time.Now()), "seed-invalid", "not-hex", time.Now())
	if err == nil || strings.Contains(err.Error(), "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D") {
		t.Fatalf("invalid challenge error=%v", err)
	}
	var capabilityErr interface{ CapabilityError() (string, string, bool) }
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("error=%T does not expose capability classification", err)
	}
	code, message, retryable := capabilityErr.CapabilityError()
	if code != "AGENT_CLOUD_SENTINEL_FAILED" || message == "" || retryable {
		t.Fatalf("classification=%q %q %v", code, message, retryable)
	}
	for _, secret := range []string{"seed-invalid", "not-hex", "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D"} {
		if strings.Contains(message, secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

func TestChatgptSentinelHeadersClassifiesPreparationFailureWithoutSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provider secret challenge=do-not-log", http.StatusBadGateway)
	}))
	defer server.Close()
	_, err := chatgptSentinelHeaders(context.Background(), server.Client(), server.URL, "token-do-not-log")
	if err == nil {
		t.Fatal("expected sentinel preparation failure")
	}
	var capabilityErr interface{ CapabilityError() (string, string, bool) }
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("error=%T does not expose capability classification", err)
	}
	code, message, retryable := capabilityErr.CapabilityError()
	if code != "AGENT_CLOUD_SENTINEL_FAILED" || message != "ChatGPT Cloud Sentinel preparation was rejected" || !retryable {
		t.Fatalf("classification=%q %q %v", code, message, retryable)
	}
	if strings.Contains(err.Error(), "do-not-log") || strings.Contains(err.Error(), "token-do-not-log") {
		t.Fatalf("sentinel error leaked provider data: %v", err)
	}
}

func sameJSONValue(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
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
