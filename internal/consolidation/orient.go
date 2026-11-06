package consolidation

import (
	"context"
	"strings"

	"github.com/scrypster/muninndb/internal/storage"
)

// VaultSummary is the read-only output of Phase 0 (Orient).
type VaultSummary struct {
	Vault        string
	EngramCount  int
	WithEmbed    int
	AvgRelevance float32
	AvgStability float32
	IsLegal      bool
}

// runPhase0Orient scans the vault to produce a VaultSummary.
// Pure read-only -- no mutations even when DryRun is false.
func (w *Worker) runPhase0Orient(ctx context.Context, store *storage.PebbleStore, wsPrefix [8]byte, vault string) (*VaultSummary, error) {
	summary := &VaultSummary{
		Vault:   vault,
		IsLegal: isLegalVault(vault),
	}

	var totalRelevance, totalStability float64
	err := scanVaultWithEmbeddings(ctx, store, wsPrefix, func(eng *storage.Engram, embed []float32) error {
		summary.EngramCount++
		totalRelevance += float64(eng.Relevance)
		totalStability += float64(eng.Stability)
		if len(embed) > 0 {
			summary.WithEmbed++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if summary.EngramCount > 0 {
		summary.AvgRelevance = float32(totalRelevance / float64(summary.EngramCount))
		summary.AvgStability = float32(totalStability / float64(summary.EngramCount))
	}

	return summary, nil
}

// isLegalVault returns true if the vault is the "legal" vault or uses the legal prefix convention.
// Matches: "legal", "legal:contracts", "legal/docs" — but NOT "paralegal" or "illegal".
func isLegalVault(vault string) bool {
	v := strings.ToLower(vault)
	return v == "legal" || strings.HasPrefix(v, "legal:") || strings.HasPrefix(v, "legal/")
}
