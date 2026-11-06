package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestWebhookSink_RetriesFailedBatch pins that a batch the collector did not
// accept is sent again instead of being dropped. Without the retry the audit
// events of an outage are gone for good.
func TestWebhookSink_RetriesFailedBatch(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	delivered := []AuditEvent{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var events []AuditEvent
		if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
			t.Errorf("decode: %v", err)
		}
		mu.Lock()
		requests++
		fail := requests == 1
		if !fail {
			delivered = append(delivered, events...)
		}
		mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL, 10*time.Millisecond)
	defer sink.Close()

	sink.Write(AuditEvent{Action: "vault.write", Result: "ok"})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(delivered)
		mu.Unlock()
		if got > 0 {
			if delivered[0].Action != "vault.write" {
				t.Fatalf("delivered action = %q, want vault.write", delivered[0].Action)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the event was never delivered after the collector failed once")
}

// TestWebhookSink_PendingBatchStaysCapped pins that a sink whose collector
// stays down does not grow its memory without limit.
func TestWebhookSink_PendingBatchStaysCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL, 10*time.Millisecond)
	defer sink.Close()

	for range maxPendingEvents + 50 {
		sink.Write(AuditEvent{Action: "vault.write", Result: "ok"})
	}

	// Wait for at least one failed delivery, then check the pending list.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		pending := len(sink.batch)
		sink.mu.Unlock()
		if pending > 0 && pending <= maxPendingEvents {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	sink.mu.Lock()
	pending := len(sink.batch)
	sink.mu.Unlock()
	t.Fatalf("pending events = %d after the collector failed, want at most %d (the failed batch must be kept)",
		pending, maxPendingEvents)
}
