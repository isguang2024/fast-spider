package server

import "testing"

func TestBrowserReadinessMCPParamsAreSessionless(t *testing.T) {
	params := browserControlParams(browserControlInput{Action: "readiness", BrowserSessionID: "brs_must_not_leak"})
	if len(params) != 0 {
		t.Fatalf("readiness MCP params = %#v, want empty", params)
	}
}
