package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	mrand "math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// chatgptCloudUA is the Chrome web-client fingerprint we present (the desktop
// ChatGPTBrowser UA is rejected without the desktop device auth channel).
const chatgptCloudUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.7922.137 Safari/537.36"

const (
	chatgptSentinelPreparePath = "/backend-api/sentinel/chat-requirements/prepare"
	chatgptTurnstileToken      = "ZnMtcGxhY2Vob2xkZXItdHVybnN0aWxl" // base64("fs-placeholder-turnstile"); server accepts any value
	chatgptPowMaxIterations    = 500000
)

type chatgptSentinelChallenge struct {
	Persona      string `json:"persona"`
	PrepareToken string `json:"prepare_token"`
	ProofOfWork  struct {
		Required   bool   `json:"required"`
		Seed       string `json:"seed"`
		Difficulty string `json:"difficulty"`
	} `json:"proofofwork"`
	Turnstile struct {
		Required bool   `json:"required"`
		DX       string `json:"dx"`
	} `json:"turnstile"`
}

// chatgptFingerprint mirrors the official conversation-client jn(): a 25-slot
// client fingerprint. The server does not validate slot content (the PoW is
// self-consistent against it); slots just need to be plausible and stable enough
// to build a matching key + proof.
func chatgptFingerprint(now time.Time) []any {
	scripts := []string{
		"https://chatgpt.com/cdn/assets/app-abc123.js",
		"https://chatgpt.com/cdn/assets/root-def456.js",
	}
	return []any{
		float64(2560 + 1440),                                                               // screen.width + screen.height
		now.Format(time.RFC1123),                                                           // String(new Date)
		nil,                                                                                // performance.memory?.jsHeapSizeLimit ?? null
		mrand.Float64(),                                                                    // Math.random()
		chatgptCloudUA,                                                                     // navigator.userAgent
		scripts[mrand.Intn(len(scripts))],                                                  // J(Array.from(document.scripts).map(src))
		scripts[mrand.Intn(len(scripts))],                                                  // script src matching c/[^/]*/_ or data-build
		"zh-CN",                                                                            // navigator.language
		"zh-CN,zh",                                                                         // navigator.languages.join(',')
		mrand.Float64(),                                                                    // Math.random()
		"userAgent−" + chatgptCloudUA,                                                      // Mn(): random navigator prototype key
		"title,body",                                                                       // J(Object.keys(document))
		"location,innerWidth,innerHeight",                                                  // J(Object.keys(window))
		float64(now.UnixMilli()),                                                           // performance.now()
		chatgptCloudUUID(),                                                                 // x() = uuid v4
		"",                                                                                 // [...URLSearchParams(location.search).keys()].join(',')
		float64(16),                                                                        // navigator.hardwareConcurrency
		float64(now.UnixMilli() * 1000),                                                    // performance.timeOrigin
		float64(0), float64(0), float64(0), float64(0), float64(0), float64(0), float64(0), // presence probes
	}
}

// chatgptSentinelY mirrors Y(): btoa(UTF8(JSON.stringify(v))).
func chatgptSentinelY(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// chatgptSentinelHash mirrors An(): FNV-1a 32-bit + MurmurHash3 fmix32.
func chatgptSentinelHash(input string) string {
	h := uint32(2166136261)
	for i := 0; i < len(input); i++ {
		h ^= uint32(input[i])
		h *= 16777619
	}
	h ^= h >> 16
	h *= 2246822507
	h ^= h >> 13
	h *= 3266489909
	h ^= h >> 16
	return fmt.Sprintf("%08x", h)
}

// chatgptRequirementsKey mirrors Dn(): gAAAAAC + base64(fingerprint with slot[3]=1).
func chatgptRequirementsKey(now time.Time) string {
	fp := chatgptFingerprint(now)
	fp[3] = float64(1)
	fp[9] = float64(0)
	return "gAAAAAC" + chatgptSentinelY(fp)
}

// chatgptSolvePoW mirrors En/kn(): find nonce where FNV(seed+payload) prefix <= difficulty.
func chatgptSolvePoW(seed, difficulty string, now time.Time) string {
	start := now
	for i := 0; i < chatgptPowMaxIterations; i++ {
		fp := chatgptFingerprint(now)
		fp[3] = float64(i)
		fp[9] = float64(time.Since(start).Milliseconds())
		payload := chatgptSentinelY(fp)
		hash := chatgptSentinelHash(seed + payload)
		if len(difficulty) <= len(hash) && hash[:len(difficulty)] <= difficulty {
			return payload + "~S"
		}
	}
	fallback := chatgptSentinelY("e")
	return "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D" + fallback
}

// chatgptSentinelHeaders solves the Sentinel challenge for a token and returns the
// three request headers required by POST /backend-api/f/conversation.
func chatgptSentinelHeaders(ctx context.Context, client *http.Client, baseURL, token string) (map[string]string, error) {
	key := chatgptRequirementsKey(time.Now())
	body := strings.NewReader(`{"p":` + strconvQuote(key) + `}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+chatgptSentinelPreparePath, body)
	if err != nil {
		return nil, err
	}
	chatgptApplyCloudHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sentinel prepare returned %s", resp.Status)
	}
	var challenge chatgptSentinelChallenge
	if err := json.NewDecoder(resp.Body).Decode(&challenge); err != nil {
		return nil, fmt.Errorf("decode sentinel challenge: %w", err)
	}
	if !challenge.ProofOfWork.Required || challenge.ProofOfWork.Seed == "" || challenge.ProofOfWork.Difficulty == "" {
		return nil, fmt.Errorf("sentinel did not return a proof of work challenge")
	}
	proof := "gAAAAAB" + chatgptSolvePoW(challenge.ProofOfWork.Seed, challenge.ProofOfWork.Difficulty, time.Now())
	return map[string]string{
		"OpenAI-Sentinel-Chat-Requirements-Prepare-Token": challenge.PrepareToken,
		"OpenAI-Sentinel-Proof-Token":                     proof,
		"OpenAI-Sentinel-Turnstile-Token":                 chatgptTurnstileToken,
	}, nil
}

// chatgptApplyCloudHeaders applies the web-client fingerprint headers to a request.
func chatgptApplyCloudHeaders(req *http.Request, token string) {
	req.Header.Set("User-Agent", chatgptCloudUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("OAI-Language", "en")
	req.Header.Set("oai-did", chatgptCloudDeviceID())
	req.Header.Set("sec-ch-ua", `"Chromium";v="151", "Google Chrome";v="151", "Not/A)Brand";v="24"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Referer", "https://chatgpt.com/")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

var chatgptCloudDeviceOnce sync.Once
var chatgptCloudDeviceIDValue string

func chatgptCloudDeviceID() string {
	chatgptCloudDeviceOnce.Do(func() {
		// A per-process stable device id (like a real client's localStorage device-id).
		chatgptCloudDeviceIDValue = chatgptCloudUUID()
	})
	return chatgptCloudDeviceIDValue
}

func chatgptCloudUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func strconvQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
