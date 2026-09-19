# Architecture

This living document records implemented architecture and agreed design decisions. The [README](README.md) defines the challenge requirements; those requirements do not imply that a design has been selected or implemented. Update this document as decisions are made.

## Architecture direction

**Decided; partially implemented.** The Go application is composed with Uber Fx. The domain must remain independent of Fx, HTTP, SQS, and persistence libraries. HTTP handlers and the SQS consumer will eventually call the same application use cases. Infrastructure adapters will implement interfaces required by the application/domain layers.

Configuration, application composition, an HTTP server, the Money value object, the Wallet aggregate, and the WagerTransaction entity exist today. PostgreSQL repositories and transaction infrastructure are implemented. The financial application processor and append-only ledger implement BET, WIN, LOSS, REFUND and ROLLBACK, including explicit pending-reference resolution. Transactional outbox snapshots and a transport-independent delivery service are implemented in checkpoint 5; no messaging transport or production publisher worker is started.

## Application composition and lifecycle

**Implemented.** [`cmd/api/main.go`](cmd/api/main.go) is the composition root. Fx provides constructor dependency injection through `fx.Provide` and instantiates the HTTP server through `fx.Invoke`.

The HTTP server uses `net/http` and `fx.Lifecycle`: `OnStart` launches `ListenAndServe` in a goroutine, and `OnStop` calls `Shutdown` with the lifecycle context for graceful shutdown. Currently, serving errors are printed and are not propagated to Fx; successful startup does not confirm that the listener bound successfully. Worker lifecycle and resource shutdown ordering remain undecided.

## Configuration

**Implemented.** [`internal/config/config.go`](internal/config/config.go) reads environment variables.

| Variable | Default |
| --- | --- |
| `HTTP_HOST` | `0.0.0.0` |
| `HTTP_PORT` | `8080` |

Defaults apply when variables are absent. An explicitly empty `HTTP_PORT` is rejected. `DATABASE_URL` is required by the Go application and passed to pgx configuration; there is no application credential default. `.env.example` includes a local Compose connection string. The shell must export this variable when running the API; the Go configuration loader does not load `.env` files.

## Money

**Implemented.** `Money` is an immutable domain value object containing an `int64` amount in minor units and a currency, with private fields and value-returning operations. Its range at the fixed two-decimal scale is `-92233720368547758.08` through `92233720368547758.07`. Parsing, calculations, and amount serialization use no floating-point values.

External parsing accepts non-negative decimal strings with an integer part and an optional one- or two-digit fraction. Leading zeros are accepted; signs, whitespace, and trailing decimal points are rejected. `Amount()` serializes with exactly two decimal places, so `10`, `10.5`, and `0010.50` normalize to `10.00`, `10.50`, and `10.50`, respectively. Negative values are supported for internal calculations.

Currency codes are normalized to uppercase. Validation currently checks ISO 4217-style syntax only (three ASCII letters), not membership in the complete ISO currency registry. Arithmetic and comparison across different currencies return `ErrCurrencyMismatch`.

`Money{}` is intentionally invalid; use `Zero(currency)` for monetary zero. Public operations and accessors reject uninitialized values with `ErrInvalidMoney`. Parsing, addition, subtraction, and negation explicitly detect overflow and return `ErrOverflow` instead of wrapping; negating the minimum `int64` value is rejected.

PostgreSQL amounts use BIGINT minor units. `FromMinorUnits(int64, currency)` accepts the full signed range without conversion, validates/normalizes currency, and preserves internal negative values. `MinorUnits() (int64, error)` rejects uninitialized Money. JSON mapping remains **To be decided**.

## Wallet

**Implemented.** `internal/domain/wallet` contains an aggregate with private identity, player ID, currency, Money balance, signed `int64` version, and creation/update timestamps. `New` accepts explicit IDs, currency, initial balance, and time. IDs are opaque non-empty UTF-8 strings without whitespace or control characters; UUID syntax is not required. Currency validation and normalization use Money. The initial balance must be valid, non-negative, and match the wallet currency. Creation starts at version `1`.

`Wallet` is a mutable aggregate with private state; pointer-based `Credit` and `Debit` mutate it and return an error. Money remains immutable. All validations and calculations complete before any aggregate fields change. Getters return values, and no setters or mutable references are exposed. Uninitialized wallets reject financial operations. Read-only getters expose zero state on an uninitialized wallet, including an invalid Money balance.

Amounts must be valid, non-negative Money in the wallet currency. Insufficient funds return `ErrInsufficientBalance`; invalid initial balances and operation amounts have separate errors. Wrapped Money errors remain detectable through `errors.Is`, including currency mismatch and monetary overflow. Every failure leaves balance, version, and timestamps unchanged.

Valid zero credits/debits are no-ops: balance, version, and timestamps remain unchanged, consistent with the README's balance-change version rule. Positive changes increment the version exactly once; version overflow is rejected. The application layer supplies processing timestamps, stored in UTC. Creation and balance-changing operations require non-zero timestamps. Timestamp ordering is not a Wallet invariant; earlier timestamps are accepted. Valid zero operations require no timestamp, but still validate the Wallet, Money, and currency. `createdAt` never changes.

This checkpoint enforces only in-memory balance invariants and creates no ledger entries or events. Global uniqueness of `(playerId, currency)` is enforced by the initial schema. The financial processor now commits balance changes together with ledger entries and saved outcomes for BET/WIN; messaging and the remaining operations are deferred. Repository locking is described below.

## WagerTransaction

**Implemented.** `internal/domain/wagertransaction` models external BET, WIN, LOSS, REFUND, and ROLLBACK operations as a mutable entity with private state. It owns internal/provider/external identity, player/wallet/round/game context, kind, immutable Money, optional external reference, status, failure code, and creation/update timestamps. IDs follow Wallet's opaque non-empty UTF-8 policy without whitespace or control characters. `(providerId, externalTransactionId)` identifies the financial operation conceptually; uniqueness is not enforced by this in-memory entity.

`NewExternal` starts in PENDING. BET/WIN/REFUND/ROLLBACK require positive Money; LOSS requires zero. REFUND/ROLLBACK require an external reference, WIN permits one, and BET/LOSS reject references. Malformed references and self-reference by external ID are rejected. OPENING and unknown kinds are rejected.

Pointer methods `WaitForReference`, `MarkProcessed`, `Reject`, and `Fail` validate completely before changing status, failure code, or updatedAt. Failed attempts preserve the entire entity. PENDING can transition to PENDING_REFERENCE, PROCESSED, REJECTED, or FAILED. PENDING_REFERENCE can transition directly to PROCESSED, REJECTED, or FAILED. Waiting requires a declared reference. Terminal states and same-state transitions are rejected; there is no transition back to PENDING or public status setter. Nil/uninitialized entities reject transitions. Getters return values.

Application-supplied processing timestamps must be non-zero and are normalized to UTC. No timestamp ordering is imposed; createdAt never changes. `MarkProcessed` validates lifecycle only: it does not validate references, wallet effects, or financial atomicity and does not mutate Wallet.

FailureCode is distinct from Go validation errors. `Reject` accepts business codes `BET_INSUFFICIENT_FUNDS`, `REVERSAL_INSUFFICIENT_FUNDS`, `REFERENCE_NOT_FOUND`, `CURRENCY_MISMATCH`, and `MONETARY_OVERFLOW`, `REFERENCE_NOT_ALLOWED`, `REFERENCE_MISMATCH`, `REFERENCE_UNSUCCESSFUL`, and `ALREADY_REVERSED`; `Fail` accepts `PERMANENT_INFRASTRUCTURE_FAILURE`. This small catalog validates the business/infrastructure category, not code applicability to individual operation kinds. The application must select the appropriate outcome and classify infrastructure failures; temporary outages do not imply FAILED. Wrapped Money errors remain available through `errors.Is`.

**Deferred:** OPENING. Transactional outbox is implemented in checkpoint 5 below. Reference resolution, internal reference IDs, and cross-transaction validation are implemented in checkpoint 4 below. Application-owned persistence records now store fingerprints and replay balances, with keys in a separate binding table; these are not additional WagerTransaction domain fields. Initial persistence schema and explicit rehydration are implemented below. No financial movement or event generation occurs here. The application coordinates WagerTransaction, Wallet, Ledger, and Outbox atomically; event creation does not belong to the domain entity.

## Persistence checkpoint 1

**Implemented.** Wallet and WagerTransaction expose `Rehydrate(State)` constructors that validate local persisted invariants and initialize private fields directly. They invoke no financial operations or transitions. Versions, statuses, failure codes, and timestamps are preserved exactly, including timestamp locations and earlier updatedAt values. Wallet rehydration rejects noncanonical currency rather than silently normalizing stored state. Creation still normalizes currency and processing timestamps. WagerTransaction rehydration accepts all five valid states with compatible failure codes; reference resolution is not implied.

Versioned golang-migrate SQL files in `migrations/` create only `wallets` and `wager_transactions` (plus migration-tool metadata). IDs are TEXT; amounts and wallet versions are BIGINT; timestamps are TIMESTAMPTZ. Optional references and failure codes use NULL. Wallet constraints enforce non-empty identities, unique player/currency, non-negative balance, positive version, and uppercase ASCII currency. Transaction constraints enforce unique provider/external identity, wallet existence without cascading deletion, kind/status, amount policy, reference presence/self-reference, and failure-code/status compatibility. External references deliberately have no foreign key. An index supports wallet transaction lookup. These constraints validate stored rows, not historical transition ordering or ledger consistency.

`compose.yaml` pins PostgreSQL to `17.11-bookworm` with a named persistent volume, localhost port binding, and a health check. A separate tools-profile migration service pins golang-migrate to `v4.18.3`; applications do not run migrations at startup. Each up/down migration uses an explicit SQL transaction. Environment defaults are local examples in `.env.example`; credentials with URL-reserved characters require URL encoding in the migration connection URI.

Local commands (Docker Desktop/Engine with Compose required):

```sh
docker compose config --quiet
docker compose up -d --wait postgres
docker compose run --rm migrate up
docker compose run --rm migrate version
# Removes the most recently applied table; use only on disposable development data.
docker compose run --rm migrate down 1
docker compose run --rm migrate up
```

For an isolated up/down verification, use a separate Compose project and unused port consistently, for example `POSTGRES_PORT=55439 docker compose -p wager-persistence-check1 ...`. After applying both migrations, `run --rm migrate down 2` removes both application tables; apply `up` again to verify reversibility. `down` without `--volumes` stops that project's containers while retaining its database. Changing POSTGRES initialization settings does not rewrite an existing volume.

PostgreSQL timestamp storage has microsecond precision; rehydration itself never truncates or replaces timestamps. Database adapters must handle UTC display and persistence precision explicitly. Repositories and per-wallet locking are implemented in checkpoint 2 below. BET/WIN/LOSS financial processing is implemented in checkpoint 3 below.

## Persistence checkpoint 2

**Implemented.** `internal/infrastructure/postgres` uses pgx/v5 and pgxpool with explicit SQL. Its Fx module constructs the pool from `DATABASE_URL`, verifies connectivity in OnStart, and closes the pool in OnStop (also closing it if startup ping fails). The module is composed before HTTP, so HTTP stops before the pool. Connection attempts have a five-second timeout; operations also receive caller contexts.

`Runner.WithinTransaction(ctx, callback)` owns a single READ COMMITTED pgx transaction. The callback receives Wallet and WagerTransaction repositories bound to that exact transaction. Success commits; callback errors roll back; commit errors are returned without retries or assumptions about replay safety. Cleanup uses a bounded context independent of request cancellation, also on panic. Rollback errors preserve the original error. Callback repositories must not escape or be used concurrently. A database rollback does not undo changes to in-memory domain objects.

Wallet repository methods are Insert, GetByID, GetForUpdate, and UpdateBalance. GetForUpdate executes SELECT FOR UPDATE and refuses pool-only use. UpdateBalance changes only balance_minor, version, and updated_at, guarded by expectedVersion. WagerTransaction methods initially were Insert, GetByID, GetByFinancialIdentity, and UpdateOutcome. Checkpoint 3 requires a saved result for PROCESSED/REJECTED: these now use CompleteOutcome. Insert calculates the fingerprint and refuses direct PROCESSED/REJECTED insertion; UpdateOutcome is restricted to PENDING_REFERENCE/FAILED. Updates are guarded by expectedStatus. Zero affected rows produce the corresponding stale-write error (including a missing update target). Reads have a separate not-found error. No generic Save/Upsert/Delete operations are exposed.

Constraint-specific infrastructure errors map `wallets_player_currency_unique` to duplicate wallet identity and `wager_financial_identity_unique` to duplicate financial identity. Other database violations, including primary-key collisions, retain their diagnostic cause without being misclassified. These errors are not domain business rejections.

Money travels as int64/BIGINT through MinorUnits and FromMinorUnits. Loading uses Wallet.Rehydrate and WagerTransaction.Rehydrate. Invalid persisted state wraps its cause with ErrInvalidPersistedData. Stored currency must already be canonical: the adapter checks for normalization changes rather than silently repairing values. Optional SQL NULLs become absent domain fields; non-NULL empty optional fields are rejected. Timestamp instants load as UTC at PostgreSQL's microsecond precision. Domain logic remains independent of pgx.

Integration tests require real PostgreSQL and `TEST_DATABASE_URL`. Each test creates an isolated randomly named schema, applies the existing migration SQL, and drops its own schema on cleanup. The supplied test account must be able to create schemas. Tests cover repositories, constraints/error mapping, rollback and commit failure, cancellation, Fx pool lifecycle, corrupted state, and actual lock contention observed through pg_blocking_pids. These are repository tests, not proof of complete financial processing correctness.

```sh
POSTGRES_PORT=55439 docker compose -p wager-persistence-check1 up -d --wait postgres
export TEST_DATABASE_URL='postgres://wager:local_only@localhost:55439/wager?sslmode=disable'
go test -tags=integration -count=1 ./internal/infrastructure/postgres
go test -race -tags=integration -count=1 ./internal/infrastructure/postgres
# Go application configuration (use the actual local port):
export DATABASE_URL="$TEST_DATABASE_URL"
go run ./cmd/api
```

Checkpoint 3 implements ledger, financial orchestration, and idempotency below. Inbox and multi-process financial scenarios remain deferred; outbox is implemented in checkpoint 5 below. Reference processing is implemented in checkpoint 4 below. No transaction retry or worker machinery is introduced.

## Persistence/application checkpoint 3

**Implemented for BET, WIN without reference, and LOSS.** `internal/application/financial.Processor.Process(ctx, ProcessRequest)` is the shared transport-independent use case. Requests carry trusted provider identity, received idempotency key, external identity, player/wallet/round/game context, kind, immutable Money, and optional reference. Results contain transaction ID, status, optional failure code, optional saved balance, and replay indicator. The provider field is a trusted input contract, not implemented authentication. No HTTP/SQS adapter or new Fx wiring is added.

The application owns narrow transaction/repository interfaces. `Runner.WithinFinancialTransaction` adapts the existing READ COMMITTED runner, binding every repository to its same pgx transaction. Domain code contains no pgx/Fx dependencies. Internal IDs use 128 random bits encoded as hex. Processing timestamps are application-generated UTC at PostgreSQL microsecond precision; no timestamp ordering invariant is added.

### Identity, keys, and fingerprint

`(provider_id, external_transaction_id)` remains the unique financial identity. `financial_idempotency_keys` permanently binds each `(provider_id, idempotency_key)` to a provider-consistent transaction FK. All accepted keys live here; there is no original-key column on wager_transactions. Same key and business payload replay. Reusing a key for another identity/payload conflicts. Another key for the same identity and payload is persisted as an alias and replays. Another key with changed payload conflicts. Bindings have database protection against UPDATE, DELETE, and TRUNCATE.

The fingerprint is SHA-256 over canonical JSON produced by Go encoding/json from string-keyed maps: lexicographically ordered object keys, compact encoding, UTF-8, standard JSON and HTML escaping, and no newline. The exact bytes are covered by unit tests. Fields are externalTransactionId, gameId, kind, money (amount and currency), playerId, providerId, referenceExternalTransactionId, roundId, walletId. Absent reference is JSON null. Amount uses Money.Amount() with exactly two decimals; currency is uppercase. Validated identifiers are preserved without case conversion, trimming, or Unicode normalization. Keys, internal IDs, timestamps, and transport metadata are excluded. This canonicalization is a fixed persistence contract; changing it requires an explicit compatibility decision.

Idempotency keys are non-empty valid UTF-8 strings without whitespace or control characters. Fingerprints and saved results belong to the application persistence Record, not the domain entity. The database stores the fingerprint as a 32-byte BYTEA.

### Transaction and replay

For a new operation, validate local input and hash before beginning. Inside the transaction: look up key, then financial identity; replay/conflict when found; lock the wallet with SELECT FOR UPDATE; validate player ownership; establish financial identity with targeted ON CONFLICT DO NOTHING; bind the key; calculate the domain operation; update the wallet with expectedVersion only if changed; insert a ledger entry only for movement; complete status and result with expectedStatus; commit. The wallet is locked before inserting its referencing transaction row to avoid foreign-key lock upgrade contention. Losing TryInsertExternal performs a new SELECT statement under READ COMMITTED before fingerprint comparison. Expected duplicate contention never relies on continuing after a unique-violation error.

BET debits and WIN credits through Wallet. Successful movements increment version once and create exactly one DEBIT/CREDIT entry respectively. LOSS still locks and validates the wallet and saves its balance, but calls neither Debit nor Credit, performs no wallet UPDATE, preserves wallet timestamps/version, and creates no ledger. All successful operations become PROCESSED.

Insufficient BET funds, incompatible currency, and monetary overflow commit REJECTED with BET_INSUFFICIENT_FUNDS, CURRENCY_MISMATCH, or MONETARY_OVERFLOW respectively, and the unchanged wallet balance. The callback returns nil for these business outcomes. Missing wallet/wrong player returns invalid context without accepting an operation or exposing a balance. Malformed inputs and unsupported kinds/references fail before financial acceptance. Wallet version exhaustion and infrastructure/integrity errors roll back rather than becoming REJECTED or FAILED.

`result_balance_minor` and `result_currency` are saved on wager_transactions atomically with the terminal outcome. Currency is separate from requested currency because a currency-mismatch rejection returns the actual wallet balance. PROCESSED/REJECTED require a result; pending states forbid one. Replay reads only the persisted record, compares the fingerprint, binds a new alias when needed, and returns the saved outcome with IdempotentReplay=true. It does not reconstruct balance from Wallet or change terminal timestamps. Existing pending records are returned as pending, without processing them.

Callback results remain provisional until commit succeeds. Any runner/commit error returns an empty ProcessResult and the error, without an automatic retry. An ambiguous commit can be reconciled later through persistent financial identity; a client must not bypass identity arbitration and directly repeat a mutation. No recovery worker is implemented, and transient failures are never automatically recorded as FAILED.

### Ledger and database protection

`internal/domain/ledger.Entry` is immutable, with validated identifiers, DEBIT/CREDIT direction, positive Money, same-currency non-negative before/after balances, exact arithmetic, and UTC creation time. Its zero value is rejected by persistence. `LedgerRepository` exposes Insert only.

`wallet_ledger_entries` stores integer BIGINT amount/before/after values, explicit currency, and wallet/transaction references. Unique (wallet_id, transaction_id) prevents duplicate movement. A composite transaction/wallet FK enforces association, and a wallet/currency FK enforces currency. Arithmetic CHECKs subtract two non-negative balances, avoiding overflowing BIGINT addition. Database statement triggers reject UPDATE, DELETE, and TRUNCATE, including empty-table statements. Privileged schema owners can alter/disable protections; production runtime-role separation remains a deployment task. No double-entry accounting is implemented.

Balance/ledger/outcome consistency is enforced by the application transaction plus row locks and database uniqueness/checks. Direct privileged SQL is not a supported financial write path. Reversal corrections will require new entries rather than editing existing entries.

### Migration and verification scope

Migration 000003 adds fingerprint/results, supporting composite keys, key bindings, ledger, database protections, and the two new rejection codes. Existing migrations are unchanged. UP explicitly requires an empty wager_transactions dataset: original keys and historical result balances cannot be fabricated from current balances. Use disposable development data or a new Compose volume/database; no automatic data deletion occurs. DOWN removes the new structures and refuses incompatible new rejection codes, with all DDL inside its SQL transaction.

Integration fixtures apply all three migrations to isolated schemas. Tests cover financial outcomes and replay, key conflicts/aliases, schema constraints and append-only protection, real transaction rollback at multiple write stages, database-raised failure, independent-wallet progress, two concurrent BET 80.00 against 100.00, 50 distinct operations, 50 duplicate copies, and concurrent aliases. Synchronization uses channels and PostgreSQL lock observation rather than sleeps. UP/DOWN/UP and refusal of a populated legacy dataset are tested. Positive initial balances are fixtures; ledger assertions cover checkpoint deltas, not complete from-zero reconciliation without OPENING.

Run all integration tests with the TEST_DATABASE_URL setup above, both normally and with -race. Three independent process verification is required later; concurrent connections/goroutines are not claimed as its substitute. Checkpoint 4 extends this processor with reference operations below. OPENING, inbox/SQS, HTTP financial endpoints, OAuth/OIDC, and recovery workers remain deferred. Checkpoint 5 adds outbox persistence and explicit delivery invocations. This checkpoint does not claim full challenge compliance or event-delivery guarantees.

## Persistence/application checkpoint 4

**Implemented.** The existing processor accepts REFUND, ROLLBACK and reference-bearing WIN. `ResolvePendingReference(ctx, providerID, transactionID)` reuses its financial execution path and persisted original request. It does not accept replacement money, payload, reference, or key. A terminal operation returns its saved result; unexpected PENDING is an integrity error. Provider-scoped not-found behavior prevents cross-provider disclosure. There is no automatic progression until a future worker invokes this method.

### Reference rules

References are resolved only by (provider_id, reference_external_transaction_id), using the existing unique index. An ID present only under another provider is absent for this operation. Both transactions must agree in provider, player, wallet, currency, and round. gameId equality is deliberately not required. Wallet ownership/currency checks still apply independently of reference validation.

| Operation | Eligible PROCESSED reference | Amount | Movement |
| --- | --- | --- | --- |
| REFUND | BET | Exact full original amount | CREDIT |
| ROLLBACK | BET | Exact full original amount | CREDIT |
| ROLLBACK | WIN or REFUND | Exact full original amount | DEBIT |
| WIN with reference | BET | Independent positive amount | CREDIT |

LOSS and ROLLBACK cannot be rollback targets. Multiple distinct WINs may reference a BET, including one already reversed; WIN does not consume reversal exclusivity. A referenced operation with invalid kind/context is rejected immediately, even if nonterminal. A compatible PENDING/PENDING_REFERENCE target causes waiting; REJECTED/FAILED causes REFERENCE_UNSUCCESSFUL. Partial reversals are rejected with REFERENCE_MISMATCH. Other stable codes are REFERENCE_NOT_ALLOWED and ALREADY_REVERSED. Existing REVERSAL_INSUFFICIENT_FUNDS, MONETARY_OVERFLOW, CURRENCY_MISMATCH and REFERENCE_NOT_FOUND retain their meanings. Version exhaustion remains an error with rollback, not a business rejection.

### Pending lifecycle and time

The processor receives a positive TTL and an injected `func() time.Time` through its constructor. Config.Load parses REFERENCE_PENDING_TTL (default 24h) and rejects empty, malformed, zero, or negative durations. cmd/api registers a constructor supplying the configured TTL and time.Now. The application samples processing time through its injected clock, in UTC at microsecond precision; resolution samples after acquiring locks.

The first PENDING -> PENDING_REFERENCE transition atomically stores reference_deadline_at = processing time + TTL, the financial identity, and key bindings. It creates no wallet update, ledger, or saved balance. Replay/aliases do not resolve the operation or extend its deadline. Unsuccessful pre-deadline resolution leaves the pending row and deadline unchanged; WaitForReference is not called again. At or after the deadline, resolution commits REJECTED/REFERENCE_NOT_FOUND even if the reference is now available. The deadline is retained after completion. No retry counters, leases, scheduling columns, polling or worker exist.

### Locks, completion and exclusivity

New requests keep the checkpoint 3 order: key/identity reads, wallet FOR UPDATE, identity/binding establishment, reference evaluation, financial writes and outcome. Resolution reads the operation without a lock to discover the wallet, locks that wallet, then locks/re-reads the operation and checks its current status/deadline. References are read without FOR UPDATE; terminal records remain immutable and valid operations for that wallet serialize on its wallet lock. Invalid cross-wallet references are rejected without locking the other wallet.

A successfully validated PROCESSED reference's internal ID is persisted before financial completion, within the same transaction. The FK includes provider, external reference and internal ID, preventing inconsistent identity mappings. Reference metadata is application persistence state and is excluded from the original fingerprint. CompleteOutcome remains guarded and commits status, failure code and historical result together. MarkPendingReference is the narrow guarded operation for the initial pending transition with its deadline; the legacy UpdateOutcome guard is unchanged and cannot supply a missing deadline.

A partial unique index on (provider_id, reference_external_transaction_id), restricted to PROCESSED REFUND/ROLLBACK, permits at most one successful reversal across both types. Under the wallet lock, the application checks existing successful reversals before movement and commits ALREADY_REVERSED for the loser. An unexpected SQL constraint failure rolls back; processing never continues in an aborted transaction. Rejected reversals do not consume exclusivity.

ROLLBACK(REFUND) debits the refunded amount but never changes historical audit records or reopens reversal permission for the original BET. Every successful movement has one new ledger entry and one wallet version increment. Pending and rejected operations have no movement. Replay uses the historical terminal balance and never recalculates it from the wallet.

### Schema and verification

Migration 000004 adds reference_transaction_id, reference_deadline_at, provider/external/internal reference consistency, self-reference and terminal-reference constraints, the partial unique index, and the new failure codes. It neither deletes nor rewrites checkpoint 3 financial history. DOWN refuses existing reference metadata/new codes rather than silently discarding resolution history; UP/DOWN/UP is verified against preserved checkpoint 3 records. No earlier migration is rewritten.

Tests cover reference directions, contextual validation, provider isolation, pending/terminal references, aliases, strict expiration with an injected clock, historical replay, rollback of resolver failures, and the independent database uniqueness constraint. Channel barriers and observed PostgreSQL lock dependencies exercise competing reversals, two resolvers, replay versus resolver, and original versus reversal in both lock orders. Goroutines are cancelled and joined before cleanup; no sleeps establish correctness. Checkpoint 3 regression scenarios remain in the integration suite.

HTTP, SQS/inbox, OAuth/OIDC, Keycloak, OPENING, background resolution/backoff, production workers and three-process verification remain deferred. Outbox delivery is described in checkpoint 5 below. This is not complete challenge compliance.

## Persistence/application checkpoint 5 — Transactional outbox

**Implemented.** `internal/application/events` constructs immutable integration snapshots. The financial `Repositories.Outbox` writer uses the exact same pgx transaction as wallet, wager transaction, ledger and idempotency bindings. Events are inserted after the outcome is determined and before the callback returns. Any construction/insertion failure rolls back the entire operation. The financial processor never publishes. Existing wallet-first locks, fingerprint, historical replay and reference deadline semantics remain unchanged.

### Event contracts and emission

| Confirmed outcome | Events |
| --- | --- |
| BET/WIN/REFUND/ROLLBACK PROCESSED | WagerTransactionProcessed + WalletBalanceChanged |
| LOSS PROCESSED | WagerTransactionProcessed |
| Any business REJECTED, including expired references | WagerTransactionRejected |
| First transition to PENDING_REFERENCE | WagerTransactionPendingReference |
| Remains pending, replay or alias | None |
| FAILED | None |
| Rolled-back operation | No committed events |

Pending resolution adds the terminal event and a balance event only on actual movement. Existing pending events remain immutable. Each of the four concrete constructors fixes event type and schema version 1. The envelope contains eventId, eventType, aggregateId, correlationId, causationId, occurredAt, version and typed data. correlationId is the internal financial transaction ID; causationId is explicitly null. aggregateId is the wager transaction ID except for WalletBalanceChanged, where it is the wallet ID.

Transaction data contains providerId, externalTransactionId, transactionId, playerId, walletId, roundId, gameId, kind, status, money, referenceExternalTransactionId, failureCode and resultingBalance. Optionals are explicit nulls; pending has no balance. Rejected snapshots use the saved actual wallet balance, whose currency may differ from the requested Money. WalletBalanceChanged contains walletId, transactionId, direction, money, balanceBefore, balanceAfter and walletVersion from the same movement used for the ledger. No transport key, request hash, internal reference, deadline or delivery metadata is exposed.

Money serializes through Amount/Currency as fixed two-decimal strings and uppercase currency, without floats. Timestamps use UTC RFC3339 with microsecond precision. Go encoding/json serializes concrete payloads once. Event holds private metadata and a serialized string; JSON access returns a copy. JSONB may normalize formatting/key order, so byte-for-byte JSON formatting is not a contract. No event hashing is introduced; request fingerprint semantics are unchanged.

### Schema and event identity

Migration 000005 adds outbox_events with transaction FK (no cascade), aggregate/type/version, immutable JSONB envelope, occurrence timestamp and delivery metadata. Random 128-bit hexadecimal event IDs are generated once and persisted. UNIQUE(transaction_id,event_type) prevents duplicate logical events; a pending operation can legitimately have pending, processed and balance events with distinct IDs. Replays never construct or insert events.

CHECK constraints enforce known types, positive version, non-negative attempts, finite timestamps, paired lease fields, no lease on published events, and JSON object/envelope identity consistency. Missing JSON fields are rejected explicitly through IS TRUE. A trigger protects event identity, transaction, aggregate, type/version, payload and occurrence; DELETE/TRUNCATE are rejected. Publication timestamps, attempts, scheduling, leases and bounded diagnostics are the only mutable columns. No generic Save/update API is exposed. Privileged schema owners remain able to disable protections, as with ledger triggers.

The partial pending index is (next_attempt_at,id) WHERE published_at IS NULL. No historical events are backfilled: existing financial records are not re-emitted. A pre-existing pending operation emits its future terminal events when resolved, without inventing an earlier pending event. DOWN takes an exclusive table lock and refuses a non-empty outbox to prevent silent loss; prior migrations are unchanged.

### Delivery and recovery

`internal/application/outbox.Service.RunOnce` claims at most one event and publishes through EventPublisher, then marks success or reschedules failure. It is explicitly invoked and does not start a production background worker or fake transport. Options are constructor-injected, with DefaultConfig: publish timeout 10s, lease 30s, initial backoff 1s, maximum 5m. Invalid/non-positive values or a lease not longer than publish timeout are rejected. Clock is injected; no environment reads or financial time.Now calls are introduced.

PostgreSQL ClaimBatch uses a short READ COMMITTED transaction and SELECT FOR UPDATE SKIP LOCKED. Eligibility requires unpublished, due next_attempt_at, and an absent/expired lease. Claiming sets a fresh random token, lease deadline and increments attempt_count. The claim transaction commits before any network call. Records may proceed independently across instances. One event per service invocation avoids a locally queued batch consuming leases before publication.

MarkPublished and Reschedule are guarded by event ID, current lease token and unpublished state. A replaced token cannot update metadata. Reschedule clears the lease and sets the next attempt; only fixed safe diagnostic categories (publish_failed, publish_timeout, publish_cancelled) are stored, never raw transport errors. Retry delay doubles with a cap and never discards an event after an attempt limit. An unsuccessful metadata write or interrupted process leaves a lease that another invocation can reclaim after expiry. No lease-renewal loop or scheduler is needed for this bounded invocation.

Delivery is **at-least-once**, not exactly-once. Publication accepted followed by a crash/unknown acknowledgement before MarkPublished can cause republication with the same persisted eventId. This ID supports downstream durable deduplication. A stale publisher can still complete external I/O after lease expiry; fencing protects database updates, not the external broker, so duplicates remain part of the contract. Eventual delivery assumes future invocations and recovery of dependencies. Production workers and broker integration remain deferred.

No ordering is promised. PendingReference may arrive after a terminal event; consumers must not regress terminal status. WalletBalanceChanged carries walletVersion for ordering balance snapshots. Envelope version is a schema version, not a financial sequence. No global mutex or serialization is used.

### Verification

Unit tests cover concrete snapshots, null fields, exact Money formatting, invalid events, retry limits, configuration, clock and safe diagnostics. Real PostgreSQL tests cover the emission matrix, pending resolution/replay, atomic rollback before/during/after event insertion, uncommitted invisibility, logical uniqueness, immutable columns, DELETE/TRUNCATE rejection, claim concurrency, SKIP LOCKED progress, lease recovery/fencing, publish/retry/restart and duplicate delivery with stable IDs. Migration UP/DOWN/UP and populated-DOWN refusal use isolated schemas. Existing financial regression tests remain intact in meaning. Concurrency uses explicit synchronization and injected times, not arbitrary sleeps.

## Concurrency direction

**Implemented at repository level.** Wallet `GetForUpdate` uses PostgreSQL row locking within an existing READ COMMITTED transaction. No process-local or global locks provide correctness. Tests prove lock contention and financial correctness across concurrent independent database transactions. Verification with three independent OS processes remains deferred.

## Future decisions

The following mechanisms remain incomplete; implemented checkpoint decisions above take precedence.

### PostgreSQL transaction boundaries — To be decided

pgx, application-owned transaction callbacks, and READ COMMITTED are implemented. Wallet, transaction, ledger, key bindings, and saved results are now grouped atomically. Outbox now shares that transaction; inbox integration remains deferred.

### Wallet concurrency control — To be decided

Per-wallet SELECT FOR UPDATE and expected-version guards are implemented. Lock ordering across multiple entities and application conflict recovery remain deferred.

### Persistent idempotency — To be decided

Implemented for the shared financial processor above; transport integration and recovery policy remain deferred.

### Ledger persistence and immutability — To be decided

Schema, immutable entries, and database protections are implemented above. Opening entries and consistent reconciliation reads remain deferred.

### Inbox processing — To be decided

Message identity/hash storage, duplicate handling, transaction integration, and durable completion before SQS acknowledgment.

### Transactional outbox — Implemented core; transport deferred

Checkpoint 5 defines snapshots, concurrent claims, retries and recovery. Output destination, SQS adapter and production worker lifecycle remain undecided.

### Reference and reversal processing — To be decided

Reference resolution, strict expiration, and reversal exclusivity are implemented below. Automatic scheduling/backoff and the background resolver remain deferred.

### OAuth2/OIDC authorization — To be decided

External identity provider, credential validation, provider identity mapping, permissions for internal wallet operations, and broker access policies. Authentication is not implemented.

### SQS retry and DLQ policy — To be decided

FIFO grouping/deduplication identifiers, visibility timeout, retry/backoff limits, redrive configuration, invalid-message handling, and shutdown behavior.

### Observability — To be decided

Structured logging, correlation propagation, metrics, and dependency readiness checks. Currently, only `GET /health/live` is implemented; HTTP serving errors use plain text output.

### Failure recovery — To be decided

Transient/permanent failure classification, durable resumption of accepted work, recovery of abandoned worker jobs, and validation through multi-instance crash/restart scenarios.
