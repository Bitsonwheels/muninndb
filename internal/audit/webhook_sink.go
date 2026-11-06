package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// maxPendingEvents bounds how many events a sink keeps while its collector is
// unreachable.
// ponytail: the pending list is capped at 10k events and the oldest are
// dropped first. Upgrade path: spill the pending batch to disk.
const maxPendingEvents = 10000

// WebhookSink batches events and POSTs them as a JSON array to a URL.
// Compatible with Splunk HEC, Datadog Logs, Loki (with a vector adapter), etc.
type WebhookSink struct {
	url           string
	client        *http.Client
	flushInterval time.Duration

	mu     sync.Mutex
	batch  []AuditEvent
	ticker *time.Ticker
	done   chan struct{}
	wg     sync.WaitGroup
}

// NewWebhookSink creates a WebhookSink that flushes at most every flushInterval.
// A flushInterval of 0 uses a default of 5 seconds.
func NewWebhookSink(url string, flushInterval time.Duration) *WebhookSink {
	if flushInterval <= 0 {
		flushInterval = 5 * time.Second
	}
	s := &WebhookSink{
		url:           url,
		client:        &http.Client{Timeout: 10 * time.Second},
		flushInterval: flushInterval,
		done:          make(chan struct{}),
	}
	s.ticker = time.NewTicker(flushInterval)
	s.wg.Add(1)
	go s.run()
	return s
}

func (s *WebhookSink) Write(e AuditEvent) error {
	s.mu.Lock()
	s.batch = append(s.batch, e)
	// Cap the pending list here too: requeue only caps after a failed flush,
	// so under sustained outage + high write rate the list could grow
	// unbounded between two flush cycles (5s). Drop the oldest, matching
	// requeue's policy.
	if len(s.batch) > maxPendingEvents {
		dropped := len(s.batch) - maxPendingEvents
		s.batch = s.batch[dropped:]
		slog.Warn("audit: webhook: pending events dropped",
			"dropped", dropped, "limit", maxPendingEvents)
	}
	s.mu.Unlock()
	return nil
}

func (s *WebhookSink) Close() error {
	s.ticker.Stop()
	close(s.done)
	s.wg.Wait()
	if err := s.flush(); err != nil {
		slog.Error("audit: webhook: final flush failed, events not delivered", "url", s.url, "err", err)
		return err
	}
	return nil
}

func (s *WebhookSink) run() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ticker.C:
			_ = s.flush()
		case <-s.done:
			return
		}
	}
}

// flush sends the pending batch. A batch that the collector did not accept is
// put back in front of the pending events, so the next flush sends it again:
// dropping it would lose exactly the events an outage produces.
func (s *WebhookSink) flush() error {
	s.mu.Lock()
	if len(s.batch) == 0 {
		s.mu.Unlock()
		return nil
	}
	toSend := s.batch
	s.batch = nil
	s.mu.Unlock()

	b, err := json.Marshal(toSend)
	if err != nil {
		// The event types hold plain data only, so this cannot happen in
		// practice. Keeping the batch would retry a poison batch forever.
		slog.Error("audit: webhook: marshal failed, batch dropped", "err", err, "events", len(toSend))
		return err
	}
	resp, err := s.client.Post(s.url, "application/json", bytes.NewReader(b))
	if err != nil {
		slog.Warn("audit: webhook: post failed, batch kept for the next try",
			"url", s.url, "err", err, "events", len(toSend))
		s.requeue(toSend)
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		// The collector is broken, not the batch: send it again.
		slog.Warn("audit: webhook: server error, batch kept for the next try",
			"url", s.url, "status", resp.StatusCode, "events", len(toSend))
		s.requeue(toSend)
		return fmt.Errorf("audit: webhook: server error: status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		// The collector rejected the batch. Sending it again would fail the
		// same way, so it is dropped - loudly.
		slog.Error("audit: webhook: batch rejected, dropped",
			"url", s.url, "status", resp.StatusCode, "events", len(toSend))
		return fmt.Errorf("audit: webhook: batch rejected: status %d", resp.StatusCode)
	}
	return nil
}

// requeue puts a failed batch back in front of the pending events so the next
// flush sends it again. The list stays capped: during a long outage the
// oldest events are dropped instead of letting the memory grow without limit.
func (s *WebhookSink) requeue(batch []AuditEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	merged := make([]AuditEvent, 0, len(batch)+len(s.batch))
	merged = append(merged, batch...)
	merged = append(merged, s.batch...)
	if len(merged) > maxPendingEvents {
		dropped := len(merged) - maxPendingEvents
		merged = merged[dropped:]
		slog.Warn("audit: webhook: pending events dropped", "dropped", dropped, "limit", maxPendingEvents)
	}
	s.batch = merged
}
