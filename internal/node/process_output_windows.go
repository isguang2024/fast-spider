//go:build windows

package node

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

var getOEMCPProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")

func normalizeProcessOutput(raw []byte) (string, string) {
	if text, ok := decodeLikelyUTF16LE(raw); ok {
		return text, "used UTF-16LE and was converted to UTF-8"
	}
	if utf8.Valid(raw) {
		return string(raw), ""
	}
	for _, codePage := range windowsProcessOutputCodePages() {
		if codePage == 0 || codePage == 65001 {
			continue
		}
		if text, err := decodeWindowsCodePage(raw, codePage); err == nil {
			return text, fmt.Sprintf("used Windows code page %d and was converted to UTF-8", codePage)
		}
	}
	return strings.ToValidUTF8(string(raw), "�"), "contained invalid UTF-8 and could not be decoded using the Windows code page"
}

func decodeLikelyUTF16LE(raw []byte) (string, bool) {
	if len(raw) < 4 {
		return "", false
	}
	evenNUL, oddNUL := 0, 0
	for index, value := range raw {
		if value != 0 {
			continue
		}
		if index%2 == 0 {
			evenNUL++
		} else {
			oddNUL++
		}
	}
	start := 0
	dominant, other := oddNUL, evenNUL
	if evenNUL > oddNUL {
		start, dominant, other = 1, evenNUL, oddNUL
	}
	if dominant < 2 || dominant*4 < len(raw) || dominant <= other*2 {
		return "", false
	}
	usable := raw[start:]
	if len(usable)%2 != 0 {
		usable = usable[:len(usable)-1]
	}
	if len(usable) == 0 {
		return "", false
	}
	wide := make([]uint16, len(usable)/2)
	for index := range wide {
		wide[index] = binary.LittleEndian.Uint16(usable[index*2:])
	}
	return strings.TrimPrefix(string(utf16.Decode(wide)), "\x00"), true
}

func windowsProcessOutputCodePages() []uint32 {
	values := make([]uint32, 0, 3)
	if codePage, err := windows.GetConsoleOutputCP(); err == nil && codePage != 0 {
		values = appendUniqueCodePage(values, codePage)
	}
	if codePage, _, _ := getOEMCPProc.Call(); codePage != 0 {
		values = appendUniqueCodePage(values, uint32(codePage))
	}
	if codePage := windows.GetACP(); codePage != 0 {
		values = appendUniqueCodePage(values, codePage)
	}
	return values
}

func appendUniqueCodePage(values []uint32, value uint32) []uint32 {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func decodeWindowsCodePage(raw []byte, codePage uint32) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	needed, err := windows.MultiByteToWideChar(codePage, 0, &raw[0], int32(len(raw)), nil, 0)
	if err != nil || needed <= 0 {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("Windows code page %d returned no decoded text", codePage)
	}
	wide := make([]uint16, needed)
	written, err := windows.MultiByteToWideChar(codePage, 0, &raw[0], int32(len(raw)), &wide[0], needed)
	if err != nil {
		return "", err
	}
	return string(utf16.Decode(wide[:written])), nil
}
