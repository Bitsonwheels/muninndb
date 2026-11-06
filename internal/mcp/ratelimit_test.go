package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The MCP HTTP server must not be an open door: it needs the same request
// rate limit and the same server timeouts as the REST server (audit finding 5).

func TestServerHasTimeouts(t *testing.T) {
	srv := New(":0", &fakeEngine{}, "", nil, nil, nil)

	if srv.srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout must be set")
	}
	if srv.srv.WriteTimeout <= 0 {
		t.Error("WriteTimeout must be set")
	}
	if srv.srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout must be set")
	}
	if srv.srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be set")
	}
}

func TestServerRateLimitRejects(t *testing.T) {
	t.Setenv("MUNINN_RATE_LIMIT_GLOBAL_RPS", "1000")
	t.Setenv("MUNINN_RATE_LIMIT_PER_IP_RPS", "1")
	srv := New(":0", &fakeEngine{}, "", nil, nil, nil)

	// The per-IP burst is perIPRPS*2 = 2, so the third request is rejected.
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodGet, "/mcp/health", nil)
		r.RemoteAddr = "192.0.2.7:1234"
		w := httptest.NewRecorder()
		srv.srv.Handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d", i+1, w.Code)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/mcp/health", nil)
	r.RemoteAddr = "192.0.2.7:1234"
	w := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 after the per-IP burst, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing on the 429 response")
	}
}
