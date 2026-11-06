package ui

import (
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	// maxLoginFailures is how many wrong passwords one client IP may submit
	// inside loginFailureWindow before the login endpoint answers 429.
	maxLoginFailures = 10
	// loginFailureWindow is how long failed attempts are remembered.
	loginFailureWindow = 5 * time.Minute
)

// loginThrottle slows down password guessing on the admin login endpoint.
// Only failed attempts count: a successful login clears the counter, so a
// client that knows the password (the CLI re-authenticates on every start)
// is never locked out.
type loginThrottle struct {
	mu       sync.Mutex
	failures *lru.Cache[string, []time.Time]
}

func newLoginThrottle() *loginThrottle {
	cache, err := lru.New[string, []time.Time](1024)
	if err != nil {
		// Only possible with a non-positive size.
		panic(err)
	}
	return &loginThrottle{failures: cache}
}

// blocked reports whether the client IP is over the limit and how long it has
// to wait. Attempts older than loginFailureWindow are ignored.
func (t *loginThrottle) blocked(ip string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	stored, _ := t.failures.Get(ip)
	attempts := recentAttempts(stored, now)
	if len(attempts) < maxLoginFailures {
		return false, 0
	}
	return true, attempts[0].Add(loginFailureWindow).Sub(now)
}

// recordFailure remembers one wrong password for the client IP.
func (t *loginThrottle) recordFailure(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	stored, _ := t.failures.Get(ip)
	t.failures.Add(ip, append(recentAttempts(stored, now), now))
}

// recordSuccess clears the failure counter for the client IP.
func (t *loginThrottle) recordSuccess(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failures.Remove(ip)
}

// recentAttempts drops the attempts that fell out of the window.
func recentAttempts(attempts []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-loginFailureWindow)
	kept := make([]time.Time, 0, len(attempts))
	for _, a := range attempts {
		if a.After(cutoff) {
			kept = append(kept, a)
		}
	}
	return kept
}
