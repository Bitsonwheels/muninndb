# Audit Round 2 — cross-package findings (2026-11-07)

Read-only audit, no code changed. Source: claude-mem session summaries S615–S619
(2026-11-07). Verification status per item:

- **[checked]** — re-verified against the code today (2026-11-08), with file:line
- **[session]** — verified during the audit session itself (against the codebase)
- **[recovered]** — survives only in the session summary; re-check against the code
  before fixing

Round 1 (20 findings, all fixed): see `AUDIT.md`. Round 2 is broader and shallower:
whole packages read for correctness, races, and design issues.

## Scope

Covered: `internal/storage` (+ `wal`, `index/hnsw`), `internal/mcp`,
`internal/replication`, `internal/engine` (activation, trigger, autoassoc),
`internal/query/mql`, `internal/transport/mbp` + rest (partial), `cmd/` shutdown
paths.

**Not yet audited** (round-3 candidates): `internal/transport/rest` + `ws` (full),
`internal/auth` (post-round-1 changes), `sdk/*`, `web/`, `internal/config`,
`internal/index` (non-hnsw), `cmd/` (full).

Recovery caveat: the audit session that produced 53 findings for mcp/replication
wrote full details to an observation store that is no longer readable; only the
23 items named in the session summary below were recovered. The remaining ~30
exist only as an aggregate count.

---

## A. internal/storage, internal/wal, internal/index/hnsw (session S616)

Fifteen issue groups examined; 13 actionable, 2 dead subsystems, 4 verified
correct. A1 is the most damaging.

- **A1. ERF v1/v2 encoding drift degrades v2 engrams on metadata writes.**
  All four write paths re-encode through `erf.Encode` (v1), which drops
  embeddings and associations from v2 records. A metadata-only write (e.g.
  confidence update) silently strips the vector payload.
  [checked] `internal/storage/engram.go:902,1073,1284,1445`
- **A2. Vault counter race in ClearVault.** Counter persistence during
  ClearVault is not synchronized with concurrent writers. [recovered]
- **A3. Unsynchronized Bloom filter assignment in the GC path.** GC writes the
  archive bloom without the synchronization the read path assumes.
  [recovered] `internal/storage/archive_bloom.go`
- **A4. WAL reader coverage is disjoint.** `ReadFrom` reads only the active
  segment while `Recover` reads sealed segments — a consumer using the wrong
  one misses records. [recovered] `internal/storage/wal/mol.go`
- **A5. HNSW Insert serializes all writes behind one lock and spawns
  unbounded goroutines** (fan-out per insert). [recovered — lock sites present
  at `internal/index/hnsw/hnsw.go:52,116,420,486,868`; the per-insert goroutine
  claim still needs a direct look before fixing]
- **A6. HNSW load-time rebuild from orphan nodes is O(n²).** [recovered]
- **A7. Duplicate O(ef²) beam-search logic** — two independent implementations
  to keep in sync. [recovered]
- **A8. Dead subsystems: `write_batcher` and `DomainCache`.** Both files exist
  with no production references. [checked — `internal/storage/write_batcher.go`,
  `internal/storage/cache/domain.go`; no references in `impl.go`/`cmd/`]
- **A9. GetConceptAssociations missing error/context checks.** [recovered]
- **A10. Hand-rolled prefix upper bounds** duplicating `keys.PrefixUpperBound`.
  [recovered]
- **A11. GetEngram returns the live cache pointer**, violating the
  caller-must-not-mutate contract. [recovered]

## B. internal/mcp (session S617; part of 53 total, 10 recovered)

- **B1. SSE response leak keyed by auth token, not session identity.** A client
  reconnecting with a rotated token orphans the old SSE stream. [recovered]
- **B2. 1 MiB request body cap contradicts the advertised ~3.7 MB batch
  payload.** Large legal batches are rejected. [recovered]
- **B3. `muninn_state` silently discards its `reason` parameter.** [recovered]
- **B4. `annotate=true` recall does a per-row engine round trip.** [recovered]
- **B5. Silent entity caps on `remember`.** Entities beyond the cap are dropped
  without an error or warning. [recovered]
- **B6. Asymmetric bare-string handling between `evolve` and `remember`.**
  [recovered]
- **B7. Dead event bus code.** [recovered]
- **B8. RetryEnrich nil-panic risk.** [recovered]
- **B9. Caller-supplied session ID is trusted as the dedup key.** Enables
  dedup bypass / cross-session aliasing. [recovered]
- **B10. Wildcard CORS on the SSE endpoint.** [recovered]

## C. internal/replication (session S617; 14 recovered)

- **C1. ClusterTLS is never wired** — inter-node traffic is plaintext even when
  TLS is configured. [checked — `internal/replication/tls.go` implements
  `NewClusterTLS`/`Bootstrap`, but no caller exists outside `tls.go` itself;
  `cmd/muninn/server.go` only calls `auth.Bootstrap`]
- **C2. DRAINING is never enforced at the write gate.** Writes accepted during
  failover are lost. [checked — `coordinator.go:1942` documents that rejection
  "belongs in the engine layer"; no engine-side gate was found]
- **C3. HandleHandoff does not authenticate the sender.** [recovered]
- **C4. Snapshot handler missing chunk validation.** [recovered]
- **C5. Reconciler replies to the payload-supplied NodeID instead of the frame
  source** — spoofable routing. [recovered]
- **C6. Co-activations forwarded under workspace zero.** [recovered]
- **C7. Cognitive effects silently dropped.** [recovered]
- **C8. Log prune advances the watermark before the delete** — a crash in
  between loses records permanently. [recovered]
- **C9. Any node can inflate the epoch.** [recovered]
- **C10. `votedFor` grows without bound.** [recovered]
- **C11. Dead code: parallel-election path, streamer, RegisterHandler.**
  [recovered]
- **C12. Broadcast discards the per-peer error map.** [recovered]
- **C13. AddPeer resets LastSeen** (skews failure detection). [recovered]
- **C14. Quorum is computed from two different populations.** [recovered]

## D. internal/engine (+ query/mql, partial transport) — session S618

Fifteen substantive findings; 7 recovered by name. The full TestEntityBoost
suite was run 10× under `-race` (8 cores, 255 s): no data races.

- **D1. handleContradiction nil-dereference.** [recovered]
- **D2. applyEntityBoost mutates before copying** (shared-struct aliasing).
  [recovered]
- **D3. Duplicate SubscriptionID leaks in the trigger registry.** [recovered]
- **D4. TransitionCache `scanSource` performance** (unbounded scan). [recovered]
- **D5. Phase-6 lease point-read N+1** in the activation pipeline. [recovered]
- **D6. PassesMetaFilter fall-through behavior** (filters silently pass on
  unexpected operators). [recovered]
- **D7. wsVaultID 4-byte prefix collision** can route across vaults (cross-vault
  data-leakage risk); full-prefix keying is the noted long-term fix. [recovered]
- **D8. Dead code inventory: 7 unused items** across the audited packages.
  [recovered — items not individually named in the summary]
- **D9. Three near-identical autoassoc worker pools** with inconsistent
  metrics. [recovered]

## E. Cross-cutting (session S619)

- **E1. Shutdown ordering.** `eng.Stop()` runs before hebbianWorker but after
  the coordinator; 17 ticker stop sites across `cmd/` were mapped. SIGHUP is
  registered unconditionally. [session]
- **E2. GroupCommitter** uses a 4096-slot pending channel with fire-and-forget
  `AppendAsync`; dropped entries are counted, not retried. [session]
- **E3. GetEngrams returns nil for missing engrams** without documenting it.
  [session]
- **E4. Trigger contradiction handling** applies burst-aware rate limiting
  (overdraft 3). [session]

**Theme of the round:** features exist but are not wired — config lies
(TLS), guards live in the wrong layer (draining), and encoding paths silently
downgrade data (v1 re-encode).

## Verified correct — do not "fix"

- Endpoint guards, error-handling discipline, cache eviction policies, storage
  shutdown paths (S616).
- MCP auth chain: sound and fail-closed; `mk_` vault keys > static token >
  capability keys; empty token disables auth (S615/S617).
- Election core: votedFor immutability, voter-population matching, tryPromote
  CAS, tie-breaking (S617).
- Entity boost: race-free under `-race`; the vault-scoped df degradation in the
  TF-IDF variant is intentional under-crediting of cross-vault entities, not a
  bug (S615/S618).
- Trigger registry limits: MaxSubscriptionsPerVault (default 100) + TTL expiry
  (S615). HNSW memory-pressure throttling + env-var limits (S615).

## Suggested priority for round-2 fixes

1. **Reachable crashes:** B8 (RetryEnrich nil panic), D1 (contradiction nil
   deref), decompression bomb / unauthenticated body amplification (S619 list).
2. **Silent data loss:** A1 (v1 re-encode), C2 + C8 (failover/prune tails),
   shutdown tail (E1), B5 (silent entity caps).
3. **Security:** C1 (cluster TLS), C3 (handoff auth), B9 (session dedup),
   B10 (CORS), D7 (vault prefix collision), C5 (reconciler routing).
4. **Performance/throughput:** A5–A7 (HNSW), B4 (annotate round trips), D4–D5.
5. **Dead code:** A8, B7, C11, D8 — deletion over addition.
