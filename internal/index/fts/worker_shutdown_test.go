package fts

import (
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
)

// TestSubmitReturnsFalseAfterStop proves that Submit does not block for the
// full wait window once the worker has been stopped: it returns false as soon
// as stopCh fires instead of waiting on the timer.
func TestSubmitReturnsFalseAfterStop(t *testing.T) {
	stub := &countingIndex{}
	w := newWorkerWithIndex(stub)

	// Fill the queue so the next Submit enters the wait path.
	for i := 0; i < workerBufSize+1; i++ {
		w.Submit(IndexJob{Concept: "fill"})
	}

	w.Stop()

	start := time.Now()
	if ok := w.Submit(IndexJob{Concept: "after-stop"}); ok {
		t.Fatalf("Submit after Stop should return false")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("Submit blocked for %s after Stop; want an immediate false", elapsed)
	}
}

// TestIndexInlineDoesNotPanicOnClosedStore proves that indexInline (used when
// the queue is full) recovers the panic a closed Pebble database raises
// instead of crashing the calling writer goroutine.
func TestIndexInlineDoesNotPanicOnClosedStore(t *testing.T) {
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	idx := New(db)
	db.Close() // close the store so IndexEngram panics

	w := newWorkerWithIndex(idx)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("indexInline panicked: %v", r)
		}
	}()
	w.indexInline(IndexJob{WS: [8]byte{}, ID: [16]byte{}, Concept: "x", Content: "y"})
}
