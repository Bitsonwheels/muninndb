package consolidation

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/scrypster/muninndb/internal/storage"
)

// runPhase2Dedup scans all engrams with embeddings and merges semantically similar ones
// (cosine similarity >= 0.95). The higher-confidence engram is kept as the representative,
// and the other is archived. In DryRun mode, no mutations occur.
func (w *Worker) runPhase2Dedup(ctx context.Context, store *storage.PebbleStore, wsPrefix [8]byte, report *ConsolidationReport, vault string) error {
	similarityThreshold := float32(0.95)
	if w.DedupThreshold > 0 {
		similarityThreshold = w.DedupThreshold
	}

	// Build a list of engrams with embeddings only. The vault is scanned page
	// by page (scanVault), so only one page is resident at a time, and the
	// embeddings of a page are read in ONE batch instead of one point read
	// per engram.
	type engramWithEmbed struct {
		engram    *storage.Engram
		embedding []float32
	}
	withEmbed := make([]engramWithEmbed, 0)
	err := scanVaultWithEmbeddings(ctx, store, wsPrefix, func(eng *storage.Engram, embed []float32) error {
		// #728: an already-archived (or soft-deleted) engram is dedup's OWN
		// output — GetEngrams/scanAllEngramIDs do not filter by lifecycle
		// state, so without this check a member this phase archived on an
		// earlier run (or an earlier cluster in the SAME run) keeps
		// reappearing in every future cluster it embeds near. That is
		// silently harmless when the whole cluster fits under one run's
		// MaxDedup — the absorb loop just re-issues an idempotent archive —
		// but it BREAKS the hard cap's deferral guarantee: a cluster larger
		// than the remaining budget is trimmed to its FIRST N members in
		// cluster order, and if the already-archived member keeps sorting
		// into that same leading position, the next run spends its whole
		// budget re-selecting it and never reaches the genuinely still-live
		// duplicate — "deferred" would silently become "never," which is
		// exactly the kind of plausible-looking wrong answer this project
		// treats as worse than an honest failure. Skipping a non-live state
		// here is also just correct on its own terms: dedup's job is to
		// reduce the set of engrams recall still serves, and an
		// already-retired one is not a candidate for that job twice.
		if eng.State == storage.StateArchived || eng.State == storage.StateSoftDeleted {
			return nil
		}
		if len(embed) > 0 {
			withEmbed = append(withEmbed, engramWithEmbed{engram: eng, embedding: embed})
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Guard against the normalization anchor flip (issue #311): archiving even a
	// single duplicate cluster in a small vault can shift the per-query
	// normalization and flip top-1 recall. The minimum applies to every path
	// that runs phase 2 - the scheduler path (RunOnce) and the dream path.
	minSize := w.MinDedupVaultSize
	if minSize <= 0 {
		minSize = 20
	}
	if len(withEmbed) < minSize {
		slog.Info("consolidation phase 2 (dedup): skipped, vault below the minimum size",
			"vault", vault, "count", len(withEmbed), "min", minSize)
		return nil
	}

	// Find clusters of similar engrams (naive O(n²) pairwise comparison)
	type cluster struct {
		members []*storage.Engram // all members of the cluster
	}
	visited := make(map[storage.ULID]bool)
	var clusters []cluster

	for i := 0; i < len(withEmbed); i++ {
		if visited[withEmbed[i].engram.ID] {
			continue
		}

		clust := cluster{members: []*storage.Engram{withEmbed[i].engram}}
		visited[withEmbed[i].engram.ID] = true

		// Find all engrams similar to this one
		for j := i + 1; j < len(withEmbed); j++ {
			if visited[withEmbed[j].engram.ID] {
				continue
			}

			sim := cosineSimilarity(withEmbed[i].embedding, withEmbed[j].embedding)
			if sim >= similarityThreshold {
				clust.members = append(clust.members, withEmbed[j].engram)
				visited[withEmbed[j].engram.ID] = true
			}
		}

		// Only record clusters with more than 1 member
		if len(clust.members) > 1 {
			clusters = append(clusters, clust)
		}
	}

	report.DedupClusters = len(clusters)

	// Process each cluster: keep representative, archive others
	for _, clust := range clusters {
		if len(clust.members) < 2 {
			continue
		}

		// Cap at MaxDedup pairs per run
		if report.MergedEngrams >= w.MaxDedup {
			slog.Debug("consolidation phase 2: reached max dedup limit", "limit", w.MaxDedup)
			break
		}

		// Elect representative: highest relevance * confidence
		var representative *storage.Engram
		maxScore := float64(-1)
		for _, member := range clust.members {
			score := float64(member.Relevance) * float64(member.Confidence)
			if score > maxScore {
				maxScore = score
				representative = member
			}
		}

		if representative == nil {
			continue
		}

		// Pattern-separation guard, applied BEFORE any absorption: a high embedding
		// similarity is not proof of duplication. A member that differs from the
		// survivor on a load-bearing token (a number, a date, a negation) is a
		// DISTINCT fact — an update or a contradiction — and must be neither
		// archived NOR have its tags/frequency folded into the survivor (which would
		// cross-label a fact the guard just declared distinct). Partition first so
		// only true duplicates are absorbed; recall's supersedes-aware ranking /
		// contradiction surfacing handle the separated pair.
		absorbable := make([]*storage.Engram, 0, len(clust.members))
		for _, member := range clust.members {
			if member.ID == representative.ID {
				continue
			}
			if divergesOnLoadBearingToken(representative, member) {
				report.DedupSeparated++
				slog.Debug("consolidation phase 2: separation guard refused merge",
					"survivor", representative.ID, "kept", member.ID)
				continue
			}
			absorbable = append(absorbable, member)
		}

		// #728: MaxDedup is a HARD per-run cap, not a per-cluster one — trim
		// this cluster's absorbable set to whatever budget remains BEFORE
		// tags/frequency are folded from it, so a single cluster larger than
		// the remaining budget cannot make report.MergedEngrams overshoot
		// w.MaxDedup. The top-of-loop check above only stops the NEXT
		// cluster from starting; without this trim, a cluster already in
		// progress always finished in full regardless of the cap (the
		// mechanism a large duplicate cluster — e.g. many-times-repeated
		// auto-ingest captures — could exceed the configured rate limit by
		// its own size). The untrimmed members are left live, exactly as if
		// their cluster had not been visited yet: the next run re-scans,
		// reclusters them (the already-archived siblings are gone from
		// allEngrams by then), and finishes the merge — nothing is lost,
		// only deferred across runs. Pinned by TestDedup_RespectsMaxDedupCap
		// (exact-equality hard-cap assertion) and
		// TestDedup_MaxDedupCapDefersRestOfClusterToNextRun.
		if remaining := w.MaxDedup - report.MergedEngrams; remaining < len(absorbable) {
			if remaining < 0 {
				remaining = 0
			}
			absorbable = absorbable[:remaining]
		}
		if len(absorbable) == 0 {
			continue
		}

		// Merge tags from the survivor + only the ABSORBABLE (true-duplicate)
		// members — never from a separation-skipped member.
		tagSet := make(map[string]bool)
		for _, tag := range representative.Tags {
			tagSet[tag] = true
		}
		for _, member := range absorbable {
			for _, tag := range member.Tags {
				tagSet[tag] = true
			}
		}
		mergedTags := make([]string, 0, len(tagSet))
		for tag := range tagSet {
			mergedTags = append(mergedTags, tag)
		}

		// Archive the absorbable duplicates and count them so the representative can
		// absorb their frequency signal. Without this bump, semantic-duplicate
		// write-only engrams (e.g. daily auto-ingest captures from agent harnesses)
		// would never reach the recall path that auto-increments AccessCount in
		// storage/cache/domain.go — losing the "this content recurred N times"
		// signal that downstream skills currently emulate via per-vault feedback
		// cron jobs.
		archivedCount := uint32(0)
		for _, member := range absorbable {
			if !w.DryRun {
				// Archive the member (soft delete)
				if err := w.Engine.UpdateLifecycleState(ctx, vault, member.ID.String(), "archived"); err != nil {
					slog.Warn("consolidation phase 2: failed to archive engram", "id", member.ID, "error", err)
					continue
				}
			}

			archivedCount++
			report.MergedEngrams++
		}

		// Persist representative updates when anything changed: tag-merge
		// added tags, or frequency-absorption bumped AccessCount.
		//
		// Compute the new access count locally — do NOT mutate representative.
		// GetEngrams returns pointers into the L1 cache that concurrent recalls
		// may be reading (see the immutability contract on storage.L1Cache.Get);
		// mutating the shared struct here is a data race. UpdateMetadata persists
		// the value and invalidates the cache, so the next read is fresh.
		tagsChanged := len(mergedTags) != len(representative.Tags)
		newAccessCount := representative.AccessCount
		if !w.DryRun && archivedCount > 0 {
			newAccessCount += archivedCount
		}
		if !w.DryRun && (tagsChanged || archivedCount > 0) {
			if err := store.UpdateMetadata(ctx, wsPrefix, representative.ID, &storage.EngramMeta{
				State:       representative.State,
				Confidence:  representative.Confidence,
				Relevance:   representative.Relevance,
				Stability:   representative.Stability,
				AccessCount: newAccessCount,
				UpdatedAt:   representative.UpdatedAt,
				LastAccess:  representative.LastAccess,
			}); err != nil {
				slog.Warn("consolidation phase 2: failed to update representative metadata", "id", representative.ID, "error", err)
				report.Errors = append(report.Errors, fmt.Sprintf("dedup: UpdateMetadata %s: %v", representative.ID, err))
			}
			if tagsChanged {
				// Persist merged tags — UpdateMetadata patches only fixed-size
				// metadata fields and does not touch the variable-length tag list.
				if err := store.UpdateTags(ctx, wsPrefix, representative.ID, mergedTags); err != nil {
					slog.Warn("consolidation phase 2: failed to update representative tags", "id", representative.ID, "error", err)
					report.Errors = append(report.Errors, fmt.Sprintf("dedup: UpdateTags %s: %v", representative.ID, err))
				}
			}
		}
	}

	slog.Debug("consolidation phase 2 (dedup) completed", "clusters", report.DedupClusters, "merged", report.MergedEngrams)
	return nil
}

// cosineSimilarity computes the cosine similarity between two float32 vectors
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, magA, magB float64
	for i := range a {
		fa := float64(a[i])
		fb := float64(b[i])
		dotProduct += fa * fb
		magA += fa * fa
		magB += fb * fb
	}

	magA = math.Sqrt(magA)
	magB = math.Sqrt(magB)

	if magA == 0 || magB == 0 {
		return 0
	}

	return float32(dotProduct / (magA * magB))
}

// scanVault calls fn for every engram in the vault, one page at a time.
//
// The vault is walked with a paged scan, so only one page is resident at a
// time instead of the whole vault.
func scanVault(ctx context.Context, store *storage.PebbleStore, wsPrefix [8]byte, fn func(eng *storage.Engram) error) error {
	return scanVaultPages(ctx, store, wsPrefix, false, func(eng *storage.Engram, _ []float32) error {
		return fn(eng)
	})
}

// scanVaultWithEmbeddings calls fn for every engram in the vault together with
// its embedding (nil when the engram has none).
//
// ERF v2 stores the embeddings under a separate 0x18 key, so the engram
// records come back without them. The embeddings of a page are read in ONE
// batch (GetEmbeddings) instead of one point read per engram.
func scanVaultWithEmbeddings(ctx context.Context, store *storage.PebbleStore, wsPrefix [8]byte, fn func(eng *storage.Engram, embed []float32) error) error {
	return scanVaultPages(ctx, store, wsPrefix, true, fn)
}

func scanVaultPages(ctx context.Context, store *storage.PebbleStore, wsPrefix [8]byte, withEmbeddings bool, fn func(eng *storage.Engram, embed []float32) error) error {
	const pageSize = 500
	for offset := 0; ; offset += pageSize {
		// The metadata scan is cheap (it reads the slim 0x02 records) and
		// only carries the ids.
		meta, err := store.EngramsByCreatedSince(ctx, wsPrefix, time.Unix(0, 0), offset, pageSize)
		if err != nil {
			return err
		}
		if len(meta) == 0 {
			return nil // no more engrams
		}

		ids := make([]storage.ULID, 0, len(meta))
		for _, m := range meta {
			if m != nil {
				ids = append(ids, m.ID)
			}
		}

		// The full records of this page.
		engrams, err := store.GetEngrams(ctx, wsPrefix, ids)
		if err != nil {
			return err
		}

		var embeddings [][]float32
		if withEmbeddings {
			embeddings, err = store.GetEmbeddings(ctx, wsPrefix, ids)
			if err != nil {
				return err
			}
		}

		// engrams and embeddings are positionally aligned with ids.
		for i, eng := range engrams {
			if eng == nil {
				continue
			}
			var embed []float32
			if withEmbeddings {
				embed = eng.Embedding
				if len(embed) == 0 && i < len(embeddings) {
					embed = embeddings[i]
				}
			}
			if err := fn(eng, embed); err != nil {
				return err
			}
		}
	}
}
