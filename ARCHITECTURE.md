# Architecture

This living document records implemented architecture and agreed design decisions. The [README](README.md) defines the challenge requirements; those requirements do not imply that a design has been selected or implemented. Update this document as decisions are made.

## Architecture direction

**Decided; partially implemented.** The Go application is composed with Uber Fx. The domain must remain independent of Fx, HTTP, SQS, and persistence libraries. HTTP handlers and the SQS consumer will eventually call the same application use cases. Infrastructure adapters will implement interfaces required by the application/domain layers.

Configuration, application composition, an HTTP server, the Money value object, the Wallet aggregate, and the WagerTransaction entity exist today. PostgreSQL repositories and transaction infrastructure are implemented. The financial application processor and append-only ledger are implemented for BET, WIN without references, and LOSS. Messaging components are not implemented yet.

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

FailureCode is distinct from Go validation errors. `Reject` accepts business codes `BET_INSUFFICIENT_FUNDS`, `REVERSAL_INSUFFICIENT_FUNDS`, `REFERENCE_NOT_FOUND`, `CURRENCY_MISMATCH`, and `MONETARY_OVERFLOW`; `Fail` accepts `PERMANENT_INFRASTRUCTURE_FAILURE`. This small catalog validates the business/infrastructure category, not code applicability to individual operation kinds. The application must select the appropriate outcome and classify infrastructure failures; temporary outages do not imply FAILED. Wrapped Money errors remain available through `errors.Is`.

**Deferred:** OPENING, reference resolution and resolved internal reference ID, cross-transaction validation, and outbox. Application-owned persistence records now store fingerprints and replay balances, with keys in a separate binding table; these are not additional WagerTransaction domain fields. Initial persistence schema and explicit rehydration are implemented below. No financial movement or event generation occurs here. Future application code must coordinate WagerTransaction, Wallet, Ledger, and Outbox atomically.

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

Checkpoint 3 implements ledger, financial orchestration, and idempotency below. Outbox/inbox, reference processing, and multi-process financial scenarios remain deferred. No transaction retry or worker machinery is introduced.

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

Run all integration tests with the TEST_DATABASE_URL setup above, both normally and with -race. Three independent process verification is required later; concurrent connections/goroutines are not claimed as its substitute. REFUND/ROLLBACK/PENDING_REFERENCE processing, OPENING, outbox, inbox/SQS, HTTP financial endpoints, OAuth/OIDC, and recovery workers remain deferred. WIN references are explicitly unsupported by this processor. This checkpoint does not claim full challenge compliance or event-delivery guarantees.

## Concurrency direction

**Implemented at repository level.** Wallet `GetForUpdate` uses PostgreSQL row locking within an existing READ COMMITTED transaction. No process-local or global locks provide correctness. Tests prove lock contention and financial correctness across concurrent independent database transactions. Verification with three independent OS processes remains deferred.

## Future decisions

The following mechanisms remain incomplete; implemented checkpoint decisions above take precedence.

### PostgreSQL transaction boundaries — To be decided

pgx, application-owned transaction callbacks, and READ COMMITTED are implemented. Wallet, transaction, ledger, key bindings, and saved results are now grouped atomically. Inbox/outbox integration remains deferred.

### Wallet concurrency control — To be decided

Per-wallet SELECT FOR UPDATE and expected-version guards are implemented. Lock ordering across multiple entities and application conflict recovery remain deferred.

### Persistent idempotency — To be decided

Implemented for the shared financial processor above; transport integration and recovery policy remain deferred.

### Ledger persistence and immutability — To be decided

Schema, immutable entries, and database protections are implemented above. Opening entries and consistent reconciliation reads remain deferred.

### Inbox processing — To be decided

Message identity/hash storage, duplicate handling, transaction integration, and durable completion before SQS acknowledgment.

### Transactional outbox — To be decided

Event contracts and snapshots, output destination, concurrent publisher coordination, retries, and recovery with stable event identities.

### Reference and reversal processing — To be decided

Durable pending-reference scheduling and expiration, reference resolution and validation, and policies preventing duplicate financial reversals. The local transaction state machine is implemented below.

### OAuth2/OIDC authorization — To be decided

External identity provider, credential validation, provider identity mapping, permissions for internal wallet operations, and broker access policies. Authentication is not implemented.

### SQS retry and DLQ policy — To be decided

FIFO grouping/deduplication identifiers, visibility timeout, retry/backoff limits, redrive configuration, invalid-message handling, and shutdown behavior.

### Observability — To be decided

Structured logging, correlation propagation, metrics, and dependency readiness checks. Currently, only `GET /health/live` is implemented; HTTP serving errors use plain text output.

### Failure recovery — To be decided

Transient/permanent failure classification, durable resumption of accepted work, recovery of abandoned worker jobs, and validation through multi-instance crash/restart scenarios.
