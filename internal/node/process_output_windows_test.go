//go:build windows

package node

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestNormalizeProcessOutputDecodesWSLUTF16LE(t *testing.T) {
	wide := utf16.Encode([]rune("wsl: localhost 网络提示"))
	raw := make([]byte, len(wide)*2)
	for index, value := range wide {
		binary.LittleEndian.PutUint16(raw[index*2:], value)
	}
	text, note := normalizeProcessOutput(raw)
	if text != "wsl: localhost 网络提示" || !strings.Contains(note, "UTF-16LE") {
		t.Fatalf("text=%q note=%q", text, note)
	}
	shifted := append([]byte{0}, raw...)
	text, _ = normalizeProcessOutput(shifted)
	if text != "wsl: localhost 网络提示" {
		t.Fatalf("shifted text=%q", text)
	}
}
