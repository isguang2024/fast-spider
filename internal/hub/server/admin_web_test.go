package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
)

func TestAdminCanCreateUserAndIsolatedFromOwnerSession(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "admin-web-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureAdminAccount(ctx); err != nil {
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()

	adminClient := newWebTestClient(t)
	status, headers, _ := webTestRequest(t, adminClient, http.MethodGet, httpServer.URL+"/admin", nil)
	if status != http.StatusSeeOther || !strings.HasSuffix(headers.Get("Location"), "/admin/login") {
		t.Fatalf("unauthenticated admin status=%d location=%q", status, headers.Get("Location"))
	}
	status, headers, _ = webTestRequest(t, adminClient, http.MethodPost, httpServer.URL+"/admin/login", url.Values{
		"username": {core.DefaultAdminUsername}, "password": {core.DefaultAdminPassword},
	})
	if status != http.StatusSeeOther || !strings.HasSuffix(headers.Get("Location"), "/admin") {
		t.Fatalf("admin login status=%d location=%q", status, headers.Get("Location"))
	}
	status, _, body := webTestRequest(t, adminClient, http.MethodGet, httpServer.URL+"/admin", nil)
	if status != http.StatusOK || !strings.Contains(string(body), "创建用户") {
		t.Fatalf("admin page status=%d body=%s", status, body)
	}
	if strings.Contains(string(body), "alice") {
		t.Fatalf("new admin page unexpectedly contains user before creation: %s", body)
	}
	csrf := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(string(body))
	if len(csrf) != 2 {
		t.Fatalf("admin CSRF token missing: %s", body)
	}
	status, headers, _ = webTestRequest(t, adminClient, http.MethodPost, httpServer.URL+"/admin/users", url.Values{
		"csrf_token": {csrf[1]}, "username": {"alice"}, "display_name": {"Alice"},
		"password": {"alice-password-123"}, "password_confirm": {"alice-password-123"},
	})
	if status != http.StatusSeeOther || !strings.Contains(headers.Get("Location"), "user-created") {
		t.Fatalf("user creation status=%d location=%q", status, headers.Get("Location"))
	}
	status, _, body = webTestRequest(t, adminClient, http.MethodGet, httpServer.URL+"/admin", nil)
	if status != http.StatusOK || !strings.Contains(string(body), "alice") || !strings.Contains(string(body), "Alice") {
		t.Fatalf("created user missing from admin list status=%d body=%s", status, body)
	}

	ownerClient := newWebTestClient(t)
	status, headers, _ = webTestRequest(t, ownerClient, http.MethodPost, httpServer.URL+"/login", url.Values{
		"username": {"alice"}, "password": {"alice-password-123"}, "return_to": {httpServer.URL + "/app"},
	})
	if status != http.StatusSeeOther || !strings.HasSuffix(headers.Get("Location"), "/app") {
		t.Fatalf("created user login status=%d location=%q", status, headers.Get("Location"))
	}
	status, headers, _ = webTestRequest(t, ownerClient, http.MethodGet, httpServer.URL+"/admin", nil)
	if status != http.StatusSeeOther || !strings.HasSuffix(headers.Get("Location"), "/admin/login") {
		t.Fatalf("owner session accessed admin status=%d location=%q", status, headers.Get("Location"))
	}
	status, headers, _ = webTestRequest(t, adminClient, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusSeeOther || !strings.Contains(headers.Get("Location"), "/login?return_to=") {
		t.Fatalf("admin session accessed owner app status=%d location=%q", status, headers.Get("Location"))
	}
}
