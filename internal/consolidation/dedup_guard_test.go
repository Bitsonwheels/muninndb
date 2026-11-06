package consolidation

import (
	"context"
	"testing"

	"github.com/scrypster/muninndb/internal/storage"
)

// TestRunOnce_SmallVault_SkipsDedup pins the small-vault guard on the
// scheduler path. RunOnce used to call phase 2 without the guard, so the
// background worker could dedup a small vault: archiving even one cluster can
// shift the per-query normalization and flip top-1 recall (issue #311).
func TestRunOnce_SmallVault_SkipsDedup(t *testing.T) {
	store, db, cleanup := testStoreWithDB(t)
	defer cleanup()
	ctx := context.Background()

	const vault = "run-once-small-vault"
	wsPrefix := store.ResolveVaultPrefix(vault)

	dup := []float32{1.0, 0.0, 0.0, 0.0}
	dupClose := []float32{0.97, 0.24310, 0.0, 0.0} // cosine ≈ 0.97

	repID := writeEngramWithEmbedding(t, ctx, store, db, wsPrefix, &storage.Engram{
		Concept: "dup-representative", Content: "France's capital is Paris.",
		Confidence: 0.9, Relevance: 0.85, Stability: 30, Embedding: dup,
	})
	memID := writeEngramWithEmbedding(t, ctx, store, db, wsPrefix, &storage.Engram{
		Concept: "dup-member", Content: "Paris is the capital of France.",
		Confidence: 0.5, Relevance: 0.5, Stability: 20, Embedding: dupClose,
	})

	// Pad to 9 engrams — below the default minimum of 20.
	for i := 0; i < 7; i++ {
		embed := make([]float32, 20)
		embed[i+2] = 1.0 // orthogonal unit vector in 20-dim space
		writeEngramWithEmbedding(t, ctx, store, db, wsPrefix, &storage.Engram{
			Concept: "unique", Content: "unique content", Confidence: 0.8, Relevance: 0.7,
			Stability: 25, Embedding: embed,
		})
	}

	mock := &mockEngineInterface{store: store}
	w := NewWorker(mock) // MinDedupVaultSize defaults to 20

	report, err := w.RunOnce(ctx, vault)
	if err != nil {
		t.Fatal(err)
	}
	if report.MergedEngrams != 0 {
		t.Errorf("MergedEngrams = %d, want 0 (a small vault must be left alone)", report.MergedEngrams)
	}

	mem, err := store.GetEngram(ctx, wsPrefix, memID)
	if err != nil {
		t.Fatal(err)
	}
	if mem.State == storage.StateArchived {
		t.Error("member engram was archived — the small-vault guard did not hold")
	}
	rep, err := store.GetEngram(ctx, wsPrefix, repID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.State == storage.StateArchived {
		t.Error("representative engram was archived — the small-vault guard did not hold")
	}
}

// TestRunOnce_SmallVault_DedupRunsWhenConfigured shows the guard is the only
// thing that changed: with a lower minimum the same small vault is deduped.
func TestRunOnce_SmallVault_DedupRunsWhenConfigured(t *testing.T) {
	store, db, cleanup := testStoreWithDB(t)
	defer cleanup()
	ctx := context.Background()

	const vault = "run-once-small-vault-configured"
	wsPrefix := store.ResolveVaultPrefix(vault)

	dup := []float32{1.0, 0.0, 0.0, 0.0}
	dupClose := []float32{0.97, 0.24310, 0.0, 0.0} // cosine ≈ 0.97

	writeEngramWithEmbedding(t, ctx, store, db, wsPrefix, &storage.Engram{
		Concept: "dup-representative", Content: "France's capital is Paris.",
		Confidence: 0.9, Relevance: 0.85, Stability: 30, Embedding: dup,
	})
	memID := writeEngramWithEmbedding(t, ctx, store, db, wsPrefix, &storage.Engram{
		Concept: "dup-member", Content: "Paris is the capital of France.",
		Confidence: 0.5, Relevance: 0.5, Stability: 20, Embedding: dupClose,
	})
	for i := 0; i < 7; i++ {
		embed := make([]float32, 20)
		embed[i+2] = 1.0
		writeEngramWithEmbedding(t, ctx, store, db, wsPrefix, &storage.Engram{
			Concept: "unique", Content: "unique content", Confidence: 0.8, Relevance: 0.7,
			Stability: 25, Embedding: embed,
		})
	}

	mock := &mockEngineInterface{store: store}
	w := NewWorker(mock)
	w.MinDedupVaultSize = 2

	report, err := w.RunOnce(ctx, vault)
	if err != nil {
		t.Fatal(err)
	}
	if report.MergedEngrams != 1 {
		t.Errorf("MergedEngrams = %d, want 1 (dedup must run when the minimum allows it)", report.MergedEngrams)
	}
	mem, err := store.GetEngram(ctx, wsPrefix, memID)
	if err != nil {
		t.Fatal(err)
	}
	if mem.State != storage.StateArchived {
		t.Errorf("member engram state = %v, want StateArchived", mem.State)
	}
}
