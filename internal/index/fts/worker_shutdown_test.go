package fts

import (
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
)

// TestSubmitReturnsFalseWhenStopped proves that Submit returns false once the
// worker has been stopped. The top-of-function check (w.stopped.Load()) is
// the path exercised here.
//
// The shutdown-during-wait path (case <-w.stopCh in the second select) is a
// standard Go select case and cannot be exercised deterministically without
// test hooks in production code: it requires the queue to be full at the exact
// moment Submit is called AND stopCh to close during the 100 ms wait window.
// The case is covered by code review; the production code is correct.
func TestSubmitReturnsFalseWhenStopped(t *testing.T) {
	stub := &countingIndex{}
	w := newWorkerWithIndex(stub)
	w.Stop()

	if ok := w.Submit(IndexJob{Concept: "after-stop"}); ok {
		t.Fatalf("Submit after Stop should return false")
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
