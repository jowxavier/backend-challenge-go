# Architecture

This living document records implemented architecture and agreed design decisions. The [README](README.md) defines the challenge requirements; those requirements do not imply that a design has been selected or implemented. Update this document as decisions are made.

## Architecture direction

**Decided; partially implemented.** The Go application is composed with Uber Fx. The domain must remain independent of Fx, HTTP, SQS, and persistence libraries. HTTP handlers and the SQS consumer will eventually call the same application use cases. Infrastructure adapters will implement interfaces required by the application/domain layers.

Configuration, application composition, an HTTP server, the Money value object, the Wallet aggregate, and the WagerTransaction entity exist today. PostgreSQL repositories and transaction infrastructure are implemented. Other domain models, financial application use cases, and messaging components are not implemented yet.

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

This checkpoint enforces only in-memory balance invariants and creates no ledger entries or events. Global uniqueness of `(playerId, currency)` is enforced by the initial schema. Durable ledger consistency and complete financial transaction orchestration remain **To be decided**. Repository locking is described below.

## WagerTransaction

**Implemented.** `internal/domain/wagertransaction` models external BET, WIN, LOSS, REFUND, and ROLLBACK operations as a mutable entity with private state. It owns internal/provider/external identity, player/wallet/round/game context, kind, immutable Money, optional external reference, status, failure code, and creation/update timestamps. IDs follow Wallet's opaque non-empty UTF-8 policy without whitespace or control characters. `(providerId, externalTransactionId)` identifies the financial operation conceptually; uniqueness is not enforced by this in-memory entity.

`NewExternal` starts in PENDING. BET/WIN/REFUND/ROLLBACK require positive Money; LOSS requires zero. REFUND/ROLLBACK require an external reference, WIN permits one, and BET/LOSS reject references. Malformed references and self-reference by external ID are rejected. OPENING and unknown kinds are rejected.

Pointer methods `WaitForReference`, `MarkProcessed`, `Reject`, and `Fail` validate completely before changing status, failure code, or updatedAt. Failed attempts preserve the entire entity. PENDING can transition to PENDING_REFERENCE, PROCESSED, REJECTED, or FAILED. PENDING_REFERENCE can transition directly to PROCESSED, REJECTED, or FAILED. Waiting requires a declared reference. Terminal states and same-state transitions are rejected; there is no transition back to PENDING or public status setter. Nil/uninitialized entities reject transitions. Getters return values.

Application-supplied processing timestamps must be non-zero and are normalized to UTC. No timestamp ordering is imposed; createdAt never changes. `MarkProcessed` validates lifecycle only: it does not validate references, wallet effects, or financial atomicity and does not mutate Wallet.

FailureCode is distinct from Go validation errors. `Reject` accepts business codes `BET_INSUFFICIENT_FUNDS`, `REVERSAL_INSUFFICIENT_FUNDS`, and `REFERENCE_NOT_FOUND`; `Fail` accepts `PERMANENT_INFRASTRUCTURE_FAILURE`. This small catalog validates the business/infrastructure category, not code applicability to individual operation kinds. The application must select the appropriate outcome and classify infrastructure failures; temporary outages do not imply FAILED. Wrapped Money errors remain available through `errors.Is`.

**Deferred:** OPENING, reference resolution and resolved internal reference ID, cross-transaction validation, idempotency/key storage, payload hashing, result balance/replay, ledger, and outbox. Initial persistence schema and explicit rehydration are implemented below. No financial movement or event generation occurs here. Future application code must coordinate WagerTransaction, Wallet, Ledger, and Outbox atomically.

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

PostgreSQL timestamp storage has microsecond precision; rehydration itself never truncates or replaces timestamps. Database adapters must handle UTC display and persistence precision explicitly. Repositories and per-wallet locking are implemented in checkpoint 2 below. Financial processing remains deferred.

## Persistence checkpoint 2

**Implemented.** `internal/infrastructure/postgres` uses pgx/v5 and pgxpool with explicit SQL. Its Fx module constructs the pool from `DATABASE_URL`, verifies connectivity in OnStart, and closes the pool in OnStop (also closing it if startup ping fails). The module is composed before HTTP, so HTTP stops before the pool. Connection attempts have a five-second timeout; operations also receive caller contexts.

`Runner.WithinTransaction(ctx, callback)` owns a single READ COMMITTED pgx transaction. The callback receives Wallet and WagerTransaction repositories bound to that exact transaction. Success commits; callback errors roll back; commit errors are returned without retries or assumptions about replay safety. Cleanup uses a bounded context independent of request cancellation, also on panic. Rollback errors preserve the original error. Callback repositories must not escape or be used concurrently. A database rollback does not undo changes to in-memory domain objects.

Wallet repository methods are Insert, GetByID, GetForUpdate, and UpdateBalance. GetForUpdate executes SELECT FOR UPDATE and refuses pool-only use. UpdateBalance changes only balance_minor, version, and updated_at, guarded by expectedVersion. WagerTransaction methods are Insert, GetByID, GetByFinancialIdentity, and UpdateOutcome; the latter changes only status, failure_code, and updated_at, guarded by expectedStatus. Zero affected rows produce the corresponding stale-write error (including a missing update target). Reads have a separate not-found error. No generic Save/Upsert/Delete operations are exposed.

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

Ledger/outbox/inbox, financial orchestration, idempotency, reference processing, and multi-process financial scenarios remain deferred. No transaction retry or worker machinery is introduced.

## Concurrency direction

**Implemented at repository level.** Wallet `GetForUpdate` uses PostgreSQL row locking within an existing READ COMMITTED transaction. No process-local or global locks provide correctness. Tests prove lock contention across independent connections; complete multi-instance financial correctness remains unimplemented.

## Future decisions

The following mechanisms are required by the challenge, but their designs have not been selected.

### PostgreSQL transaction boundaries — To be decided

pgx, application-owned transaction callbacks, and READ COMMITTED are implemented. Atomic grouping of wallet state, transactions, ledger, inbox, and outbox in financial application processing remains deferred.

### Wallet concurrency control — To be decided

Per-wallet SELECT FOR UPDATE and expected-version guards are implemented. Lock ordering across multiple entities and application conflict recovery remain deferred.

### Persistent idempotency — To be decided

Schema and uniqueness rules, deterministic payload hashing and normalization shared by HTTP/SQS, conflict handling, and storage of original results for replay.

### Ledger persistence and immutability — To be decided

Schema, database protections against edits/deletions, financial constraints, opening entries, and consistent reconciliation reads.

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
