package httprate

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"golang.org/x/time/rate"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func reject429(w http.ResponseWriter, scope string, retryAfter int) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	w.WriteHeader(http.StatusTooManyRequests)
}

func TestMiddleware_GlobalLimitRejects(t *testing.T) {
	global := rate.NewLimiter(1, 1) // 1 RPS, burst 1
	handler := Middleware(global, 1000, reject429)(okHandler())

	// First request consumes the burst token.
	r1 := httptest.NewRequest(http.MethodGet, "/", nil)
	r1.RemoteAddr = "10.0.0.1:1234"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, r1)

	if w1.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", w1.Code)
	}

	// Second request should be rejected immediately.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "10.0.0.1:1234"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)

	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: want 429, got %d", w2.Code)
	}
}

func TestMiddleware_GlobalLimitAllows(t *testing.T) {
	global := rate.NewLimiter(1000, 1000)
	handler := Middleware(global, 1000, reject429)(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestMiddleware_PerIPLimitRejects(t *testing.T) {
	global := rate.NewLimiter(10000, 10000)
	// perIPRPS=1 → burst = 1*2 = 2 (set by the middleware: perIPRPS*2)
	handler := Middleware(global, 1, reject429)(okHandler())

	ip := "10.0.0.99:5555"
	// Exhaust the per-IP burst (burst = 2).
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = ip
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d", i+1, w.Code)
		}
	}

	// Next request from the same IP should be rejected.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = ip
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 from per-IP limiter, got %d", w.Code)
	}
}

func TestMiddleware_DifferentIPsIndependent(t *testing.T) {
	global := rate.NewLimiter(10000, 10000)
	handler := Middleware(global, 1, reject429)(okHandler())

	for _, ip := range []string{"10.0.0.1:1000", "10.0.0.2:2000"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = ip
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("IP %s: want 200, got %d", ip, w.Code)
		}
	}
}

func TestMiddleware_RetryAfterHeader(t *testing.T) {
	global := rate.NewLimiter(1, 1)
	handler := Middleware(global, 1000, reject429)(okHandler())

	// Consume the burst.
	r1 := httptest.NewRequest(http.MethodGet, "/", nil)
	r1.RemoteAddr = "10.0.0.1:1234"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, r1)

	// Trigger 429.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "10.0.0.1:1234"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)

	ra := w2.Header().Get("Retry-After")
	if ra == "" {
		t.Fatal("Retry-After header missing on 429 response")
	}
	secs, err := strconv.Atoi(ra)
	if err != nil {
		t.Fatalf("Retry-After is not a valid integer: %q", ra)
	}
	if secs < 1 {
		t.Fatalf("Retry-After should be >= 1, got %d", secs)
	}
}

func TestClientIP_IgnoresXForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.0.2.1:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.50")
	if got := ClientIP(r); got != "192.0.2.1" {
		t.Fatalf("X-Forwarded-For should be ignored; want 192.0.2.1, got %s", got)
	}
}

func TestClientIP_IgnoresXRealIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.0.2.1:54321"
	r.Header.Set("X-Real-IP", "198.51.100.22")
	if got := ClientIP(r); got != "192.0.2.1" {
		t.Fatalf("X-Real-IP should be ignored; want 192.0.2.1, got %s", got)
	}
}

func TestClientIP_RemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.0.2.1:54321"
	if got := ClientIP(r); got != "192.0.2.1" {
		t.Fatalf("want 192.0.2.1, got %s", got)
	}
}

func TestClientIP_AddressForms(t *testing.T) {
	cases := []struct {
		remoteAddr string
		want       string
	}{
		{"192.168.1.1:1234", "192.168.1.1"},
		{"[::1]:8080", "::1"},
		{"127.0.0.1:0", "127.0.0.1"},
		{"no-port", "no-port"},
	}
	for _, tc := range cases {
		r := &http.Request{RemoteAddr: tc.remoteAddr}
		if got := ClientIP(r); got != tc.want {
			t.Errorf("ClientIP(%q) = %q, want %q", tc.remoteAddr, got, tc.want)
		}
	}
}
