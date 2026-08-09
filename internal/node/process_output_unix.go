//go:build !windows

package node

import (
	"strings"
	"unicode/utf8"
)

func normalizeProcessOutput(raw []byte) (string, string) {
	if utf8.Valid(raw) {
		return string(raw), ""
	}
	return strings.ToValidUTF8(string(raw), "�"), "contained invalid UTF-8 and was normalized"
}
