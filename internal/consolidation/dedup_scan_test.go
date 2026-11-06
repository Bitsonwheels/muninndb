package consolidation

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/scrypster/muninndb/internal/storage"
)

// TestDedup_PagedScanFindsDuplicateAcrossPages pins the paged vault scan
// (audit finding 8): the vault is walked 500 engrams at a time and the
// embeddings of a page are read in one batch, instead of loading the whole
// vault and doing one point read per engram.
//
// The engrams are written with store.WriteEngram, which stores the embedding
// under the separate 0x18 key (ERF v2) - so the scan has to read it back.
func TestDedup_PagedScanFindsDuplicateAcrossPages(t *testing.T) {
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatal(err)
	}
	// NoSyncEngrams: this test writes 501 engrams, and the fsync per write
	// would dominate the runtime. The scan reads the same data either way.
	store := storage.NewPebbleStore(db, storage.PebbleStoreConfig{CacheSize: 100, NoSyncEngrams: true})
	defer store.Close()

	ctx := context.Background()
	vault := "dedup_paged"
	wsPrefix := store.ResolveVaultPrefix(vault)

	dup := []float32{1, 0, 0, 0}

	// 501 engrams: the first and the last share an embedding and land on
	// different pages of the scan. The 499 in between carry no embedding, so
	// they are scanned but never clustered. Every engram carries the same
	// text, so the separation guard (which compares numbers, dates and
	// negations) does not call the pair distinct facts.
	for i := 0; i < 501; i++ {
		eng := &storage.Engram{
			Concept:    "shared concept",
			Content:    "shared content",
			Confidence: 0.9,
			Relevance:  0.9,
			Stability:  30,
		}
		if i == 0 || i == 500 {
			eng.Embedding = dup
		}
		if _, err := store.WriteEngram(ctx, wsPrefix, eng); err != nil {
			t.Fatal(err)
		}
	}

	mock := &mockEngineInterface{store: store}
	w := &Worker{Engine: mock, MinDedupVaultSize: 2, MaxDedup: 100, MaxTransitive: 100}
	report := &ConsolidationReport{}

	if err := w.runPhase2Dedup(ctx, store, wsPrefix, report, vault); err != nil {
		t.Fatal(err)
	}
	if report.DedupClusters != 1 {
		t.Fatalf("DedupClusters = %d, want 1 (the duplicate pair spans two pages)", report.DedupClusters)
	}
	if report.MergedEngrams != 1 {
		t.Fatalf("MergedEngrams = %d, want 1", report.MergedEngrams)
	}
}

// TestOrient_PagedScanCountsVault checks that the phase 0 summary still sees
// every engram after the scan moved to the shared paged helper.
func TestOrient_PagedScanCountsVault(t *testing.T) {
	store, _, cleanup := testStoreWithDB(t)
	defer cleanup()

	ctx := context.Background()
	vault := "orient_paged"
	wsPrefix := store.ResolveVaultPrefix(vault)

	for i := 0; i < 3; i++ {
		if _, err := store.WriteEngram(ctx, wsPrefix, &storage.Engram{
			Concept:    "concept",
			Content:    "content",
			Confidence: 0.5,
			Relevance:  0.5,
			Stability:  10,
			Embedding:  []float32{1, 0, 0, 0},
		}); err != nil {
			t.Fatal(err)
		}
	}

	mock := &mockEngineInterface{store: store}
	w := &Worker{Engine: mock}

	summary, err := w.runPhase0Orient(ctx, store, wsPrefix, vault)
	if err != nil {
		t.Fatal(err)
	}
	if summary.EngramCount != 3 {
		t.Fatalf("EngramCount = %d, want 3", summary.EngramCount)
	}
	if summary.WithEmbed != 3 {
		t.Fatalf("WithEmbed = %d, want 3", summary.WithEmbed)
	}
}
