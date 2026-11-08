# AUDIT-ROUND3 — Verbleibende Pakete

**Datum:** 2026-11-08 · **Branch:** `fix/audit-2026-11` · **Modus:** Read-only-Audit,
keine Fixes. Methodik: parallele Subagenten (je Lane Voll-Lektüre mit file:line-Belegen),
kritische Claims vom Hauptagenten per Read/Grep nachverifiziert ([checked]).

Ergebnis-Quelle im Detail: `.planning/2026-11-08-audit-round3-remaining/findings.md`.

## Scope

| Lane | Paket | Status |
|------|-------|--------|
| A | internal/transport/rest | ✅ voll |
| B | internal/auth + internal/config | ✅ voll |
| C | internal/transport/mbp | ✅ voll (Gap: cluster_frames.go) |
| D | cmd/muninn (server/lifecycle/logwriter + wal/mol.go-Teil) | ✅ voll |
| D2 | cmd/muninn Rest | ⚠️ Sampling (12 Dateien nicht voll gelesen) |
| E | internal/index (non-hnsw) | ✅ voll (3 Dateien, zwei unabhängige Audits) |
| F | sdk/web | ❌ offen |

## Zusammenfassung

**19 Findings: 2 high, 6 medium, 11 low.** Die beiden high finden sich beide im
mbp-Transport (unauth OOM-DoS-Kette). rest/ und auth/config sind bemerkenswert sauber;
Round-1-Fixes verdrahtet korrekt.

## High

- **C1 · internal/transport/mbp/compress.go:101-107** [checked] — zstd decompression
  bomb: `dec.DecodeAll(data, nil)` materialisiert die komplette Ausgabe; der
  100-MB-Cap-Check (`len(decoded) > maxDecompressedSize`) läuft erst danach. Kleiner
  Frame → GB-Allocation. Fix: `zstd.WithDecoderMaxMemory` / deklarierte Größe vorab
  prüfen, vor Allokation ablehnen.
- **C2 · internal/transport/mbp/server.go:157 (+frame.go)** [checked] — pre-auth
  amplification: `ReadFrame(conn)` ist der erste Schritt in `handleConnection`;
  `make([]byte, frame.PayloadLength)` bis MaxPayloadSize (16 MB) aus attacker-
  kontrolliertem Length-Feld, vor jeder Auth. Das Wire-Limit greift korrekt VOR der
  Allokation (frame.go:134-137 [checked]), aber 16 MB/Verbindung ist für
  unauthentifizierte Verbindungen zu großzügig. Fix: kleines Pre-Auth-Frame-Limit
  (HELLO braucht wenig), Verbindung bei Oversize schließen.
  Kombiniert mit C1: unauth OOM-DoS-Kette.

## Medium

- **B1 · cmd/muninn/server.go:928-933** [checked] — `anyNonLoopbackAddr` macht
  `continue` bei `net.SplitHostPort`-Fehler statt fail-closed. Derzeit unreachable
  (einziger Caller :1281, Adressen davor validiert :1118) → pure defense-in-depth.
  Fix: Parse-Fehler = exposed, oder Startup-Abbruch.
- **C3 · internal/transport/mbp/server.go:313-324** [session] — `dispatchFrame`
  dekomprimiert `FlagCompressed`-Frames auch für `AuthMethod="none"`-Sessions, vor
  per-handler auth/scope. Mit C1: unauth CPU/Mem-Amplification. Fix: Dekompression
  erst nach Auth bzw. hartes compressed-input-cap.
- **C4 · internal/transport/mbp/server.go:582-586** [checked] — `queueFrame`:
  `select { case writeCh <- frame: default: }` → silent drop bei vollem 256-slot
  writeCh (Kommentar: "should not happen"). Verlorene Responses unter Last.
  Fix: block+ctx oder Fehler; mindestens loggen/zählen.
- **M1 · cmd/muninn/process_windows.go:58** [session] — `isPebbleLockHeld` immer false
  auf Windows → Backup-Pre-Flight gegen laufenden Daemon umgangen. Linux-Hosts
  un betroffen. Fix: LOCK-File-Probing oder dokumentierte Limitation.
- **E1 · internal/index/fts/fts.go:818-823** [session×2] — `getIDF` leakt Pebble
  closer bei kurzem TermStats-Wert (early return vor `closer.Close()`) → Resource-
  Leak bei wiederholten Suchen mit korrupten/kurzen Einträgen.
- **E2 · internal/index/adjacency/adjacency.go:57-61** [session×2; zweites Audit:
  medium wegen STO-11/STO-12-Bug-History der kanonischen Funktion] — hand-rolled
  `prefixSuccessor` dupliziert `keys.PrefixUpperBound`. Fix: kanonische Funktion
  nutzen, Helfer löschen (Byte-Äquivalenz vorher prüfen).

## Low

- **B2 · internal/auth/bootstrap.go:53-56** [session] — nur `""` wird zum Default
  normalisiert; `MUNINN_ADMIN_PASSWORD=" "` umgeht Default-Guard. Fix: `TrimSpace==""`.
- **B3 · internal/auth/bootstrap.go:99-102** [session, uncertain] — post-hoc-Check
  deckt Whitespace/empty env auf exposed binds nicht ab.
- **B4 · internal/auth/middleware.go:29** [session, uncertain] — `TrimPrefix("Bearer ")`
  statt `ParseBearerToken`; Konsistenz-Nit.
- **D2 · cmd/muninn/server.go:1294** [session, uncertain] — `defer mol.Close()`
  parallel zur geordneten Sequenz (store.Close/gc.Stop :1933-1936) → potential
  double-close; Idempotenz von `wal.MOL.Close` unverifiziert.
- **D3 · cmd/muninn/server.go:1881-1944** [checked] — Shutdown-Cutoff
  `time.After(30*time.Second)` → `os.Exit(1)`. GroupCommitter-Drain existiert
  (internal/wal/mol.go:476-506 dränct auf ctx.Done UND done), aber Stall (großer
  WAL-Tail, langsamer fsync) schneidet den Tail ab → Round-2-These „silent tail
  loss at shutdown" REAL, aber auf Stall-Fälle begrenzt.
- **D4 · cmd/muninn/upgrade.go:331** [session, uncertain] — Release-Tag ungeprüft in
  URL. Fix: `^v?[0-9].*` validieren.
- **D5 · cmd/muninn/backup.go:72** [session, uncertain] — Stat/MkdirAll TOCTOU,
  lokal vernachlässigbar.
- **E3 · internal/index/fts/fts.go:704-708** [session, uncertain] — hand-rolled
  upper bound (Separator-Byte 0x01) statt keys.PrefixUpperBound; korrekt für
  heutiges Layout, fragil bei Formatänderung.
- **E4 · internal/index/fts/worker.go:200-205** [session] — `Worker.Stop` nicht
  idempotent (unguarded close stopCh/done → double-close panic bei zweitem Stop).
- **E5 · internal/index/adjacency/adjacency.go:105** [session] — `WriteAssociation`
  derefereziert nil assoc ohne Check.
- **E6 · internal/index/adjacency/adjacency.go:130** [session, uncertain] —
  `pebble.NoSync` bei Assoziation-Writes → unclean shutdown kann frische Edges
  verlieren (evtl. bewusster Perf-Trade-off; dokumentieren oder Sync).

## Round-2-Resolutionen

- **wsVaultID-Prefix-Collision** (Round 2, „transport/ws"): Symbol verortet in
  `internal/engine/engine.go:4266-4268` [checked] — `binary.BigEndian.Uint32(ws[:4])`,
  ~1.16 % Kollision bei 10k Vaults, nur sweep-Pfad betroffen; eigenes Doc-Kommentar
  (:4258-4265) nennt Langzeit-Fix (byVault auf volles [8]byte). `internal/transport/ws`
  existiert nicht.
- **SIGHUP unconditionally** (Round 2): weitgehend entschärft — mit `--log-file`
  Rotation, ohne dokumentierter No-op+WARN (logwriter.go:100-121) [session].
- **Tail loss at shutdown** (Round 2 E1): REAL, aber eng — Drain läuft (mol.go:476-506),
  Verlustfenster nur wenn store.Close()/gc.Stop() >30 s stallen (D3).

## Verified correct (Auswahl, vollständige Liste in findings.md)

- rest: Middleware-Ketten vollständig, MaxBytesReader (64 KB/32 MB/512 MB), CORS
  allowlist-only, SSE hinter WriteOnlyGuard + deferred Unsubscribe, keine
  unauth amplification.
- auth/config: constant-time compares, expiry enforcement, keine Plaintext-Secrets,
  GetVaultConfig fail-closed, IPv6-Loopback korrekt, kein XFF-Trust, Parsefehler
  propagieren. Round-1-Fixes (body limits, bootstrap-Guards) korrekt verdrahtet.
- mbp: HELLO-only pre-auth, ValidateAPIKey+scopeVault vor engine.Hello, Wire-Limit
  vor Allokation, scopeVault fails closed, Frame-Semaphore 64.
- cmd: Shutdown-Reihenfolge eng.Stop→hebbian→transition→store.Close→gc.Stop korrekt,
  Coordinator-Stop zuerst.
- index: Locks um DF-RMW, kein cross-vault IDF-Contamination, BFS-Bounds,
  kein 4-Byte-Präfix-Keying.

## Gaps (Bewertung: schlecht genug?)

- mbp/cluster_frames.go (235 LOC) ungelesen — Cluster-Frame-Pfad offen.
- cmd/muninn-Rest war Sampling: init.go (1347), setup_ai.go (912), repl_client (530),
  status (510), exec (233), envfile (364), api_key (356), remember (331), doctor (300),
  mcp_stdio (220), repl (406), help (427) nicht voll gelesen.
- sdk/web: nicht auditiert.

## Prioritäten (Fix-Reihenfolge)

1. **C1+C2+C3** (mbp unauth OOM-Kette) — kleinster diff mit größtem Effekt.
2. **B1** (fail-closed exposure detection) — Ein-Zeilen-Fix.
3. **C4** (silent response drop) — observability zuerst.
4. **B2** (whitespace admin password) — Ein-Zeilen-Fix.
5. **E1** (closer leak), **E4** (Stop-Idempotenz), **E2** (prefixSuccessor).
6. **M1, D3, D2, D4, D5, E3, E5, E6** — nach Relevanz.
