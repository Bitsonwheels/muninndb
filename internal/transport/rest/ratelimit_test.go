package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/scrypster/muninndb/internal/httprate"
	"golang.org/x/time/rate"
)

// The rate limiter itself lives in internal/httprate (shared with the MCP
// server). This test pins the REST-specific 429 body.
func TestRateLimit_ResponseBody429(t *testing.T) {
	global := rate.NewLimiter(1, 1)
	handler := httprate.Middleware(global, 1000, rateLimitReject)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Consume the burst.
	r1 := httptest.NewRequest(http.MethodGet, "/", nil)
	r1.RemoteAddr = "10.0.0.1:1234"
	handler.ServeHTTP(httptest.NewRecorder(), r1)

	// Trigger 429.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "10.0.0.1:1234"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)

	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", w2.Code)
	}

	var body APIError
	if err := json.NewDecoder(w2.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if body.Code != "rate_limit_exceeded" {
		t.Fatalf("code: want %q, got %q", "rate_limit_exceeded", body.Code)
	}
	if body.Message != "rate limit exceeded" {
		t.Fatalf("message: want %q, got %q", "rate limit exceeded", body.Message)
	}
}
