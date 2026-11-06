package rest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scrypster/muninndb/internal/auth"
)

// Body size limits (audit finding 2):
//   - public routes: 64 KB
//   - authenticated routes: 32 MB
//   - import route: 512 MB
//
// http.MaxBytesReader wraps the reader it is given instead of replacing it, so
// two wrappers compound and the smaller limit wins. These tests pin the limit
// per route class: a wrapper that leaks into the authenticated chain silently
// truncates every write at 64 KB.

const bigBodySize = 1 << 20 // 1 MB: above the 64 KB public limit, far below 32 MB

func TestAuthenticatedRouteAcceptsLargeBody(t *testing.T) {
	store := newTestAuthStore(t)
	srv := newTestServer(t, store)

	token, _, err := store.GenerateAPIKey("default", "body-size-test", auth.ModeFull, nil)
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}

	body := []byte(`{"concept":"big","content":"` + strings.Repeat("x", bigBodySize) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/engrams", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("1 MB body on an authenticated route: want 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPublicRouteKeepsSmallBodyLimit(t *testing.T) {
	store := newTestAuthStore(t)
	srv := newTestServer(t, store)

	body := []byte(`{"server_version":"` + strings.Repeat("x", bigBodySize) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/hello", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("1 MB body on a public route: want 400 (64 KB limit), got %d: %s", w.Code, w.Body.String())
	}
}
