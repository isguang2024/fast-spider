package secretscan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// WriteFindings emits only source, path/object, line, and rule metadata.
func WriteFindings(w io.Writer, findings []Finding) error {
	for _, finding := range findings {
		locator := "path=" + fmt.Sprintf("%q", safeLocatorForced(finding.Path, false))
		if finding.ObjectID != "" && finding.Path == "" {
			locator = "object=" + safeObjectID(finding.ObjectID)
		} else if finding.ObjectID != "" {
			locator += " object=" + safeObjectID(finding.ObjectID)
		}
		if _, err := fmt.Fprintf(w, "source=%s %s line=%d rule=%s\n", safeWord(finding.Source), locator, finding.Line, safeWord(finding.Rule)); err != nil {
			return err
		}
	}
	return nil
}

func (s *scanner) safeLocation(loc location) string {
	if loc.path != "" {
		return s.safeLocator(loc.path)
	}
	return safeObjectID(loc.objectID)
}

func (s *scanner) safeLocator(value string) string {
	return safeLocatorForced(value, s.locatorContainsMarker(value))
}

func (s *scanner) locatorContainsMarker(value string) bool {
	for _, marker := range s.markers {
		if len(marker) != 0 && strings.Contains(value, string(marker)) {
			return true
		}
	}
	return false
}

func safeLocatorForced(value string, force bool) string {
	if force || locatorContainsBuiltInSecret([]byte(value)) {
		hash := sha256.Sum256([]byte(value))
		return "<redacted-path:" + hex.EncodeToString(hash[:6]) + ">"
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, value)
}

func safeObjectID(value string) string {
	if len(value) != 40 && len(value) != 64 {
		return "<invalid-object>"
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return "<invalid-object>"
		}
	}
	return strings.ToLower(value)
}

func safeWord(value string) string {
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return "invalid"
		}
	}
	return value
}
