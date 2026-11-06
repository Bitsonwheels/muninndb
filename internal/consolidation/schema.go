package consolidation

import (
	"context"
	"log/slog"
	"math"

	"github.com/scrypster/muninndb/internal/storage"
)

// runPhase3SchemaPromotion identifies and promotes highly-connected, high-confidence engrams
// to be schema nodes by boosting their relevance. An engram is promoted if:
// - Out-degree >= 10 (at least 10 forward associations)
// - Relevance >= 0.8
// Promotion boost: relevance = min(relevance * 1.1, 1.0)
func (w *Worker) runPhase3SchemaPromotion(ctx context.Context, store *storage.PebbleStore, wsPrefix [8]byte, report *ConsolidationReport) error {
	const minOutDegree = 10
	const minRelevance = 0.8
	const boostFactor = 1.1

	var promoted int
	err := scanVault(ctx, store, wsPrefix, func(eng *storage.Engram) error {
		if eng.Relevance < minRelevance {
			return nil
		}

		// Count forward associations (out-degree)
		assocs, err := store.GetAssociations(ctx, wsPrefix, []storage.ULID{eng.ID}, 1000)
		if err != nil {
			return nil
		}

		if len(assocs[eng.ID]) < minOutDegree {
			return nil
		}

		// Promote: boost relevance
		newRelevance := math.Min(float64(eng.Relevance)*boostFactor, 1.0)

		if !w.DryRun {
			if err := store.UpdateRelevance(ctx, wsPrefix, eng.ID, float32(newRelevance), eng.Stability); err != nil {
				slog.Warn("consolidation phase 3: failed to promote engram", "id", eng.ID, "error", err)
				return nil
			}
		}

		promoted++
		return nil
	})
	if err != nil {
		return err
	}

	report.PromotedNodes = promoted
	slog.Debug("consolidation phase 3 (schema promotion) completed", "promoted", promoted)

	return nil
}
