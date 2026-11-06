package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/scrypster/muninndb/internal/auth"
	"github.com/scrypster/muninndb/internal/storage"
)

// consolidationMockEngine implements both EngineAPI (via embedding MockEngine) and
// consolidation.EngineInterface so the type assertion in handleConsolidate succeeds.
type consolidationMockEngine struct {
	MockEngine
	store *storage.PebbleStore
}

// Store implements consolidation.EngineInterface.
func (m *consolidationMockEngine) Store() *storage.PebbleStore {
	return m.store
}

// ListVaults implements consolidation.EngineInterface (overrides MockEngine.ListVaults).
func (m *consolidationMockEngine) ListVaults(ctx context.Context) ([]string, error) {
	names, err := m.store.ListVaultNames()
	if err != nil {
		return nil, err
	}
	return names, nil
}

// UpdateLifecycleState implements consolidation.EngineInterface.
func (m *consolidationMockEngine) UpdateLifecycleState(ctx context.Context, vault, id, state string) error {
	ulid, err := storage.ParseULID(id)
	if err != nil {
		return err
	}
	wsPrefix := m.store.ResolveVaultPrefix(vault)
	eng, err := m.store.GetEngram(ctx, wsPrefix, ulid)
	if err != nil {
		return err
	}
	newState, err := storage.ParseLifecycleState(state)
	if err != nil {
		return err
	}
	meta := &storage.EngramMeta{
		State:       newState,
		Confidence:  eng.Confidence,
		Relevance:   eng.Relevance,
		Stability:   eng.Stability,
		AccessCount: eng.AccessCount,
		UpdatedAt:   time.Now(),
		LastAccess:  eng.LastAccess,
	}
	return m.store.UpdateMetadata(ctx, wsPrefix, ulid, meta)
}

// newConsolidationTestEngine creates a consolidationMockEngine backed by an in-memory pebble DB.
func newConsolidationTestEngine(t *testing.T) *consolidationMockEngine {
	t.Helper()
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := storage.NewPebbleStore(db, storage.PebbleStoreConfig{CacheSize: 64})
	return &consolidationMockEngine{store: store}
}

// TestHandleConsolidate_Success posts a valid consolidation request and expects HTTP 200.
func TestHandleConsolidate_Success(t *testing.T) {
	eng := newConsolidationTestEngine(t)
	srv := NewServer("localhost:0", eng, nil, nil, nil, EmbedInfo{}, EnrichInfo{}, nil, "", nil)

	handler := srv.handleConsolidate()

	req := httptest.NewRequest("POST", "/v1/vaults/default/consolidate", strings.NewReader(`{}`))
	req.SetPathValue("vault", "default")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleConsolidate_MissingVault expects HTTP 400 when the vault path value is empty.
func TestHandleConsolidate_MissingVault(t *testing.T) {
	eng := newConsolidationTestEngine(t)
	srv := NewServer("localhost:0", eng, nil, nil, nil, EmbedInfo{}, EnrichInfo{}, nil, "", nil)

	handler := srv.handleConsolidate()

	// No vault path value set — PathValue("vault") returns "".
	req := httptest.NewRequest("POST", "/v1/vaults//consolidate", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing vault, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleConsolidate_UnknownVault verifies the handler returns 200 for an unknown vault
// (the consolidation worker performs a best-effort run on any vault name).
func TestHandleConsolidate_UnknownVault(t *testing.T) {
	eng := newConsolidationTestEngine(t)
	srv := NewServer("localhost:0", eng, nil, nil, nil, EmbedInfo{}, EnrichInfo{}, nil, "", nil)

	handler := srv.handleConsolidate()

	req := httptest.NewRequest("POST", "/v1/vaults/default/consolidate", strings.NewReader(`{}`))
	req.SetPathValue("vault", "default")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)

	// The consolidation worker does not error on an empty vault; it runs a
	// best-effort pass and returns a zero-count report with HTTP 200.
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for an empty vault (worker is best-effort), got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleConsolidate_InvalidJSON expects HTTP 400 when the request body is not valid JSON.
func TestHandleConsolidate_InvalidJSON(t *testing.T) {
	eng := newConsolidationTestEngine(t)
	srv := NewServer("localhost:0", eng, nil, nil, nil, EmbedInfo{}, EnrichInfo{}, nil, "", nil)

	handler := srv.handleConsolidate()

	req := httptest.NewRequest("POST", "/v1/vaults/default/consolidate", strings.NewReader("invalid-json"))
	req.SetPathValue("vault", "default")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON body, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleConsolidate_VaultMismatch expects HTTP 400 when the path vault does not
// match the authenticated request vault (defense in depth on top of the auth middleware).
func TestHandleConsolidate_VaultMismatch(t *testing.T) {
	eng := newConsolidationTestEngine(t)
	srv := NewServer("localhost:0", eng, nil, nil, nil, EmbedInfo{}, EnrichInfo{}, nil, "", nil)

	handler := srv.handleConsolidate()

	req := httptest.NewRequest("POST", "/v1/vaults/other/consolidate", strings.NewReader(`{}`))
	req.SetPathValue("vault", "other")
	req.Header.Set("Content-Type", "application/json")
	// Simulate an authenticated request for the "default" vault.
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextVault, "default"))
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a vault mismatch, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleConsolidate_NotAvailable expects HTTP 500 when the engine does not
// implement consolidation.EngineInterface, instead of panicking.
func TestHandleConsolidate_NotAvailable(t *testing.T) {
	eng := &MockEngine{}
	srv := NewServer("localhost:0", eng, nil, nil, nil, EmbedInfo{}, EnrichInfo{}, nil, "", nil)

	handler := srv.handleConsolidate()

	req := httptest.NewRequest("POST", "/v1/vaults/default/consolidate", strings.NewReader(`{}`))
	req.SetPathValue("vault", "default")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when consolidation is unavailable, got %d: %s", w.Code, w.Body.String())
	}
}
