package main

import "testing"

func TestNormalizePublicBaseURLWithPath(t *testing.T) {
	got, err := normalizePublicBaseURL(" https://hub.example/fast-spider/ ")
	if err != nil {
		t.Fatalf("normalizePublicBaseURL() error=%v", err)
	}
	if got != "https://hub.example/fast-spider" {
		t.Fatalf("normalizePublicBaseURL()=%q", got)
	}
}

func TestNormalizePublicBaseURLAllowsLoopbackHTTP(t *testing.T) {
	for _, raw := range []string{
		"http://localhost:8787/fast-spider/",
		"http://127.0.0.1:8787/fast-spider/",
		"http://[::1]:8787/fast-spider/",
	} {
		t.Run(raw, func(t *testing.T) {
			if got, err := normalizePublicBaseURL(raw); err != nil || got == "" {
				t.Fatalf("normalizePublicBaseURL(%q)=(%q,%v), want loopback HTTP accepted", raw, got, err)
			}
		})
	}
}

func TestNormalizePublicBaseURLRejectsUnsafeForms(t *testing.T) {
	for _, raw := range []string{
		"hub.example/fast-spider",
		"ftp://hub.example/fast-spider",
		"http://hub.example/fast-spider",
		"https://user:pass@hub.example/fast-spider",
		"https://hub.example/fast-spider?debug=1",
		"https://hub.example/fast-spider#fragment",
	} {
		t.Run(raw, func(t *testing.T) {
			if got, err := normalizePublicBaseURL(raw); err == nil || got != "" {
				t.Fatalf("normalizePublicBaseURL(%q)=(%q,%v), want rejection", raw, got, err)
			}
		})
	}
}
