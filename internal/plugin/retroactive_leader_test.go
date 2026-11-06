package plugin

import (
	"context"
	"errors"
	"testing"
)

// A node that may not write must not do the paid work. The gate has to
// short-circuit before the store is touched at all, so the store is rigged to
// fail the count: a pass that reached it would return false.
func TestRetroactiveProcessor_ProcessBatchSkipsWhenNotLeader(t *testing.T) {
	store := &mockPluginStore{countErr: errors.New("store must not be touched")}
	embedPlugin := &mockEmbedPlugin{mockPlugin: mockPlugin{name: "embed-retro", tier: TierEmbed}}
	rp := NewRetroactiveProcessor(store, embedPlugin, DigestEmbed)
	rp.SetLeaderCheck(func() bool { return false })

	if ok := rp.processBatch(context.Background()); !ok {
		t.Error("processBatch should return true when the node may not write")
	}
	if store.setFlagCalls != 0 {
		t.Errorf("no engram may be processed on a follower, got %d SetDigestFlag calls", store.setFlagCalls)
	}
}

// The leader does the work, exactly as before the gate existed.
func TestRetroactiveProcessor_ProcessBatchRunsOnLeader(t *testing.T) {
	eng := &Engram{Concept: "vec", Content: "data"}
	iter := &mockIterator{engrams: []*Engram{eng}}

	store := &mockPluginStore{countResult: 1, scanResult: iter}
	embedPlugin := &mockEmbedPlugin{mockPlugin: mockPlugin{name: "embed-retro", tier: TierEmbed}}
	rp := NewRetroactiveProcessor(store, embedPlugin, DigestEmbed)
	rp.SetLeaderCheck(func() bool { return true })

	if ok := rp.processBatch(context.Background()); !ok {
		t.Error("processBatch should return true on the leader")
	}
	if store.setFlagCalls != 1 {
		t.Errorf("expected 1 SetDigestFlag call on the leader, got %d", store.setFlagCalls)
	}
}
