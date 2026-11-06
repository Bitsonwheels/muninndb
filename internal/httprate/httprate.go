// Package httprate provides the shared HTTP request rate limiter used by the
// REST and MCP servers: one implementation, one client-identity rule, one set
// of environment variables.
package httprate

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/time/rate"
)

const (
	// DefaultGlobalRPS is the total request rate per server.
	DefaultGlobalRPS = 1000
	// DefaultPerIPRPS is the request rate per client IP.
	DefaultPerIPRPS = 100
	// ipCacheSize bounds the number of tracked client IPs.
	ipCacheSize = 50_000
)

// ClientIP returns the direct TCP peer IP from r.RemoteAddr with the port
// stripped. Proxy headers (X-Forwarded-For, X-Real-IP) are intentionally
// ignored because they are attacker-controlled without a trusted proxy list.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RejectFunc writes the 429 response. Each protocol has its own error format,
// so the caller decides what the body looks like. scope is "global" or "per_ip".
type RejectFunc func(w http.ResponseWriter, scope string, retryAfter int)

// Middleware enforces a global and a per-IP token-bucket limit. Requests that
// exceed either limit are rejected and never reach the handler.
func Middleware(global *rate.Limiter, perIPRPS int, reject RejectFunc) func(http.Handler) http.Handler {
	ipCache, _ := lru.New[string, *rate.Limiter](ipCacheSize)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Global limiter first.
			if !global.Allow() {
				reject(w, "global", retryAfter(global))
				return
			}

			ip := ClientIP(r)
			limiter, ok := ipCache.Get(ip)
			if !ok {
				limiter = rate.NewLimiter(rate.Limit(perIPRPS), perIPRPS*2)
				ipCache.Add(ip, limiter)
			}
			if !limiter.Allow() {
				reject(w, "per_ip", retryAfter(limiter))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// retryAfter returns the number of seconds until the limiter allows again.
func retryAfter(l *rate.Limiter) int {
	res := l.Reserve()
	delay := res.Delay()
	res.Cancel()
	return int(delay.Seconds()) + 1
}

// FromEnv builds the middleware from MUNINN_RATE_LIMIT_GLOBAL_RPS and
// MUNINN_RATE_LIMIT_PER_IP_RPS. Invalid values fall back to the defaults.
func FromEnv(reject RejectFunc) func(http.Handler) http.Handler {
	globalRPS := envIntDefault("MUNINN_RATE_LIMIT_GLOBAL_RPS", DefaultGlobalRPS)
	perIPRPS := envIntDefault("MUNINN_RATE_LIMIT_PER_IP_RPS", DefaultPerIPRPS)
	global := rate.NewLimiter(rate.Limit(globalRPS), globalRPS*2)
	return Middleware(global, perIPRPS, reject)
}

// envIntDefault reads an integer environment variable by key. If the variable
// is unset or invalid, the default is returned and a warning is logged.
func envIntDefault(key string, def int) int {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 || v > 100000 {
		slog.Warn("invalid env var value, using default", "key", key, "value", s, "default", def)
		return def
	}
	return v
}
