package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scrypster/muninndb/internal/logging"
	"github.com/scrypster/muninndb/internal/ui"
)

// loginRequest posts a login attempt from the same client IP.
func loginRequest(t *testing.T, srv *ui.Server, password string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"username":"admin","password":"` + password + `"}`
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func newLoginTestServer(t *testing.T) *ui.Server {
	t.Helper()
	as := newTestAuthStore(t)
	if err := as.CreateAdmin("admin", "secret123"); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	sessionSecret := []byte("test-session-secret-32-bytes-ok!")
	srv, err := ui.NewServer(makeMockFS(), &mockEngine{}, http.NotFoundHandler(), as, sessionSecret, logging.NewRingBuffer(10, nil), nil, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

// TestAdminLogin_ThrottlesFailedAttempts proves that repeated wrong passwords
// from one client IP end in 429 instead of an unlimited guessing loop.
func TestAdminLogin_ThrottlesFailedAttempts(t *testing.T) {
	srv := newLoginTestServer(t)

	for i := 1; i <= 10; i++ {
		w := loginRequest(t, srv, "wrong-password")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d: %s", i, w.Code, w.Body.String())
		}
	}

	w := loginRequest(t, srv, "wrong-password")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt 11: want 429, got %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("expected a Retry-After header on the 429 response")
	}
}

// TestAdminLogin_SuccessResetsThrottle proves that a correct password clears
// the failure counter - the CLI re-authenticates on every start and must not
// lock itself out.
func TestAdminLogin_SuccessResetsThrottle(t *testing.T) {
	srv := newLoginTestServer(t)

	for i := 1; i <= 9; i++ {
		loginRequest(t, srv, "wrong-password")
	}
	if w := loginRequest(t, srv, "secret123"); w.Code != http.StatusOK {
		t.Fatalf("correct password: want 200, got %d: %s", w.Code, w.Body.String())
	}
	for i := 1; i <= 9; i++ {
		w := loginRequest(t, srv, "wrong-password")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d after the reset: want 401, got %d: %s", i, w.Code, w.Body.String())
		}
	}
}
