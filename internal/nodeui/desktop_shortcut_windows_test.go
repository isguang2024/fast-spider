//go:build windows

package nodeui

import (
	"strings"
	"testing"
)

func TestDesktopShortcutScriptIsIdempotentAndQuotesExecutable(t *testing.T) {
	script := desktopShortcutScript(`C:\Users\O'Brien\Fast Spider\fast-spider-node.exe`)
	for _, fragment := range []string{
		"Test-Path -LiteralPath $shortcutPath",
		"Fast Spider Node.lnk",
		"$shortcut.Arguments = ''",
		"O''Brien",
		"$shortcut.Save()",
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("shortcut script is missing %q: %s", fragment, script)
		}
	}
}
