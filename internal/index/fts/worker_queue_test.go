package fts

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingIndex records every job it is asked to index. The first blockFirst
// calls block until release is closed, which keeps the pool workers busy so a
// test can fill the queue.
type blockingIndex struct {
	mu         sync.Mutex
	concepts   []string
	blockFirst int32
	calls      atomic.Int32
	release    chan struct{}
}

func (b *blockingIndex) IndexEngram(ws [8]byte, id [16]byte, concept, createdBy, content string, tags []string) error {
	if b.calls.Add(1) <= b.blockFirst {
		<-b.release
	}
	b.mu.Lock()
	b.concepts = append(b.concepts, concept)
	b.mu.Unlock()
	return nil
}

func (b *blockingIndex) indexed(concept string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.concepts {
		if c == concept {
			return true
		}
	}
	return false
}

// A full queue must not lose the job. The engram is already durable in Pebble
// when the index job is submitted, so a dropped job left it invisible to
// keyword search until someone ran a manual reindex (audit finding 6).
func TestSubmitIndexesInlineWhenQueueIsFull(t *testing.T) {
	workers := int32(runtime.NumCPU())
	stub := &blockingIndex{blockFirst: workers, release: make(chan struct{})}
	w := newWorkerWithIndex(stub)

	// Give every worker a full batch so it calls IndexEngram and blocks there.
	for i := 0; i < int(workers)*workerBatchSize; i++ {
		w.Submit(IndexJob{Concept: "filler"})
	}
	deadline := time.Now().Add(5 * time.Second)
	for stub.calls.Load() < workers && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := stub.calls.Load(); got < workers {
		t.Fatalf("only %d workers reached the indexer, want %d", got, workers)
	}

	// Fill the queue. The workers are blocked, so nothing drains it.
	for i := 0; i < workerBufSize; i++ {
		select {
		case w.input <- IndexJob{Concept: "filler"}:
		default:
		}
	}

	if !w.Submit(IndexJob{Concept: "must-be-indexed"}) {
		t.Fatal("Submit rejected the job instead of indexing it")
	}
	if !stub.indexed("must-be-indexed") {
		t.Error("the job was lost: the queue was full and nothing indexed it")
	}

	close(stub.release)
	w.Stop()
}
