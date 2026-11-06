package consolidation

import (
	"context"
	"log/slog"
	"time"

	"github.com/scrypster/muninndb/internal/storage"
)

// runPhase4DecayAcceleration accelerates relevance decay for old, low-access engrams.
// Engrams matching ALL of these criteria are decayed:
// - AccessCount < 2
// - Age > 30 days
// - Relevance < 0.3
// Decay: relevance = relevance * 0.5
func (w *Worker) runPhase4DecayAcceleration(ctx context.Context, store *storage.PebbleStore, wsPrefix [8]byte, report *ConsolidationReport) error {
	const maxAccessCount = 2
	const maxRelevance = 0.3
	ageThreshold := 30 * 24 * time.Hour
	now := time.Now()

	var decayed int
	err := scanVault(ctx, store, wsPrefix, func(eng *storage.Engram) error {
		// Check all three criteria
		if eng.AccessCount >= maxAccessCount {
			return nil // has sufficient access
		}

		age := now.Sub(eng.CreatedAt)
		if age < ageThreshold {
			return nil // too new
		}

		if eng.Relevance >= maxRelevance {
			return nil // too relevant already
		}

		// Apply decay
		newRelevance := eng.Relevance * 0.5

		if !w.DryRun {
			if err := store.UpdateRelevance(ctx, wsPrefix, eng.ID, newRelevance, eng.Stability); err != nil {
				slog.Warn("consolidation phase 4: failed to decay engram", "id", eng.ID, "error", err)
				return nil
			}
		}

		decayed++
		return nil
	})
	if err != nil {
		return err
	}

	report.DecayedEngrams = decayed
	slog.Debug("consolidation phase 4 (decay acceleration) completed", "decayed", decayed)

	return nil
}
