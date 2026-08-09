package server

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginFailureLimiter(t *testing.T) {
	limiter := newLoginFailureLimiter()
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	for i := 0; i < loginFailureLimit; i++ {
		if limiter.blocked("203.0.113.10", now) {
			t.Fatalf("blocked before failure limit at attempt %d", i)
		}
		limiter.failure("203.0.113.10", now)
	}
	if !limiter.blocked("203.0.113.10", now) {
		t.Fatal("source was not blocked at failure limit")
	}
	if limiter.blocked("203.0.113.11", now) {
		t.Fatal("independent source was blocked")
	}
	limiter.success("203.0.113.10")
	if limiter.blocked("203.0.113.10", now) {
		t.Fatal("successful login did not clear failures")
	}
	limiter.failure("203.0.113.10", now)
	if limiter.blocked("203.0.113.10", now.Add(loginFailureWindow)) {
		t.Fatal("expired failure window remained blocked")
	}
}

func TestRemoteIPUsesForwardedAddressOnlyBehindLoopback(t *testing.T) {
	req := httptest.NewRequest("GET", "http://hub.example/login", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.20, 127.0.0.1")
	if got := remoteIP(req); got != "203.0.113.20" {
		t.Fatalf("remoteIP behind loopback=%q", got)
	}

	req = httptest.NewRequest("GET", "http://hub.example/login", nil)
	req.RemoteAddr = "198.51.100.30:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.20")
	if got := remoteIP(req); got != "198.51.100.30" {
		t.Fatalf("remoteIP trusted spoofed header from public peer: %q", got)
	}
}

func TestRemoteIPLoopbackProxyHeaderPrecedence(t *testing.T) {
	req := httptest.NewRequest("GET", "http://hub.example/login", nil)
	req.RemoteAddr = "[::1]:12345"
	req.Header.Set("CF-Connecting-IP", "203.0.113.21")
	req.Header.Set("X-Real-IP", "203.0.113.22")
	req.Header.Set("X-Forwarded-For", "203.0.113.23, 127.0.0.1")
	if got := remoteIP(req); got != "203.0.113.21" {
		t.Fatalf("CF-Connecting-IP precedence=%q", got)
	}
	req.Header.Del("CF-Connecting-IP")
	if got := remoteIP(req); got != "203.0.113.22" {
		t.Fatalf("X-Real-IP precedence=%q", got)
	}
	req.Header.Del("X-Real-IP")
	if got := remoteIP(req); got != "203.0.113.23" {
		t.Fatalf("X-Forwarded-For precedence=%q", got)
	}
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := remoteIP(req); got != "::1" {
		t.Fatalf("invalid forwarded address was trusted: %q", got)
	}
}

func TestLoginFailureLimiterBoundsAndPrunesEntries(t *testing.T) {
	limiter := newLoginFailureLimiter()
	now := time.Now().UTC()
	for i := 0; i < loginFailureMaxEntries+128; i++ {
		limiter.failure(fmt.Sprintf("source-%d", i), now)
	}
	if got := len(limiter.entries); got != loginFailureMaxEntries {
		t.Fatalf("limiter entries=%d, want cap %d", got, loginFailureMaxEntries)
	}
	if limiter.blocked("source-0", now) {
		t.Fatal("single failure was incorrectly blocked")
	}
	if got := len(limiter.entries); got != loginFailureMaxEntries {
		t.Fatalf("limiter entry count changed unexpectedly before expiry: %d", got)
	}
	if limiter.blocked("source-0", now.Add(loginFailureWindow)) {
		t.Fatal("expired source remained blocked")
	}
	if got := len(limiter.entries); got != 0 {
		t.Fatalf("expired limiter entries retained: %d", got)
	}
}
