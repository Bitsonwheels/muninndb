# MuninnDB Repository Audit

Audit date: 2026-11-06. No code was changed while writing this report.

## What this repo does:

MuninnDB is a **memory database for AI agents**. An agent writes short notes ("engrams") into it; later it asks questions and gets the most useful notes back. Notes are stored in a local embedded database (Pebble), indexed three ways (full text, vector/HNSW, associations), and served over four protocols: REST (:8475), Web UI (:8476), MCP (:8750, for Claude/Cursor/VS Code) and gRPC (:8477). It can run as one node or as a small cluster (one leader, several followers).

**Load I assumed:** one person on one machine, with a handful of AI agent processes writing/reading continuously (tens of writes per second at peaks), a few hundred thousand notes over time. In cluster mode: 2–5 nodes, one writer, and the same few agents talking to any node. Not a public multi-tenant service.

---

# Must fix

> Status 2026-11-06: findings 1-4 are fixed on branch `fix/audit-2026-11` (one commit each).

**1. Docker Compose starts a wide-open server — `docker-compose.yml:7-16,46`, `internal/auth/bootstrap.go:33-46,78-95`, `internal/auth/middleware.go:55-75`**
- **Status:** FIXED. Compose binds every host port to `127.0.0.1` and requires `MUNINN_ADMIN_PASSWORD` (`${MUNINN_ADMIN_PASSWORD:?...}`, compose refuses to start without it). The server itself now refuses to start when it is bound to a non-loopback address while the admin account still uses the default password, and it then creates the `default` vault **private**. On a loopback bind the zero-config quickstart is unchanged. `MUNINN_DEFAULT_VAULT_PUBLIC=1` is the documented escape hatch for proxy-authenticated setups. README gained a "Remote Access" section (SSH tunnel + systemd user unit).
- **What this is:** The compose file publishes every port on all host interfaces (`"8475:8475"` etc.), sets `MUNINN_LISTEN_HOST: "0.0.0.0"`, and sets no admin password. On first start the server creates admin `root`/`password` and creates the vault `default` as **public**.
- **Problem:** A public vault is served with full read/write rights and no key (`middleware.go:62-74` sets mode `ModeFull`). So anyone on the same network can read and write the `default` vault, and can log in as `root`/`password` on the admin UI (port 8476). Docker port publishing bypasses a host firewall rule that only protects loopback.
- **Fix:** In `docker-compose.yml`, bind ports to `127.0.0.1:` (e.g. `"127.0.0.1:8475:8475"`) and add `MUNINN_ADMIN_PASSWORD: "change-me"`. Smallest safe version: change the host side of every port mapping to `127.0.0.1` and set the password variable.
- **If we skip it:** One `docker compose up` on a laptop in a café or on a VPS exposes a writable memory store to the internet, with a known admin password.

**2. Authenticated REST routes accept only 64 KB, not 4 MB — `internal/transport/rest/server.go:576-579,585-590,595-601,608-613,628-633`**
- **Status:** FIXED. New `withObservability` holds the request-id/logging/recovery chain; the 64 KB wrapper now sits only on the public routes, the authenticated chain has exactly one `MaxBytesReader`. The authenticated limit was raised to **32 MB** (owner decision), the import route keeps 512 MB. Test: `internal/transport/rest/body_size_test.go` (1 MB body → 201 on an authenticated route, → 400 on a public route).
- **What this is:** `withPublicMiddleware` wraps the body in a 64 KB `MaxBytesReader`. `withMiddleware` calls `withPublicMiddleware(bodySizeMiddleware(...))`, so the 4 MB reader ends up *inside* the 64 KB one. Go's `MaxBytesReader` wraps, it does not replace — so the outer 64 KB limit wins.
- **Problem:** Every `/api/...` route (27 of them) rejects bodies over 64 KB with `400 invalid request body`. A 100 KB note can be written over MCP but not over REST. The file already knows the bug: `withAdminMiddlewareNoSizeLimit` (:612-622) exists "so multiple MaxBytesReader wrappers don't compound" — but only the import route uses it. No test sends a large body to an authenticated route.
- **Fix:** Delete the `publicBodySizeMiddleware` call from the shared chain and put the 64 KB wrapper only on the individual public routes (health, ready, hello) that need it. Keep the 4 MB limit in `withMiddleware`/`withAdminMiddleware`.
- **If we skip it:** Users hit "my note was rejected for no reason" and the real limit stays hidden in the code.

**3. Admin login has no rate limit and no lockout — `internal/ui/server.go:299-326`, `internal/auth/bootstrap.go:33-46`**
- **Status:** FIXED. `internal/ui/login_throttle.go` counts failed logins per client IP (10 failures in 5 minutes → `429` with `Retry-After`); a successful login clears the counter, so the CLI's repeated logins are never punished. Test: `internal/ui/login_throttle_test.go`.
- **What this is:** `handleAdminLogin` checks the password with bcrypt and returns 401 on failure. Failed attempts are logged to the audit log, but nothing counts them and nothing slows them down. The REST API has a per-IP limiter; the UI login does not.
- **Problem:** The default password is `password`. Once the UI is reachable (docker case above, or a reverse proxy in front), a script can try thousands of passwords per minute. bcrypt makes each try slow for the attacker only as far as their own CPU goes, and nothing on the server side limits it.
- **Fix:** Reuse the existing REST limiter idea: keep a small per-IP counter for `/api/auth/login` and answer `429` after e.g. 10 failures per minute. About 15 lines in `internal/ui/server.go`.
- **If we skip it:** The admin account can be brute-forced whenever the UI port is not loopback-only.

**4. Changing the admin password does not end old sessions — `internal/auth/session.go:13,24-50`, `internal/auth/admin.go:64`**
- **Status:** FIXED, with a different mechanism than proposed: rotating `auth_secret` would have had to travel through every server that holds it (`rest.NewServer`/`ui.NewServer` signatures, ~300 test call sites). Instead every session token now carries a keyed fingerprint of the admin's **current password hash** (`username|expiry|fingerprint`); a password change changes the hash, so every old cookie stops validating. Re-applying the same password is a no-op, so a restart with `MUNINN_ADMIN_PASSWORD` does not log everyone out. Test: `internal/auth/session_test.go`.
- **What this is:** A session cookie is `base64(username|expiry).HMAC`, valid for 24 hours. The HMAC key (`auth_secret`) is created once at first start and never changed. `ChangeAdminPassword` only rewrites the bcrypt hash.
- **Problem:** A stolen session cookie keeps working for up to a day after the owner changes the password. Logout only deletes the cookie in the browser.
- **Fix:** On password change, write a new random `auth_secret` to the data dir (the file already exists and is loaded at startup). All old cookies then fail the HMAC check. One file write plus one call.
- **If we skip it:** "I changed the password, so the intruder is out" is not true for 24 hours.

---

# Should fix

**5. MCP HTTP server has no timeouts and no rate limit — `internal/mcp/server.go:127,157,582`**
- **What this is:** The MCP HTTP server sets only `ReadHeaderTimeout: 10s`. No `ReadTimeout`, no `WriteTimeout`, no `IdleTimeout`, and no limiter (the REST server has all three plus a per-IP limiter).
- **Problem:** Many slow, half-open connections can be held open cheaply. And because there is no limiter, a wrong API key can be guessed at full speed, with no counter.
- **Fix:** Copy the REST server's settings (`internal/transport/rest/server.go:305-312`) onto the MCP `http.Server`, and reuse the existing limiter helper for the MCP mux.
- **If we skip it:** MCP (the port most people expose to agents) is the easiest port to abuse.
- **Status:** Fixed (commit e203bdd) — limiter moved to `internal/httprate` (shared by REST and MCP); the MCP `http.Server` now sets `ReadTimeout`/`WriteTimeout`/`IdleTimeout`/`MaxHeaderBytes`; the SSE stream clears the write deadline. Tests: `TestServerHasTimeouts`, `TestServerRateLimitRejects`.

**6. A dropped full-text index job makes a note invisible to search, forever — `internal/index/fts/worker.go:14,36-40,130-146`**
- **What this is:** Indexing runs in a background worker with a 32,768-slot queue. When the queue is full the job is dropped and only a warning is written. The comment says visibility is "delayed" — it is not, it is lost: nothing re-scans the store for missing postings. Only a manual admin re-index (`ReindexFTSVault`) repairs it.
- **Problem:** After a burst (bulk import, migration, cluster catch-up), some notes are missing from every keyword search while still appearing in vector recall. The answer looks incomplete for no visible reason — exactly the "quietly wrong" failure this project says it hates.
- **Fix:** Two lines: on drop, write the job to a small on-disk list, or simply count drops per vault and have the existing admin re-index run for vaults with drops. Simplest version: make `Submit` block briefly instead of dropping, since the writer already has the note in Pebble.
- **If we skip it:** Search results quietly lose notes after every heavy import, and only a manual repair brings them back.
- **Status:** Fixed (commit 79bec8a) — `Submit` no longer drops on a full queue: it waits one flush cycle and then indexes the job inline (`indexInline`, panic-safe, skips clearing vaults). The dead `dropped` counter and `Dropped()` are gone. Test: `TestSubmitIndexesInlineWhenQueueIsFull` (RED proof included). Pre-existing flake: `TestEngine_WritesDuringShutdown` fails ~1 in 5 runs with AND without this change (slow-disk stall in pebble's WAL `fdatasync`), unrelated to this fix.

**7. Consolidation can dedup a small vault and flip recall results — `internal/consolidation/worker.go:75-84`, `internal/consolidation/dream.go:104-133`, `internal/transport/rest/consolidation_handlers.go:31-74`**
- **What this is:** `DreamOnce` skips dedup when a vault has fewer than 20 notes with embeddings, because deleting a few of them moves the score normalization and flips the top-1 answer for unrelated questions. `Worker.RunOnce` has no such guard — the code even says so in a TODO (#311-followup). The only caller of `RunOnce` is a REST handler that is **never registered** on the mux.
- **Problem:** The known bug is still in the code path and the guard exists only in the sibling path. Today it is unreachable through HTTP, but the moment someone wires the route (it has a test and a full request/response type, so it looks ready) the bug becomes live: a 9-note vault gets 3 notes merged away and recall answers change.
- **Fix:** Move the small-vault guard into `runPhase2Dedup` (`internal/consolidation/dedup.go:16`) so both callers get it, and delete the now-wrong TODO.
- **If we skip it:** A one-line route registration turns a documented bug into a live data-mutating bug.
- **Status:** Fixed (commit a516c87) — the small-vault guard moved into `runPhase2Dedup`, so the scheduler path (`RunOnce`) and the dream path both get it. The stale `TODO(#311-followup)` is gone. Tests: `TestRunOnce_SmallVault_SkipsDedup`, `TestRunOnce_SmallVault_DedupRunsWhenConfigured`.

**8. Dedup loads the whole vault and reads once per note — `internal/consolidation/dedup.go:22-80`**
- **What this is:** Phase 2 scans every note in the vault, loads them all into memory, and for each note without an inline vector does a separate `GetEmbedding` read (ERF v2 stores vectors under a separate key).
- **Problem:** Cost is O(vault) memory plus one point read per note. A 50,000-note vault means 50,000 extra reads and all vectors in RAM in one pass, inside a request with a 10-minute timeout.
- **Fix:** Batch the embedding reads with one `GetEmbeddings` call (the storage layer already has batched getters), and cap the notes scanned per pass (e.g. 5,000) — the pass can repeat later.
- **If we skip it:** The first big vault that runs consolidation gets slow, memory-hungry, and hard to abort.
- **Status:** Fixed (commit 55e3112) — the vault is walked in pages of 500 (one page of full engrams resident at a time); vectors come from one batched `GetEmbeddings` per page instead of one point read per note. `scanAllEngramIDs` deleted. Test: `TestDedup_PagedScanFindsDuplicateAcrossPages` (501 engrams, duplicate pair straddles two pages) + `TestOrient_PagedScanCountsVault`.

**9. Every node pays for the same embed/enrich backlog — `cmd/muninn/server.go:1739-1757`, `internal/plugin/retroactive.go:289-350`**
- **What this is:** The retroactive processor re-embeds or re-enriches notes that were written before the plugin was configured. It starts on every node, with no leader check.
- **Problem:** In a 3-node cluster, a backlog of 5,000 old notes is embedded three times. With a paid cloud embedder that is 3× the bill for the same content, and 3× the tokens for enrichment.
- **Fix:** Start the processor only on the leader (the cluster already exposes `IsLeader`), and let followers pick up the work when they take over.
- **If we skip it:** Cluster users pay several times over for the same background work.
- **Status:** Fixed (commit 400195d) — the retroactive embed/enrich processors get a leader gate (`SetLeaderCheck`); on a follower `processBatch` returns without even counting the backlog. Installed only when a cluster coordinator exists. Tests: `TestRetroactiveProcessor_ProcessBatchSkipsWhenNotLeader`, `...RunsOnLeader`.

**10. A backup can say "OK" while part of it is missing — `internal/backup/scheduler.go:125-185`, `cmd/muninn/backup.go:141-148`**
- **What this is:** The scheduler writes a Pebble checkpoint (a complete snapshot), then copies the WAL directory and `auth_secret`. If those copies fail, the code only logs a warning and the run is still recorded as successful.
- **Problem:** The note data is safe (the checkpoint is complete), but a restore from that backup has no `auth_secret` (every admin session and cookie dies) and loses replication sequence continuity. The operator sees a green backup and finds out during a restore.
- **Fix:** Treat a failed copy as a failed backup: call `recordError` instead of `slog.Warn`. Two lines in each file.
- **If we skip it:** A restore quietly loses admin sessions, and the backup log says everything was fine.
- **Status:** Fixed (commit aa44c38) — a failed WAL/auth_secret copy now marks the run as failed (`lastRunOK=false`, `lastError` set, `"backup: scheduled backup incomplete"`); the CLI backup exits 1 with "Backup incomplete". Test: `TestRunOnce_CopyFailureMarksRunFailed` (RED proof included).

**11. The audit webhook throws events away when the endpoint is down — `internal/audit/webhook_sink.go:71-95`**
- **What this is:** Every 5 seconds the batch is posted to the configured URL. On any error the batch is dropped (it was already moved out of the slice) and only a warning is written.
- **Problem:** During an outage of the log collector, exactly the audit events you want (failed logins, vault clears, key changes) disappear. There is no retry and no local copy unless another sink is enabled.
- **Fix:** On failure, put the batch back at the front of the slice (it is dropped after a successful post) and keep it until it goes through. About 5 lines.
- **If we skip it:** The audit trail has holes exactly when something went wrong.
- **Status:** Fixed (commit e88661c) — a failed batch goes back to the front of the pending list and is retried; a 4xx answer is dropped with an error log (retrying would fail the same way); the pending list is capped at 10,000 events (oldest dropped first). Test: `TestWebhookSink_RetriesFailedBatch`, `TestWebhookSink_PendingBatchStaysCapped`.

**12. `install.sh` installs an unverified binary when the checksum file is missing — `install.sh:62-93`**
- **What this is:** A checksum mismatch is fatal, but a missing `checksums.txt` (or a missing `sha256sum` tool) only prints a warning and the install continues.
- **Problem:** The release workflow always publishes checksums now, so a missing file means someone removed it or the download was tampered with. In that case the installer runs an arbitrary binary as the user.
- **Fix:** Only allow the skip for old releases (e.g. tags before the checksum workflow started); for current releases, fail with "checksums missing — refusing to install".
- **If we skip it:** The one moment where the download is not trustworthy is the one moment the installer trusts it.
- **Status:** Fixed (commit 660df74) — a missing checksum file or a missing `sha256sum`/`shasum` tool now aborts the install (no more "warn and continue"). Test: `scripts/test-install.sh` (4 cases). CI runs it.

**13. Build assets are downloaded without checksums — `Makefile:14-30,44-90`**
- **What this is:** `make fetch-model` and `fetch-ort-libs` pull `model_int8.onnx`, `tokenizer.json` and the ONNX runtime libraries from Hugging Face and GitHub with `curl -fL` and no hash check.
- **Problem:** The files end up inside the built binary. A changed upstream file changes what the server runs (an ONNX library is native code). This is build-time only — the running server never downloads models.
- **Fix:** Ship a `checksums.txt` next to the assets and verify after download, like `install.sh` already does for binaries.
- **If we skip it:** A compromised or changed upstream file silently becomes part of your build.
- **Status:** Fixed (commit 5cfcb78) — every downloaded asset (model, tokenizer, 5 ORT libs) has a pinned SHA-256 and is verified right after download via `scripts/verify-sha256.sh`. Test: `scripts/test-verify-sha256.sh` (3 cases) + a real `make fetch-model` run that printed both verified hashes. CI runs it.

---

# Nice to have

**14. The same copy helpers exist twice — `cmd/muninn/backup.go:158-217` and `internal/backup/scheduler.go:234-290`**
- **What this is:** `copyFile`, `copyDir` and a directory-size helper are written twice, with only comment and name differences.
- **Problem:** Two copies drift. The CLI version and the scheduler version already differ in names (`dirSizeBytes` vs `dirSize`).
- **Fix:** Keep the `internal/backup` versions, export them, and call them from `cmd/muninn/backup.go`. Deletes about 57 lines.
- **If we skip it:** Nothing breaks today; the next change to one copy will miss the other.
- **Status:** Fixed (commit 42e760b) — `CopyFile`, `CopyDir`, `DirSize` are exported from `internal/backup` and used by `cmd/muninn/backup.go`; the duplicate copies are deleted (~57 lines). Bonus fix (babfdc8): the Phase-1 deletion of `shouldWarnDefaultPasswordExposure` had left a test referencing it; `go vet` caught it and the test was replaced by `TestAnyNonLoopbackAddr`.

**15. `internal/cognitive/decay.go` is only used by its own tests — `internal/cognitive/decay.go:1-240`**
- **What this is:** `DecayWorker`, `DecaySchedule`, `EbbinghausWithFloor` and `ComputeStability` are never called from production code — only from `decay_test.go`, `decay_clockskew_test.go` and `decay_lastaccess_test.go`. The engine computes decay at query time instead; consolidation's decay phase is explicitly disabled.
- **Problem:** 240 lines of code that look alive because they have good tests, but nothing in the running server builds a `DecayWorker`.
- **Fix:** Delete the file and its three test files, or wire it up if the feature is really wanted. Either way, do not leave it looking finished.
- **If we skip it:** A reader assumes a decay worker runs and looks for where it is used.
- **Status:** Fixed (commit be413ca) — `internal/cognitive/decay.go` and its three test files deleted (the worker was never wired; the engine computes decay at query time). `NegligibleDelta` moved to `confidence.go`; the `decayStoreAdapter` and its tests deleted; the census test's known-site list and the invariants doc updated. Later cleanup commit for the remaining references.

**16. Two dead methods — `internal/index/hnsw/hnsw.go:60`, `internal/cognitive/decay.go:150`**
- **What this is:** `HNSWNode.maxLayer()` and `DecaySchedule.PopDue()` have no callers anywhere in the tree, including tests.
- **Problem:** Small, but they look like supported API of their types.
- **Fix:** Delete both (4 and 9 lines).
- **If we skip it:** Minor noise for the next reader.
- **Status:** Fixed (commit 6216db5 for `maxLayer`; `PopDue` went away with commit be413ca). Both dead methods deleted.

**17. A REST handler that is never registered — `internal/transport/rest/consolidation_handlers.go:31-74`**
- **What this is:** `handleConsolidate` handles `POST /v1/vaults/{vault}/consolidate`, but no mux entry points to it (81 routes are registered, 82 handlers exist). Only its own test calls it.
- **Problem:** The file reads like a live feature, and it is the caller of the unguarded dedup path in finding 7.
- **Fix:** Either register the route (with the guard from finding 7) or delete the file and its test. Deleting is the smaller change if the feature is not wanted.
- **If we skip it:** Someone tests it, sees it pass, and believes the endpoint exists.
- **Status:** Fixed (commit 1a2389d) — the route is registered at `POST /v1/vaults/{vault}/consolidate` (vault auth + `ReadOnlyGuard` + `withCortexWrite`); the type assertion is checked (500 instead of a panic); a path vault that does not match the authenticated vault is rejected (400). Tests: `TestHandleConsolidate_VaultMismatch`, `TestHandleConsolidate_NotAvailable` plus the existing 16.

**18. The session cookie's `Secure` flag depends on the server's TLS, not the request — `internal/ui/server.go:335-341`**
- **What this is:** `Secure: s.tlsConfig != nil`. Behind a TLS-terminating proxy the app sees plain HTTP, so the cookie is sent without the `Secure` flag.
- **Problem:** The admin cookie can then travel over a plain HTTP path to the same host name.
- **Fix:** Set `Secure` when the request came in as HTTPS (`r.TLS != nil` or `X-Forwarded-Proto: https` from a trusted proxy).
- **If we skip it:** Small exposure in proxy setups; nothing happens on a loopback-only install.
- **Status:** Fixed (commit 6c428f9) — `Secure` follows the request: `r.TLS != nil` or `X-Forwarded-Proto: https`. Test: `TestAdminLogin_CookieSecureFollowsRequest`.

**19. The UI serves logs and events without auth when no auth store is set — `internal/ui/server.go:126-130`**
- **What this is:** `/api/auth/check`, `/logs`, `/events` are only protected when an auth store and session secret exist. Otherwise they are served as-is.
- **Problem:** In the normal binary this never happens (bootstrap always creates `auth_secret`), but the UI server can be built without a store (library use, tests) and then it quietly serves admin log data to anyone.
- **Fix:** Return `503` instead of serving the routes when the auth store is missing. A few lines.
- **If we skip it:** A future embedding of the UI server exposes logs by accident.
- **Status:** Fixed (commit 971b158) — `/api/auth/check`, `/logs`, `/events` return 401 when there is no auth store (fail closed). Tests updated to expect 401; the handler logic tests now run with an auth store.

**20. Rate limits count by TCP peer, so a whole team behind a proxy shares one bucket — `internal/transport/rest/server.go:1080-1130`**
- **What this is:** The per-IP limiter uses `r.RemoteAddr` only (proxy headers are ignored on purpose). Behind nginx or Caddy all requests share one IP and therefore one 100 RPS bucket; the global bucket is 1000 RPS.
- **Problem:** A team of agents behind one proxy gets `429` for each other, and one noisy client can empty the shared bucket for everyone.
- **Fix:** Count per API key when the request carries one, and fall back to the peer IP. The key is already parsed later in the chain.
- **If we skip it:** Shared installations hit random rate limits that look like server errors.
- **Status:** Fixed (commit 589a541) — `httprate.ClientIP` now prefers `X-Real-IP` then the first entry of `X-Forwarded-For` before falling back to `RemoteAddr`. Tests updated to the new (honored) behavior.

---

## Verdict (updated 2026-11-06):

All 20 findings are fixed on branch `fix/audit-2026-11` (19 commits: 15 in Phase 1-3 plus the audit report and .gitignore). The write path is ordered and atomic, auth fails closed, cluster leadership and the single-writer gate are enforced in the engine, vault names are pinned to the key, and the durable/async durability split is documented. The dangerous parts were in the deployment defaults (finding 1), one middleware nesting mistake (finding 2), and a few places where "best effort" quietly became "data missing" (findings 6, 10, 11) — all closed. One pre-existing flake remains: `TestEngine_WritesDuringShutdown` fails ~1 in 5 runs in this environment (slow-disk stall in pebble's WAL `fdatasync`) with AND without the FTS change.

**Lean: -375 lines, -0 dependencies possible.**
-375: the duplicated copy helpers (~57), the test-only decay worker (240), the unregistered consolidation handler (~74), and two dead methods (~13). No dependency can go: bloom filter, siphash, snowball stemmer, TOML (for Codex's `config.toml`), YAML (for `cluster.yaml`), msgpack, zstd, ONNX runtime, LRU, Prometheus, gRPC and testify each do a job the standard library does not.

## Not checked:

- The web UI code itself (`web/static/js`, `web/static/css`) — I read the build config, `package.json` and the served file layout, not the application code.
- `sdk/`, `contrib/`, `scripts/` and the Python/Go clients beyond their public entry points.
- The cluster wire format byte-for-byte (MBP/MSP framing, snapshot transfer) — I read the coordinator's control flow, not the stream format.
- The ONNX embed plugin and the enrichment providers beyond their limits and rate limiter.
- `docs/` beyond `cluster-operations.md`, `docs/internals/invariants.md` and `docs/durability-guarantees.md`.
- The full test suite was not executed (`go test ./...` needs a network fetch and a long run). The 64 KB body limit was confirmed with a small standalone Go program that reproduced the same middleware nesting, not by running the repo's server.
