package secretscan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

type contentRule struct {
	name    string
	pattern *regexp.Regexp
}

var contentRules = []contentRule{
	{name: "private-key", pattern: regexp.MustCompile(`-----BEGIN[ \t]+(?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`)},
	{name: "putty-private-key", pattern: regexp.MustCompile(`PuTTY-User-Key-File-[23]:`)},
	{name: "github-token", pattern: regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{20,255}|github_pat_[A-Za-z0-9_]{20,255})`)},
	{name: "gitlab-token", pattern: regexp.MustCompile(`(?:glpat|gldt|glrt)-[A-Za-z0-9_-]{20,255}`)},
	{name: "aws-access-key", pattern: regexp.MustCompile(`(?:AKIA|ASIA|A3T[A-Z0-9])[A-Z0-9]{16}`)},
	{name: "slack-token", pattern: regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{20,255}`)},
	{name: "slack-webhook", pattern: regexp.MustCompile(`https://hooks\.slack\.com/services/[A-Za-z0-9_-]{8,}/[A-Za-z0-9_-]{8,}/[A-Za-z0-9_-]{20,}`)},
	{name: "google-api-key", pattern: regexp.MustCompile(`AIza[A-Za-z0-9_-]{35}`)},
	{name: "stripe-secret-key", pattern: regexp.MustCompile(`(?:sk|rk)_live_[A-Za-z0-9]{16,255}`)},
	{name: "npm-token", pattern: regexp.MustCompile(`npm_[A-Za-z0-9]{20,255}`)},
	{name: "huggingface-token", pattern: regexp.MustCompile(`hf_[A-Za-z0-9]{20,255}`)},
	{name: "openai-token", pattern: regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,255}`)},
	{name: "fast-spider-token", pattern: regexp.MustCompile(`(?:bsp|ctk|dev|oat|ort|ses)_[A-Za-z0-9_-]{24,255}`)},
	{name: "jwt", pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{name: "credentialed-uri", pattern: regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]{1,20}://[^/@\s:]{1,128}:[^/@\s]{3,256}@[A-Za-z0-9.-]+(?::[0-9]{1,5})?`)},
}

var (
	secretKeyPattern = `password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|client[_-]?secret|private[_-]?key|aws[_-]?secret[_-]?access[_-]?key|authorization|bearer|cookie|session[_-]?key`
	quotedAssignment = regexp.MustCompile(`(?im)(?:^[\t ]*|[,{][\t ]*)["']?(` + secretKeyPattern + `)["']?\s*(?:=|:)\s*["']([^"'\r\n]{8,512})["']`)
	envAssignment    = regexp.MustCompile(`(?im)^[\t ]*(` + secretKeyPattern + `)[\t ]*=[\t ]*([A-Za-z0-9+/_=.%$@{}:!?~^&*-]{8,512})[\t ]*(?:[#;].*)?$`)
	yamlAssignment   = regexp.MustCompile(`(?im)^[\t ]*(` + secretKeyPattern + `)[\t ]*:[\t ]*([A-Za-z0-9+/_=.%$@{}:!?-]{8,512})[\t ]*(?:#.*)?$`)
	yamlTemplate     = regexp.MustCompile(`(?im)^[\t ]*(` + secretKeyPattern + `)[\t ]*:[\t ]*(\{\{[^#\r\n]{1,500}\}?\}?)[\t ]*(?:#.*)?$`)
	referencePattern = regexp.MustCompile(`^(?:\$\{[A-Za-z_][A-Za-z0-9_]*\}|\$[A-Za-z_][A-Za-z0-9_]*|%[A-Za-z_][A-Za-z0-9_]*%|\{\{[\t ]*(?:env|config|secret|vault)\.[A-Za-z_][A-Za-z0-9_.-]*[\t ]*\}\}|(?:env|config)\.[A-Za-z_][A-Za-z0-9_.-]*|(?:secret|vault)://[A-Za-z0-9_.@/-]+)$`)
)

const lineIndexBlockBytes = 4 << 10

var newline = []byte{'\n'}

// contentLineIndex keeps a compact newline prefix per fixed-size block. It
// bounds indexing memory even for a newline-dense 64 MiB blob while avoiding a
// scan from byte zero for every finding.
type contentLineIndex struct {
	data     []byte
	prefixes []uint32
}

func newContentLineIndex(data []byte) contentLineIndex {
	blocks := (len(data) + lineIndexBlockBytes - 1) / lineIndexBlockBytes
	prefixes := make([]uint32, blocks+1)
	for block := 0; block < blocks; block++ {
		start := block * lineIndexBlockBytes
		end := min(start+lineIndexBlockBytes, len(data))
		prefixes[block+1] = prefixes[block] + uint32(bytes.Count(data[start:end], newline))
	}
	return contentLineIndex{data: data, prefixes: prefixes}
}

func (index contentLineIndex) lineAt(offset int) int {
	if offset <= 0 {
		return 1
	}
	if offset > len(index.data) {
		offset = len(index.data)
	}
	block := offset / lineIndexBlockBytes
	start := block * lineIndexBlockBytes
	return int(index.prefixes[block]) + bytes.Count(index.data[start:offset], newline) + 1
}

// These fingerprints are exact historical test/example placeholders. They are
// value-based and path-independent, so production and test files are treated the
// same. The object IDs in comments make each exception independently auditable.
var testPlaceholderFingerprints = map[string]struct{}{
	"1da7e95ba163e2a04fb0079b15fcaebfeec45f916e108f2b799f5b8cead9e46c": {}, // 877c6b5f... test assignment
	"6f3a0a9b58bbd71717ce8526c64228460f29965d05af9770041a5b2eb77d40e6": {}, // 883354d1... credentialed URL rejection fixture
	"cf9d3237d3352460eb55a40dc8279d11cb1c615a1c984675e194056943e5e02d": {}, // fc124b48... test assignment
}

func (s *scanner) scanContent(loc location, data []byte) error {
	lines := newContentLineIndex(data)
	for _, rule := range contentRules {
		matches, err := s.boundedFindAllIndex(rule.pattern, data)
		if err != nil {
			return err
		}
		for _, match := range matches {
			candidate := data[match[0]:match[1]]
			if isTestPlaceholder(candidate) || (rule.name == "credentialed-uri" && isCredentialURIPlaceholder(candidate)) {
				continue
			}
			if err := s.add(loc, lines.lineAt(match[0]), rule.name); err != nil {
				return err
			}
		}
	}
	for _, marker := range s.markers {
		for offset := 0; offset <= len(data)-len(marker); {
			i := bytes.Index(data[offset:], marker)
			if i < 0 {
				break
			}
			i += offset
			if err := s.consumeMatch(); err != nil {
				return err
			}
			if err := s.add(loc, lines.lineAt(i), "private-marker"); err != nil {
				return err
			}
			offset = i + len(marker)
		}
	}
	if err := s.scanAssignments(loc, data, quotedAssignment, lines); err != nil {
		return err
	}
	if err := s.scanAssignments(loc, data, envAssignment, lines); err != nil {
		return err
	}
	if err := s.scanAssignments(loc, data, yamlAssignment, lines); err != nil {
		return err
	}
	return s.scanAssignments(loc, data, yamlTemplate, lines)
}

func (s *scanner) scanAssignments(loc location, data []byte, pattern *regexp.Regexp, lines contentLineIndex) error {
	matches, err := s.boundedFindAllSubmatchIndex(pattern, data)
	if err != nil {
		return err
	}
	for _, match := range matches {
		if len(match) < 6 || match[4] < 0 || match[5] < 0 {
			continue
		}
		candidate := normalizeAssignmentValue(data[match[4]:match[5]])
		if isTestPlaceholder(candidate) || looksLikeReference(candidate) {
			continue
		}
		rule := "secret-assignment"
		if len(candidate) >= 20 && shannonEntropy(candidate) >= 3.8 {
			rule = "high-entropy-secret-context"
		}
		if err := s.add(loc, lines.lineAt(match[4]), rule); err != nil {
			return err
		}
	}
	return nil
}

func shannonEntropy(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}
	var counts [256]int
	for _, b := range data {
		counts[b]++
	}
	var entropy float64
	length := float64(len(data))
	for _, count := range counts {
		if count == 0 {
			continue
		}
		p := float64(count) / length
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// isTestPlaceholder is the sole content allowlist. It is value-based, bounded,
// and applies equally to production and test paths; no directory or *_test.go is
// exempt from scanning.
func isTestPlaceholder(candidate []byte) bool {
	if len(candidate) == 0 || len(candidate) > 128 {
		return false
	}
	sum := sha256.Sum256(bytes.TrimSpace(candidate))
	if _, ok := testPlaceholderFingerprints[hex.EncodeToString(sum[:])]; ok {
		return true
	}
	value := strings.ToLower(strings.TrimSpace(string(candidate)))
	value = strings.Trim(value, `"'<>`)
	for _, prefix := range []string{
		"sk-", "sk-proj-", "sk-svcacct-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_",
		"github_pat_", "glpat-", "gldt-", "glrt-", "npm_", "hf_", "bsp_", "ctk_",
		"dev_", "oat_", "ort_", "ses_",
	} {
		value = strings.TrimPrefix(value, prefix)
	}
	value = strings.Trim(value, `"'<>`)
	for _, exact := range []string{
		"password", "secret", "example", "test-secret", "test-password", "owner-secret", "owner-password",
		"old-secret", "new-secret", "client-secret", "sample-secret", "not-a-secret", "notasecret",
		"super-secret", "raw-meta-secret", "token-value", "hidden", "do-not-forward", "device_key",
		"dev_artifact_retry", "dev_test", "test-placeholder-value", "your-token-here", "your-secret-here",
		"replace-me", "changeme", "redacted",
	} {
		if value == exact {
			return true
		}
	}
	return false
}

func isCredentialURIPlaceholder(candidate []byte) bool {
	parsed, err := url.Parse(string(candidate))
	if err != nil || parsed.User == nil {
		return false
	}
	password, ok := parsed.User.Password()
	if !ok || parsed.User.Username() != "user" || (password != "pass" && password != "password") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "example.com" || host == "example.test" || strings.HasSuffix(host, ".example") || strings.HasSuffix(host, ".example.test")
}

func looksLikeReference(candidate []byte) bool {
	return referencePattern.Match(bytes.TrimSpace(candidate))
}

func normalizeAssignmentValue(candidate []byte) []byte {
	candidate = bytes.TrimSpace(candidate)
	if len(candidate) >= 2 && ((candidate[0] == '"' && candidate[len(candidate)-1] == '"') || (candidate[0] == '\'' && candidate[len(candidate)-1] == '\'')) {
		candidate = bytes.TrimSpace(candidate[1 : len(candidate)-1])
	}
	return candidate
}

func locatorContainsBuiltInSecret(data []byte) bool {
	for _, rule := range contentRules {
		for _, match := range rule.pattern.FindAllIndex(data, -1) {
			candidate := data[match[0]:match[1]]
			if isTestPlaceholder(candidate) || (rule.name == "credentialed-uri" && isCredentialURIPlaceholder(candidate)) {
				continue
			}
			return true
		}
	}
	for _, pattern := range []*regexp.Regexp{quotedAssignment, envAssignment, yamlAssignment, yamlTemplate} {
		for _, match := range pattern.FindAllSubmatchIndex(data, -1) {
			if len(match) < 6 || match[4] < 0 || match[5] < 0 {
				continue
			}
			candidate := normalizeAssignmentValue(data[match[4]:match[5]])
			if !isTestPlaceholder(candidate) && !looksLikeReference(candidate) {
				return true
			}
		}
	}
	return false
}

func sensitiveFilenameRule(path string) string {
	path = strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(path)
	if base == ".env.example" || base == ".env.sample" || base == ".env.template" {
		return ""
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return "sensitive-filename"
	}
	for _, exact := range []string{
		".env", ".npmrc", ".pypirc", ".netrc", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
		"credentials.json", "service-account.json", "service_account.json", "secrets.json", "secrets.yml",
		"secrets.yaml", "terraform.tfstate",
	} {
		if base == exact {
			return "sensitive-filename"
		}
	}
	for _, extension := range []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".pkcs8"} {
		if strings.HasSuffix(base, extension) {
			return "sensitive-filename"
		}
	}
	for _, suffix := range []string{"/.aws/credentials", "/.docker/config.json", "/.kube/config", "/application_default_credentials.json"} {
		if strings.HasSuffix("/"+path, suffix) {
			return "sensitive-filename"
		}
	}
	return ""
}
