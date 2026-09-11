package nodeui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/isguang2024/fast-spider/hostapi"
)

type extensionTestAgent struct{}

func (extensionTestAgent) Control(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}
func (extensionTestAgent) Close(context.Context) error { return nil }

type extensionTestSurface struct{ called bool }

func (s *extensionTestSurface) RegisterUI(mux *http.ServeMux, ctx hostapi.UISurfaceContext) error {
	s.called = true
	mux.HandleFunc("GET /extension-test", ctx.APIOnly(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	return nil
}

func TestUISurfaceRegistersInsideExistingGuardedServer(t *testing.T) {
	surface := &extensionTestSurface{}
	app, err := New(Options{DataDir: t.TempDir(), Version: "test", Agent: extensionTestAgent{}, AgentCallerOwned: true, UISurfaces: []hostapi.UISurface{surface}})
	if err != nil {
		t.Fatal(err)
	}
	handler := app.handler()
	if !surface.called {
		t.Fatal("expected UI extension registration")
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/extension-test", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/extension-test", nil)
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("authorized status=%d", authorized.Code)
	}
}
