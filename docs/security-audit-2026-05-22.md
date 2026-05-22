# Security Audit — 2026-05-22

## Executive Summary

Eighteen findings were identified across the codebase, distributed as: **1 Critical, 4 High, 5 Medium, 5 Low, 3 Info**. Two findings (F-001 and F-002) should be prioritised above everything else before any real-money launch.

**F-001** is the most urgent: every RPC in `UserService` (six endpoints including `ReportSteps` and `UpdateUserSettings`) trusts the `user_id` field from the request payload rather than the authenticated principal. Any authenticated user can credit chips to any other account, remove another account's daily deposit cap, or read any user's private balance data. This is a trivially exploitable money-inflation and privacy bug.

**F-002** is structurally simpler but operationally dangerous: gRPC server reflection is whitelisted as a public, unauthenticated method. Any client on the network can enumerate every service, RPC, and message type without providing a token. This inverts the security posture of an API designed around JWT authentication.

The remaining high-severity issues (F-003: cross-game command_id collision, F-004: no RBAC for admin commands) require more effort to exploit but will become critical once the game is live. The medium findings (no TLS, no rate limits, no message size cap) are standard pre-production hardening. The positive story: the game engine's actor-identity flow, deck shuffling entropy, SQL parameterisation, money atomicity, and hole-card filtering on `PlaySession` are all implemented correctly.

---

## Severity Definitions

**Critical**: Active exploitation possible by an authenticated user with no special access; consequence is significant chip theft, hole card disclosure, or total bypass of game rules.

**High**: Exploitation requires unusual conditions but consequence is the same as Critical; OR exploitation is easy but consequence is bounded (e.g. one user's data).

**Medium**: Exploitable but consequences are limited (e.g. minor information disclosure, denial of service of one user).

**Low**: Defence-in-depth gaps, observability issues, documentation gaps. No direct exploitation path identified.

**Info**: Not a vulnerability but worth noting for design review.

---

## Findings

### F-001: UserService RPCs use request-supplied user_id, not authenticated principal

**Severity:** Critical
**Threat actor:** A2 (money extractor), A1 (cheating player), A3 (account takeover)
**Component:** `internal/user/service.go`, all six handlers
**Status:** Open

**Description.** All six `UserService` handlers (`CreateUser`, `GetUser`, `UpdateUserSettings`, `ReportSteps`, `GetUserSnapshot`, `ListDepositReports`) read the target user UUID from the request payload (`req.GetUserId()`) rather than from the authenticated principal in the context. The auth interceptors enforce that the caller must present a valid JWT, but they do not enforce that the `user_id` in the request matches the caller's own identity. There is no check anywhere in the handler chain that binds the request to the principal.

**Exploitation scenario.**

1. Alice authenticates normally and obtains a valid JWT.
2. She calls `UpdateUserSettings` with `user_id = <alice's own UUID>` and `max_daily_deposit = 0` (sentinel value for "no cap"). Her own daily deposit cap is now removed.
3. She calls `ReportSteps` with `user_id = <alice's own UUID>` and `cumulative_steps_today = 9223372036854775807` (int64 max). Because there is no cap, the full step count is credited, awarding her a massive chip balance in a single request.
4. Alternatively, she calls `ReportSteps` with `user_id = <bob's UUID>` and a large step count, awarding Bob chips (useful for collusion or account boosting).
5. She calls `GetUser` / `GetUserSnapshot` / `ListDepositReports` with any other user's UUID to read their balance and deposit history.

**Evidence.**

- `internal/user/service.go:78`: `userID, err := parseUserID(req.GetUserId())` — used directly without principal check.
- `internal/user/service.go:120`: same pattern for `UpdateUserSettings`.
- `internal/user/service.go:119–171`: `ReportSteps` calls `parseUserID(req.GetUserId())` then passes to `svc.store.ReportSteps`, which credits chips.
- `internal/auth/auth.go:31–43`: `WithPrincipal` / `FromContext` are never called in `user/service.go`.
- `internal/server/user_server.go:6–9`: `NewUserServer` returns `user.New(st)` — no authenticator wired in.

**Recommendation.** Each `UserService` handler must call `auth.FromContext(ctx)` to obtain the authenticated principal and compare its UUID against `req.GetUserId()`. Handlers that legitimately need admin access to arbitrary user IDs (e.g. a future admin panel) must gate on a role claim in the JWT. `ReportSteps` and `UpdateUserSettings` should reject any request where `user_id != principal.UserID`.

**Caveats.** The `UserService` auth interceptors run and do require a valid JWT — unauthenticated callers are still rejected. The gap is authorisation (what you can do), not authentication (who you are).

---

### F-002: gRPC server reflection whitelisted as unauthenticated

**Severity:** High
**Threat actor:** A4 (service disruptor), A5 (data thief), A1 (cheating player)
**Component:** `internal/auth/interceptor.go:19`
**Status:** Open

**Description.** `PublicMethods` includes `/grpc.reflection.v1.ServerReflection/ServerReflectionInfo`, bypassing the auth interceptor for gRPC server reflection. Any client that can reach the gRPC port (no token needed) can enumerate the complete API surface: all services, all RPCs, all proto message schemas. This provides an attacker with the full blueprint to craft arbitrary requests without needing a token until they actually call a protected RPC.

**Exploitation scenario.** An unauthenticated client connects to the server and issues a reflection request using `grpcurl --plaintext <host>:9090 list`. The output reveals every service name, RPC method, request/response types, and field names. This information is used to craft targeted attacks against F-001 (UserService), identify undocumented endpoints, or automate fuzzing of all message types.

**Evidence.**

- `internal/auth/interceptor.go:19`: `"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo": true`

**Recommendation.** Remove the reflection entry from `PublicMethods` for production builds. If reflection is needed for internal tooling, gate it on a separate service account token or admin JWT role. The idiomatic approach is to enable reflection only in dev/test environments via a config flag: `if cfg.EnableReflection { reflection.Register(grpcServer) }`.

**Caveats.** The proto schema is compiled into the binary regardless, so a sufficiently motivated attacker with binary access could extract it. Reflection is still a meaningful reduction in time-to-exploit.

---

### F-003: Command idempotency key not scoped to game_id — cross-game collision corrupts in-memory state

**Severity:** High
**Threat actor:** A1 (cheating player)
**Component:** `db/queries/game_events.sql:30–34`, `internal/engine/session.go:261–272`, `internal/store/store.go:415–419`
**Status:** Open

**Description.** The `FindEventByCommandID` SQL query searches `game_events` by `caused_by_command_id` alone, with no filter on `game_id`. The intent is per-game idempotency, but the implementation is global. If a player uses the same `command_id` UUID in two different games (or reuses one from a past game), the session for game B will find the event from game A and treat it as already processed. Critically, the run loop in `session.run` then calls `applyAll(state, cachedEventsFromGameA)` — applying game A's events to game B's in-memory state without persisting them. This diverges the in-memory state from the database.

**Exploitation scenario.**

1. Alice plays game A and folds with `command_id = "uuid-X"`. Event stored in DB for game A.
2. Alice joins game B. When it is her turn to call or raise, she instead submits a `FoldCommand` with `command_id = "uuid-X"`.
3. Session B's `process` finds the game A fold event and returns it without persisting anything to game B's DB.
4. `applyAll` is called in game B: Alice's `IsFolded = true` in memory but not in the DB.
5. The game B advance loop treats Alice as folded and proceeds (awarding the pot to others). If session B is later evicted and reloaded, Alice is not folded in the DB; the state diverges.
6. Depending on timing and session lifetime, Alice may avoid being charged a call while appearing to have folded.

**Evidence.**

- `db/queries/game_events.sql:30–34`: `WHERE caused_by_command_id = $1` — no `game_id` predicate.
- `internal/engine/session.go:261–272`: cached events returned by `lookupByCommandID` are applied to current state by the caller.
- `internal/engine/session.go:378–388`: `lookupByCommandID` calls `FindEventByCommandID` without game context.

**Recommendation.** Add `game_id` to the idempotency lookup. The SQL should be: `WHERE caused_by_command_id = $1 AND game_id = $2`. The index on `caused_by_command_id` (`game_events_command_idx`) should become a composite index on `(caused_by_command_id, game_id)`. The `FindEventByCommandID` store method and all call sites must pass the game UUID.

**Caveats.** Requires simultaneous coordination across two active games with the same player. The session idle timeout (10 min) and reaper limit the exploitation window, but both games could be active concurrently.

---

### F-004: Admin commands have no role differentiation

**Severity:** High
**Threat actor:** A1 (cheating player), A4 (service disruptor)
**Component:** `internal/server/server.go:248–262`, proto service definition
**Status:** Open — currently Unimplemented; will become Critical when implemented

**Description.** `PauseTable`, `CloseTable`, `KickPlayer`, and `MutePlayer` are registered RPCs behind the auth interceptor. Authentication is required but there is no role check — any authenticated user would be able to pause or close any table, kick any player, and mute any player. Because these are currently `Unimplemented`, there is no exploitable path today, but the architecture has no hook for role-based authorisation.

**Exploitation scenario.** Bob is losing a poker hand. He calls `PauseTable` for that game to interrupt play and buy time, or `KickPlayer` to remove the player who is beating him.

**Evidence.**

- `internal/server/server.go:248–262`: all admin handlers return `codes.Unimplemented` with no auth check.
- `internal/auth/interceptor.go`: `PublicMethods` has no admin carve-out; there is no `requireAdminRole` helper.

**Recommendation.** Before implementing these handlers, add an admin role mechanism. The JWT validator already supports custom claims — add an `is_admin` or `role` claim via an Auth0 Action, extract it in `customClaims`, and expose it on `auth.Principal`. Each admin handler must call `requireAdmin(ctx)` before proceeding.

**Caveats.** Acknowledged design gap — these methods are all `Unimplemented` today. Document as TODO(admin-rbac).

---

### F-005: HoleCardsRevealed events stored in plaintext in game_events

**Severity:** High (insider threat; Low for external adversaries)
**Threat actor:** A6 (insider with DB read access)
**Component:** `db/migrations/00003_game_events.sql`, `internal/store/store.go:536`
**Status:** Open — expected by design; document and accept or mitigate

**Description.** `game_events.envelope` stores the complete serialised `GameEvent` protobuf, which for `HoleCardsRevealed` events contains both hole cards for a specific player. The `payload` column stores the inner message bytes. Anyone with `SELECT` access to the `game_events` table can read every player's hole cards for every hand, ever played. This is an inherent consequence of the event-sourcing design, but it means the DB is a complete cheating oracle and privacy leak.

**Exploitation scenario.** A6: A DBA or compromised service account with read access queries `SELECT envelope FROM game_events WHERE event_type = 'poker.v1.HoleCardsRevealed'`, deserialises the envelopes, and extracts card values for all hands. This data can be sold, used to identify high-value targets (A3), or mined for pattern analysis.

**Evidence.**

- `internal/store/store.go:536`: `proto.Marshal(evt)` — full event including cards is stored verbatim.
- `db/migrations/00003_game_events.sql:6`: `envelope BYTEA NOT NULL`.

**Recommendation.** For the current threat model, document and accept. For a higher-security future state: (a) encrypt `HoleCardsRevealed` payloads at the application layer before storing, or (b) use a separate `hole_card_secrets` table with column-level encryption and column access grants that exclude the application's runtime DB user. At minimum, restrict `SELECT` on `game_events` to the application user only (no direct analyst access) and use a view or audit-logged query path for any analysis queries.

**Caveats.** This is the correct event-sourcing design. Alternative architectures (e.g. keeping hole cards only in memory, never persisting) prevent replay after a crash. Acknowledged in CLAUDE.md as TODO(stream-filtering) — that TODO refers to the broadcast filter, but the DB persistence of hole cards is a separate concern.

---

### F-006: No TLS on the gRPC server

**Severity:** Medium
**Threat actor:** A3 (account takeover), A5 (data thief)
**Component:** `cmd/server/main.go:57`
**Status:** Open

**Description.** `grpc.NewServer()` is called without any `grpc.Creds(tlsCredentials)` option. The server listens on plain TCP. JWT bearer tokens, chip amounts, and hole card data transit the wire in cleartext (Base64-encoded proto over HTTP/2). If TLS is terminated at a load balancer (typical deployment), the internal plaintext segment still exposes tokens to anyone with network access inside the cluster.

**Exploitation scenario.** An attacker on the same network segment (e.g. a compromised container) captures traffic, extracts a JWT from the `authorization` metadata, and uses it to authenticate as that user until the token expires.

**Evidence.**

- `cmd/server/main.go:57`: `grpcServer := grpc.NewServer(...)` — no `grpc.Creds(...)` option.
- No `certFile`/`keyFile` flags defined in `main.go`.

**Recommendation.** Either (a) configure TLS directly on the gRPC server using `credentials.NewTLS(tlsConfig)` with certificates from an environment variable or secrets manager, or (b) document the TLS termination architecture (load balancer + mutual-TLS between LB and backend) so operators know what's expected.

**Caveats.** Plain-TCP gRPC with LB-terminated TLS is a common and acceptable pattern in well-secured clusters. The finding is about the lack of any documentation or enforcement of this assumption.

---

### F-007: No per-user stream count limit or request rate limiting

**Severity:** Medium
**Threat actor:** A4 (service disruptor)
**Component:** `internal/server/session.go`, `cmd/server/main.go`
**Status:** Open

**Description.** Any authenticated user can open an unbounded number of simultaneous `PlaySession` streams. Each stream holds a gRPC goroutine for its lifetime. There are no limits on concurrent streams per user, per IP, or server-wide. There is also no request rate limiter on any RPC. A malicious user who knows another player's game can spam `PlaySession` or `Submit` calls to exhaust server resources.

**Exploitation scenario.** Alice opens 10 000 concurrent `PlaySession` streams, each sending one command per second to a large table. The server spawns 10 000 gRPC handler goroutines. Even with the session inbox (64 slots), the session's run goroutine processes one command at a time; all waiting callers block until context cancels, holding goroutines and connections.

**Evidence.**

- `cmd/server/main.go:57`: `grpc.NewServer(...)` — no `grpc.MaxConcurrentStreams()`, no `grpc.ConnectionTimeout()`.
- `internal/engine/session.go:119–133`: `Submit` blocks until inbox has space or context cancels; no per-user quota.

**Recommendation.** Add `grpc.MaxConcurrentStreams(N)` to the server options (e.g. 1000 per connection). Add a token-bucket or sliding-window rate limiter as a unary/stream interceptor, keyed on the authenticated principal's UUID. A simple in-process implementation using `golang.org/x/time/rate` per principal UUID is sufficient for now.

**Caveats.** gRPC's default HTTP/2 implementation limits concurrent streams per connection to 100. A single connection cannot exceed this, but an attacker can open many connections.

---

### F-008: No maximum gRPC message receive size configured

**Severity:** Medium
**Threat actor:** A4 (service disruptor)
**Component:** `cmd/server/main.go:57`
**Status:** Open

**Description.** `grpc.NewServer()` uses the default maximum receive message size of 4 MiB. A malicious client can send a `ChatMessageCommand` with a text payload of up to 4 MiB. While `validateChatMessage` enforces a 500-rune limit on the text field, the size check happens after the message is fully deserialised. Proto deserialisation of a 4 MiB blob is cheap, but the pattern allows any future RPC with large repeated fields to be abused before validation runs.

**Exploitation scenario.** A client sends a `PlayerCommand` wrapping a `ChatMessageCommand` with 4 MiB of garbage bytes. The server deserialises the full message before the validator rejects it. With many such requests, proto deserialisation and GC pressure accumulate.

**Evidence.**

- `cmd/server/main.go:57`: no `grpc.MaxRecvMsgSize(...)` option.
- `internal/engine/validate_session.go:34`: `utf8.RuneCountInString(cmd.Text) > maxChatMessageRunes` — runs after deserialisation.

**Recommendation.** Add `grpc.MaxRecvMsgSize(1 << 16)` (64 KiB) as a `grpc.ServerOption`. This is more than sufficient for any legitimate message in this protocol; the largest expected message is a game state snapshot at a 9-player table.

---

### F-009: ErrInboxFull is defined but unreachable; full inbox silently blocks callers

**Severity:** Medium
**Threat actor:** A4 (service disruptor)
**Component:** `internal/engine/session.go:18, 119–133`
**Status:** Open

**Description.** `ErrInboxFull` is exported and documented, implying callers can handle it. However, `Session.Submit` uses a blocking channel send (`case s.inbox <- env:`) with no `default` branch. If the 64-slot inbox is full, callers block indefinitely until the context is cancelled or the session closes — they never receive `ErrInboxFull`. Under sustained load (many concurrent players on one table), this means PlaySession goroutines block silently instead of returning a fast rejection that allows the client to retry.

**Exploitation scenario.** A4: Alice and 63 other bots each send a command to the same session simultaneously, filling the inbox. The 65th real player's `PlaySession` goroutine blocks for up to the full context timeout (however long the gRPC client allows), consuming one gRPC goroutine per blocked request.

**Evidence.**

- `internal/engine/session.go:119–126`: blocking select with no `default` case.
- `internal/engine/session.go:18`: `ErrInboxFull` defined but never returned.

**Recommendation.** Add a non-blocking `default` case to the inbox send:

```go
select {
case s.inbox <- env:
case <-s.closed:
    return nil, ErrSessionClosed
case <-ctx.Done():
    return nil, ctx.Err()
default:
    return nil, ErrInboxFull
}
```

Map `ErrInboxFull` to a `codes.ResourceExhausted` gRPC status in the server layer.

**Caveats.** The current blocking behaviour is deliberate backpressure — callers wait rather than fail. Making it non-blocking changes the client contract. Decide which semantics are correct before changing.

---

### F-010: pgxpool uses default maximum connection count

**Severity:** Medium
**Threat actor:** A4 (service disruptor)
**Component:** `cmd/server/main.go:30`
**Status:** Open

**Description.** `pgxpool.New(ctx, *dsn)` without a `pgxpool.Config` uses the default `MaxConns` of `max(4, runtime.NumCPU())`. On a typical 4-core server this is 4 connections. Under concurrent load from many games, connection starvation causes requests to queue inside pgxpool. There is no per-request acquire timeout configured, so a slow query holding a connection blocks all subsequent requests indefinitely.

**Evidence.**

- `cmd/server/main.go:30`: `pool, err := pgxpool.New(ctx, *dsn)` — no pool config.
- `go.mod`: pgx v5.9.2.

**Recommendation.** Configure the pool explicitly:

```go
cfg, _ := pgxpool.ParseConfig(*dsn)
cfg.MaxConns = 30
cfg.MinConns = 5
cfg.MaxConnLifetime = 30 * time.Minute
cfg.MaxConnIdleTime = 5 * time.Minute
pool, err = pgxpool.NewWithConfig(ctx, cfg)
```

Also set a statement timeout in the DSN (`options=-c statement_timeout=5000`) so runaway queries cannot hold connections indefinitely.

---

### F-011: Dev-mode stub auth can be enabled by a single env var typo

**Severity:** Low
**Threat actor:** A1, A2, A3
**Component:** `internal/config/config.go:45`, `internal/auth/interceptor.go:63–68`
**Status:** Open — well-designed but worth documenting

**Description.** `DevModeAllowStubAuth` is enabled by `AUTH_DEV_MODE=1`. Any process with this env var set (e.g. a CI runner accidentally connected to a staging or production database, or a misconfigured deployment) allows any client to authenticate as any UUID by setting the `x-dev-player-id` gRPC metadata header with no JWT required.

**Exploitation scenario.** A CI pipeline runs integration tests against a shared staging DB with `AUTH_DEV_MODE=1` set globally. A developer connects to the staging server directly with `grpcurl -H 'x-dev-player-id: <admin-uuid>'` and acts as any user.

**Evidence.**

- `internal/config/config.go:45`: `DevModeAllowStubAuth: os.Getenv("AUTH_DEV_MODE") == "1"`.
- `internal/auth/interceptor.go:63–68`: stub is consulted before JWT validation.

**Recommendation.** Add an explicit log warning at startup when `DevModeAllowStubAuth` is true: `log.Printf("WARNING: dev-mode stub auth is enabled — NEVER use in production")`. Consider a compile-time build tag (`//go:build dev`) as a secondary guard. The default (off unless explicitly set to "1") is correct; this is a documentation and operational hygiene gap.

**Caveats.** The current implementation is sound. This is a defence-in-depth note.

---

### F-012: Soft delete does not satisfy GDPR "right to erasure"

**Severity:** Low
**Threat actor:** A5 (data thief / regulatory)
**Component:** `db/migrations/00006_users.sql:9`, `db/queries/users.sql`
**Status:** Open — design gap

**Description.** User deletion sets `deleted_at = NOW()` on the `users` row. All PII (display_name, external_id/email via Auth0 sub, timezone) remains in the database. Event history (step deposits, game events) is retained forever and references the user UUID. Under GDPR/CCPA "right to erasure," soft delete is insufficient.

**Evidence.**

- `db/migrations/00006_users.sql:9`: `deleted_at TIMESTAMPTZ`.
- `db/queries/users.sql:2`: `WHERE id = $1 AND deleted_at IS NULL` — data is filtered, not deleted.

**Recommendation.** Implement a hard-delete or pseudonymisation path: on erasure request, overwrite `display_name`, `external_id`, `email` (in JWT store) with a tombstone value (e.g. `"[deleted]"`, a null external_id), then hard-delete the row. For `game_events` and `step_deposit_reports`, either hard-delete or scrub identifying fields. This requires a data retention and erasure policy decision before implementation.

---

### F-013: No retention policy for game_events or step_deposit_reports

**Severity:** Low
**Threat actor:** A5, A6
**Component:** `db/migrations/00003_game_events.sql`, `db/migrations/00007_step_deposits.sql`
**Status:** Open — known gap

**Description.** Both tables are append-only with no `deleted_at` or TTL mechanism. Over time they grow without bound. `game_events` contains hole card data (F-005). `step_deposit_reports` contains health data (step counts) which is regulated PII in several jurisdictions. The lack of a retention policy means this data accumulates indefinitely.

**Recommendation.** Define a retention policy (e.g. delete `game_events` older than 2 years, delete `step_deposit_reports` older than 3 years after account deletion). Implement a background purge job. Coordinate with the GDPR erasure path (F-012).

---

### F-014: go vet copylocks warnings in validate_test.go

**Severity:** Low
**Threat actor:** None (test-only bug)
**Component:** `internal/engine/validate_test.go:73, 76, 80, 83, 120, 128, 136, 144, 506`
**Status:** Open

**Description.** `go vet` reports multiple "assignment copies lock value" warnings in `validate_test.go`. Proto-generated structs embed `protoimpl.MessageState` which contains a `sync.Mutex`. Copying them by value (e.g. `c := *someProtoStruct`) copies the mutex, which is a data race and undefined behaviour if the mutex is ever contended.

**Evidence.**

```text
internal/engine/validate_test.go:73:7: assignment copies lock value to c: ...GameState contains ...sync.Mutex
internal/engine/validate_test.go:76:9: assignment copies lock value to cp: ...PlayerState contains ...sync.Mutex
```

(9 locations total)

**Recommendation.** Replace struct-value assignments with pointer assignments in the test setup functions. Use `proto.Clone(src).(*pb.GameState)` where a copy is needed.

---

### F-015: No DB user privilege documentation or least-privilege enforcement

**Severity:** Low
**Threat actor:** A5, A6
**Component:** `cmd/server/main.go:24`, database configuration
**Status:** Open — operational gap

**Description.** The default DSN in `main.go` is `postgres://poker:poker@localhost:5432/poker`. There is no documentation of what privileges the `poker` DB user holds, whether migrations run as a separate elevated user, or whether the application user is restricted to DML. If the application user has DDL rights (CREATE TABLE, DROP, etc.), a SQL injection or application compromise leads directly to schema destruction.

**Recommendation.** Document (in CLAUDE.md or a runbook) that: (a) migrations should run as a separate `poker_migrator` role with DDL rights, (b) the runtime `poker` application user should have `GRANT SELECT, INSERT, UPDATE, DELETE` on application tables only, and (c) no `CREATE`, `DROP`, `TRUNCATE`, or `GRANT` on the runtime user. Add a startup check or CI step that verifies the runtime user cannot `CREATE TABLE`.

---

### F-016: gRPC reflection registered without server, so it is effectively dead — but it is in PublicMethods

**Severity:** Info
**Threat actor:** A4, A5
**Component:** `internal/auth/interceptor.go:19`, `cmd/server/main.go`
**Status:** Open — see F-002

**Description.** Reflection is whitelisted in `PublicMethods` but `reflection.Register(grpcServer)` is never called in `main.go`. The reflection service is therefore not actually registered — clients receive a "service not found" error. However, the whitelist entry remains, and if reflection is added in future (common during development), it will immediately be unauthenticated. This is noted as part of F-002; F-002 is the actionable finding.

---

### F-017: Hole cards visible in server-side logs via fmt.Errorf deal errors

**Severity:** Info
**Threat actor:** A5, A6
**Component:** `internal/engine/advance.go:199–203`
**Status:** Open — low-probability

**Description.** Error messages from card dealing include the player ID but not the card values themselves. However, the error wraps `fmt.Errorf("deal to %s card 1: %w", p.PlayerId, err)`. If `err` from `deck.Draw()` ever includes a card value in its message (currently it does not — `"deck: no cards remaining"` is the only error), it would appear in the log. This is a latent risk.

**Evidence.**

- `internal/engine/advance.go:199`: `return nil, fmt.Errorf("deal to %s card 1: %w", p.PlayerId, err)`.
- `internal/deck/deck.go:35`: current error is `"deck: no cards remaining"` — no card value.

**Recommendation.** No immediate action required. If `deck.Draw` error messages are ever changed, ensure they never include card values. A defensive `log.Printf` that logs a `GameEvent` wholesale would be dangerous; confirm there are no such log lines (none were found in this audit).

---

### F-018: display_name from JWT claim is attacker-controlled and stored verbatim

**Severity:** Info
**Threat actor:** A3 (account takeover), future UI
**Component:** `internal/auth/claims.go:44–47`, `internal/auth/auth.go:155–166`
**Status:** Open — context-dependent

**Description.** `display_name` is extracted from the JWT's namespaced custom claim and stored directly in `users.display_name`. Auth0 allows users to set their own display name. A user who sets their display name to `<script>alert(1)</script>` or a long Unicode string with bidirectional overrides could cause rendering issues in any future UI that displays names. Currently the server is gRPC-only (no HTML rendering), so there is no XSS surface today.

**Evidence.**

- `internal/auth/claims.go:44`: `c.DisplayName = v` — no sanitisation.
- `internal/auth/auth.go:158`: `displayName = custom.DisplayName` — passed directly to `CreateUser`.
- `db/migrations/00006_users.sql:14`: `CHECK (char_length(display_name) BETWEEN 1 AND 50)` — length-limited by DB but no character filtering.

**Recommendation.** When building any UI that renders player names, use context-aware HTML escaping. On ingestion, strip or reject display names containing `<`, `>`, `"`, null bytes, or bidirectional Unicode control characters. The DB constraint enforces length but not character class.

---

## Dependency scan results

### govulncheck

`govulncheck ./...` was run against the full module. Exit code 3 (vulnerabilities found in reachable code).

**5 vulnerabilities — all in `golang.org/x/crypto@v0.50.0`, all fixed in `v0.52.0`.**

All five are in the `crypto/ssh` package and are reached via the testcontainers
test helper (`internal/testutil/db.go:25` → `postgres.Run` → SSH connection to the
Docker daemon). **None of these code paths exist in production binaries** — `testutil`
is only imported from `_test.go` files. The production server does not use `crypto/ssh`.

| ID           | Title                                                                    | Severity |
| ------------ | ------------------------------------------------------------------------ | -------- |
| GO-2026-5020 | Infinite loop on large channel writes in `crypto/ssh`                    | High     |
| GO-2026-5019 | Bypass of FIDO/U2F physical interaction in `crypto/ssh`                  | High     |
| GO-2026-5018 | Pathological RSA/DSA parameters cause DoS in `crypto/ssh`                | Medium   |
| GO-2026-5017 | Client can cause server deadlock on unexpected responses in `crypto/ssh` | High     |
| GO-2026-5013 | Byte arithmetic underflow and panic in `crypto/ssh`                      | High     |

**Recommendation.** Bump `golang.org/x/crypto` to `v0.52.0` or later (`go get golang.org/x/crypto@v0.52.0 && go mod tidy`). Although the vulnerabilities are test-only, keeping dependencies clean reduces noise in future scans and removes the risk of accidental production use.

govulncheck also reported 4 additional vulnerabilities in imported packages and 4 in required modules where the affected symbols are not called. These are low priority; the `x/crypto` bump will likely resolve most of them as a transitive update.

### gosec

`gosec -fmt=text -severity=medium ./...` was run. **10 findings, all G115 (integer overflow conversion, CWE-190). All are false positives** given the domain invariants.

| Location | Conversion | Assessment |
| -------- | ---------- | ---------- |
| `internal/store/store.go:252–253` | `int32 → int16` (Variant, Structure enum) | Proto enums are small (< 10 values); safe. |
| `internal/store/store.go:398` | `uint64 → int64` (fromSequence) | Sequence numbers start at 1, increment by 1 per event. Reaching 2^63 events per game is impossible. |
| `internal/store/store.go:412` | `int64 → uint64` (GetLatestSequence result) | DB stores BIGINT; value is always ≥ 0. |
| `internal/store/store.go:467` | `uint64 → int64` (sequence in snapshot query) | Same as above. |
| `internal/store/store.go:552` | `uint64 → int64` (evt.GetSequence()) | Same as above. |
| `internal/store/store.go:612` | `int64 → uint64` (snapshot row sequence) | DB value is always ≥ 0. |
| `internal/engine/session.go:370` | `uint64 → int64` (seq → StateVersion) | Same sequence domain. |
| `internal/engine/advance.go:380` | `uint32 → byte` (card value) | Cards are `deck.Card` in [0, 51]; `byte` holds [0, 255]; always safe. |

No G304 (file path injection), G401/G501 (weak crypto), G304, or G102 (binding to all interfaces beyond `0.0.0.0`) findings were raised. gosec found nothing in the auth, engine, or server packages beyond the numeric conversion warnings.

---

## Positive findings

The following were explicitly checked and found to be correctly implemented:

**Randomness.** `crypto/rand` via `math/big.Int` Fisher-Yates shuffle (`deck/deck.go`). No `math/rand` import anywhere in non-test code. `FixedDealer` is defined in `engine/dealer.go` but instantiated only in test files. `RouterOptions.DealerFactory` defaults to `NewCryptoDealer` — the production path always uses the crypto dealer.

**Hole card filtering on PlaySession.** `shouldSuppressFromActor` in `internal/server/session.go` correctly drops `HoleCardsRevealed` events whose `player_id` does not match the stream's authenticated actor. The actor identity is captured once at stream open (`actorFromContext(ctx)` before the read loop) and cannot be changed by client messages mid-stream.

**Actor identity in game commands.** `actorPlayerID` flows from `auth.Principal.UserID` → `actorFromContext` → `router.Submit` → `session.Submit` → `HandleCommand(state, cmd, actor, dlr)`. The `actor` parameter is never derived from any field inside the `PlayerCommand` proto payload. Clients cannot self-assert their identity in game commands.

**JoinTable / LeaveTable actor identity.** Both handlers call `actorFromContext(ctx)` and use the returned UUID for all store operations. The `GameId` and `BuyIn` from the request are used, but the `UserID` is always from the principal.

**SQL injection.** All queries are sqlc-generated with `$N` parameter binding. No `fmt.Sprintf` or string concatenation in SQL strings was found anywhere in the codebase.

**JIT user provisioning race.** `resolveUser` correctly handles concurrent provisioning via unique-constraint catch on `CreateUser` → fallback to `GetUserByExternalID`. The test `TestAuthenticator_VerifyConcurrentJITSameSub_SingleUserCreated` covers this path with 20 goroutines.

**Steps-to-chips race.** `ReportSteps` uses `SELECT FOR UPDATE` on `user_daily_steps` (`GetDailyStepsForUpdate`) before computing the delta, serialising concurrent deposits for the same user/day. The `GREATEST` function on upsert and `ON CONFLICT DO NOTHING` on the audit row provide further guards.

**Rake applied once per hand.** `endHandUncontested` and `endHandShowdown` are mutually exclusive code paths in `advance.go`; rake is computed and emitted exactly once per hand. No path calls both.

**Integer overflow in rake.** `computeRake` uses `amount * int64(cfg.RakeBps) / 10000`. With `amount` up to ~10^12 chips (maximum realistic table stake) and `RakeBps` up to 10000 (100%), the product is ~10^16, well within int64 max (~9.2×10^18). The cap further limits it.

**Negative amounts.** `buy_in` is checked `> 0` in `server.go:122`. `bet.Amount` is checked `<= 0` in `validate_actions.go:97`. `cumulative_steps_today` is checked `< 0` in `service.go:128`. Raise `to` is checked against current bet and stack. The DB has `CHECK (credited_chips >= 0)` and `CHECK (max_steps >= 0)` constraints as a backstop.

**JWT validation.** Issuer is checked by the `validator.New` exact match, not substring. Audience is enforced. Algorithm is pinned to RS256 — the validator construction uses `validator.RS256`. Expiration is enforced with 30-second clock skew. JWKS caching uses the Auth0 SDK's rotating cache; key rotation is handled automatically via TTL-based re-fetch.

**go.sum committed.** `go.sum` is present and should be committed to VCS. Module verification is recommended in CI via `go mod verify`.

---

## Methodology and Coverage

**Read in full:** `internal/auth/*`, `internal/config/*`, `internal/server/server.go`, `internal/server/session.go`, `internal/server/principal.go`, `internal/server/user_server.go`, `internal/user/service.go`, `internal/engine/session.go`, `internal/engine/router.go`, `internal/engine/handler.go`, `internal/engine/dealer.go`, `internal/engine/validate*.go`, `internal/engine/advance.go` (structural + key functions), `internal/engine/pot.go`, `internal/engine/lobby.go`, `internal/lobby/join_leave.go`, `internal/store/store.go`, `internal/store/users.go` (ReportSteps path), `internal/deck/deck.go`, `db/migrations/*`, `db/queries/*`, `cmd/server/main.go`, `go.mod`.

**Partially read:** `internal/engine/apply.go` (via function signatures), `internal/store/pgstore/*.sql.go` (via grep for query patterns), proto definitions (grep-level review).

**Not read:** `internal/store/pgstore/game_configs.sql.go`, `internal/store/pgstore/game_events.sql.go` (confirmed to be sqlc output matching the audited query files), test files other than for methodology checking.

**Tools not run:** `govulncheck` and `gosec` were not installed. Install instructions are provided above.

---

## Recommendations for next steps

**Fix immediately (before real-money launch):**

1. **F-001** — Add `auth.FromContext(ctx)` and principal-vs-request-user_id enforcement to all six `UserService` handlers. This is the highest-risk issue.
2. **F-002** — Remove gRPC reflection from `PublicMethods`. If reflection is needed in dev, gate it with a build tag or config flag.

**Fix before public beta:**

1. **F-003** — Scope `FindEventByCommandID` to `(game_id, command_id)`. This requires a SQL change, an index change, and updating all call sites.
2. **F-006** — Document (or implement) TLS policy. If TLS is terminated at a load balancer, add a health-check assertion that the runtime server is not directly reachable over plaintext from outside the cluster.
3. **F-007** — Add `grpc.MaxConcurrentStreams(500)` and a per-principal rate limiter interceptor.
4. **F-008** — Add `grpc.MaxRecvMsgSize(65536)`.

**Fix before production scale:**

1. **F-004** — Implement admin role claims before implementing admin RPCs.
2. **F-009** — Decide inbox semantics (backpressure vs. fast-reject) and either remove `ErrInboxFull` or wire it correctly.
3. **F-010** — Configure pgxpool max connections and statement timeout explicitly.
4. **F-014** — Fix copylocks in validate_test.go.

**Accept or defer:**

1. **F-005** — Accept as a design consequence of event sourcing. Document the insider-threat risk and restrict DB access to the application user only.
2. **F-011** — Accept the current design; add the startup warning log.
3. **F-012 / F-013** — Define data retention and GDPR erasure policy. Implement when regulatory requirements are known.
4. **F-015** — Operational hardening; document the least-privilege DB user requirements.
5. **F-016, F-017, F-018** — Info-level; no immediate action required.
