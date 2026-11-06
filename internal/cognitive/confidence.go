package cognitive

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"
)

const (
	LaplaceSmoothingAlpha = 0.025 // equivalent to adding 0.025 pseudo-observations
	LaplaceSmoothingScale = 0.95

	EvidenceContradiction = 0.1
	EvidenceCoActivation  = 0.65
	EvidenceUserConfirmed = 0.95
	EvidenceUserRejected  = 0.05

	// NegligibleDelta is the confidence change below which an update is not
	// worth writing back.
	NegligibleDelta = 0.001
)

// ConfidenceStore is the storage interface for confidence updates.
type ConfidenceStore interface {
	UpdateConfidence(ctx context.Context, ws [8]byte, id [16]byte, confidence float32) error
	GetConfidence(ctx context.Context, ws [8]byte, id [16]byte) (float32, error)
}

// ConfidenceUpdate is submitted to the confidence worker.
type ConfidenceUpdate struct {
	WS       [8]byte
	EngramID [16]byte
	Evidence float64 // 0.0-1.0
	Source   string  // "contradiction_detected", "co_activation", "user_confirmed", "user_rejected"
}

// BayesianUpdate applies a Bayesian update to the prior confidence.
func BayesianUpdate(prior, evidence float64) float64 {
	// Clamp inputs to [0, 1] to guard against out-of-range values reaching
	// the denominator calculation.
	if prior < 0 {
		prior = 0
	} else if prior > 1 {
		prior = 1
	}
	if evidence < 0 {
		evidence = 0
	} else if evidence > 1 {
		evidence = 1
	}
	// posterior = (prior × evidence) / (prior × evidence + (1-prior) × (1-evidence))
	numerator := prior * evidence
	denominator := numerator + (1-prior)*(1-evidence)
	// Clamp denominator to prevent division by zero or near-zero divergence when
	// both prior and evidence are close to 1.0 (or both close to 0.0).
	if denominator < 1e-9 {
		denominator = 1e-9
	}
	posterior := numerator / denominator
	// Laplace smoothing: prevents 0 or 1
	return LaplaceSmoothingScale*posterior + LaplaceSmoothingAlpha
}

// EvidenceStrength returns the evidence strength for a source.
func EvidenceStrength(source string) float64 {
	switch source {
	case "contradiction_detected":
		return EvidenceContradiction
	case "co_activation":
		return EvidenceCoActivation
	case "user_confirmed":
		return EvidenceUserConfirmed
	case "user_rejected":
		return EvidenceUserRejected
	default:
		return 0.5 // neutral
	}
}

// ConfidenceWorker updates engram confidence scores via Bayesian updating.
type ConfidenceWorker struct {
	*Worker[ConfidenceUpdate]
	store ConfidenceStore
}

// NewConfidenceWorker creates a new confidence worker.
func NewConfidenceWorker(store ConfidenceStore) *ConfidenceWorker {
	cw := &ConfidenceWorker{store: store}
	cw.Worker = NewWorker[ConfidenceUpdate](
		5000, 100, 30*time.Second,
		cw.processBatch,
	)
	return cw
}

func (cw *ConfidenceWorker) processBatch(ctx context.Context, batch []ConfidenceUpdate) error {
	// Group updates by engram ID to chain them
	type updateGroup struct {
		ws       [8]byte
		id       [16]byte
		evidence []float64
	}
	grouped := make(map[[16]byte]*updateGroup)

	for _, u := range batch {
		if g, ok := grouped[u.EngramID]; ok {
			g.evidence = append(g.evidence, u.Evidence)
		} else {
			grouped[u.EngramID] = &updateGroup{
				ws:       u.WS,
				id:       u.EngramID,
				evidence: []float64{u.Evidence},
			}
		}
	}

	for _, g := range grouped {
		current, err := cw.store.GetConfidence(ctx, g.ws, g.id)
		if err != nil {
			continue
		}

		prior := float64(current)
		for _, ev := range g.evidence {
			prior = BayesianUpdate(prior, ev)
		}

		delta := math.Abs(prior - float64(current))
		if delta < NegligibleDelta {
			continue
		}

		if err := cw.store.UpdateConfidence(ctx, g.ws, g.id, float32(prior)); err != nil {
			slog.Error("confidence: failed to persist updated confidence",
				"engram_id", fmt.Sprintf("%x", g.id), "error", err)
		}
	}
	return nil
}
