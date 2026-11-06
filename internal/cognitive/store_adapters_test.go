package cognitive

import (
	"context"
	"testing"

	"github.com/scrypster/muninndb/internal/storage"
)

// mockEngineStore is a test double that implements storage.EngineStore.
// Only GetMetadata is implemented; all other methods panic to catch unexpected calls.
type mockEngineStore struct {
	storage.EngineStore // embed to satisfy the interface with panics for unimplemented methods
	metadataResult      []*storage.EngramMeta
	metadataErr         error
}

func (m *mockEngineStore) GetMetadata(ctx context.Context, ws [8]byte, ids []storage.ULID) ([]*storage.EngramMeta, error) {
	return m.metadataResult, m.metadataErr
}

func TestNewHebbianStoreAdapterNotNil(t *testing.T) {
	mock := &mockEngineStore{}
	adapter := NewHebbianStoreAdapter(mock)
	if adapter == nil {
		t.Fatal("NewHebbianStoreAdapter returned nil")
	}
}

func TestNewContradictStoreAdapterNotNil(t *testing.T) {
	mock := &mockEngineStore{}
	adapter := NewContradictStoreAdapter(mock)
	if adapter == nil {
		t.Fatal("NewContradictStoreAdapter returned nil")
	}
}

func TestNewConfidenceStoreAdapterNotNil(t *testing.T) {
	mock := &mockEngineStore{}
	adapter := NewConfidenceStoreAdapter(mock)
	if adapter == nil {
		t.Fatal("NewConfidenceStoreAdapter returned nil")
	}
}
