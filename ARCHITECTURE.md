# Architecture

This document describes the final implemented solution. [README.md](README.md) contains the local runbook, API examples, test commands and a separate summary of the challenge requirements. Historical checkpoint limitations are not descriptions of the current system.

## Composition and boundaries

| Layer | Responsibility |
| --- | --- |
| `cmd/api` | Composition root using Uber Fx |
| `internal/domain` | Money, Wallet, WagerTransaction and immutable ledger-entry invariants |
| `internal/application/financial` | Shared financial execution, reference resolution, Inbox coordination and wallet operations |
| `internal/application/events`, `outbox`, `consumer` | Typed event snapshots, delivery policy and command handling |
| `internal/infrastructure/postgres` | pgx pools, SQL transactions, repositories, scheduling and database coordination |
| `internal/infrastructure/oidcauth`, `sqs` | OIDC validation and AWS SDK transport adapters |
| `internal/interfaces/http`, `messaging` | HTTP contracts, worker composition and lifecycle |
| `internal/observability` | JSON operational logger and process-local diagnostic counters/gauges |

Domain packages do not depend on Fx, HTTP, SQS or persistence libraries. Infrastructure implements application-required interfaces. HTTP `Process` and SQS `ProcessMessage` share the same private financial execution; no transport-specific financial implementation exists. WagerTransaction does not mutate Wallet or create ledger entries/events: application code coordinates those changes.

Fx injects configuration, connections, repositories, use cases, handlers and workers. Environment variables are loaded by configuration constructors; the Go process does not source `.env`. The README documents exporting the same trusted local configuration used by Compose. HTTP defaults to `0.0.0.0:8080`; `DATABASE_URL` is required.

## Lifecycle and resource ownership

PostgreSQL validates connectivity in OnStart. Messaging resolves queue URLs and checks inbound FIFO/redrive and Standard output configuration. OIDC discovery must succeed before HTTP starts. The HTTP listener binds synchronously in OnStart, so a bind error fails startup; serving then runs in a goroutine.

HTTP shutdown stops new requests and uses the lifecycle deadline. Messaging stops new receives/claims, lets active consumer/publication work finish within the shutdown context, then cancels outstanding work. A bounded additional cleanup window permits rollback and visibility cleanup before dependencies close. Reference recovery and metric sampling are cancelled and joined as part of the messaging lifecycle. PostgreSQL closes after its dependent components.

The consumer uses a processing timeout shorter than visibility. On cancellation or an unconfirmed outcome it does not acknowledge successful processing; work remains eligible for redelivery. WaitGroups and channels manage lifecycle/tests only, never financial correctness.

## Domain model and Money

Money has private `int64` minor units and currency and uses immutable value semantics. At scale two, its range is `-92233720368547758.08` through `92233720368547758.07`. External parsing accepts an integer part with zero, one or two fractional digits. Signs, whitespace, scientific notation, empty values, NaN/Infinity and excess scale are rejected. Leading zeros are accepted. Output always has two decimals; there is no silent rounding or floating-point financial path.

Currency is normalized to uppercase and Money checks three ASCII letters, not complete ISO registry membership. The HTTP financial/wallet entry points and SQS decoder restrict external input to BRL. Generic internal Money still supports other syntactically valid currencies and rejects incompatible operations.

`Money{}` is invalid; monetary zero requires `Zero(currency)`. Public accessors/operations reject uninitialized Money. Add, Subtract, parsing and Negate detect overflow, including negating MinInt64. `FromMinorUnits` and `MinorUnits` map the full signed range directly to PostgreSQL BIGINT without decimal-string conversions. Negative values are permitted internally, never as wallet balances or external financial input.

Wallet is a mutable aggregate with private ID, player, currency, balance, signed `int64` version and timestamps. IDs are opaque non-empty UTF-8 strings without whitespace/control characters, not necessarily UUIDs. Creation starts at version 1 with a non-negative compatible balance. Credit/Debit validate and calculate completely before changing fields. Errors preserve balance/version/time. Positive balance changes increment version exactly once; version overflow fails. Valid zero amounts are no-ops and need no operation timestamp.

WagerTransaction is a mutable entity with private financial/context identity, kind, Money, optional external reference, status, failure code and timestamps. External BET/WIN/REFUND/ROLLBACK require positive Money; LOSS requires zero. BET/LOSS forbid references; REFUND/ROLLBACK require them; WIN permits one. Self-references and invalid context are rejected. `NewOpening` constructs a processed internal operation with no external provider/key/round/game metadata; external constructors reject OPENING.

Ledger entries are immutable. Construction validates direction, positive amount, compatible currencies, non-negative balances and `after = before ± amount` with exact arithmetic. No setter or financial correction through entry mutation exists.

### State machine, timestamps and rehydration

| From | Allowed target |
| --- | --- |
| PENDING | PENDING_REFERENCE, PROCESSED, REJECTED, FAILED |
| PENDING_REFERENCE | PROCESSED, REJECTED, FAILED |
| PROCESSED / REJECTED / FAILED | None |

Waiting requires a declared reference. Same-state transitions, terminal transitions and a return to PENDING are forbidden. Transition validation completes before mutation. `MarkProcessed` validates lifecycle; it does not independently prove wallet or reference effects.

Application-supplied processing timestamps are non-zero, UTC and truncated to PostgreSQL microsecond precision. Timestamp ordering is not a domain invariant. Rehydration restores validated persisted state without calling business operations, incrementing versions, changing instants or emitting events. Wallet rehydration rejects noncanonical persisted currency rather than repairing it. Repository loading rejects invalid persisted data and preserves detectable underlying errors.

Ordinary operations complete inside one SQL transaction without a committed intermediate PENDING acceptance. The durable deferred state is PENDING_REFERENCE, which has automatic recovery.

## PostgreSQL persistence and migrations

The project uses pgx/v5 and explicit SQL. There are **eight migration versions** with transactional golang-migrate up/down files:

| Version | Change |
| --- | --- |
| 000001 | Wallets |
| 000002 | Wager transactions |
| 000003 | Financial fingerprints/results, idempotency bindings, ledger and protections |
| 000004 | Resolved references, deadlines and successful-reversal uniqueness |
| 000005 | Transactional Outbox and immutable snapshots |
| 000006 | Persistent Inbox and completion protection |
| 000007 | Internal OPENING shape and uniqueness |
| 000008 | Persistent reference retry scheduling |

The application tables are `wallets`, `wager_transactions`, `financial_idempotency_keys`, `wallet_ledger_entries`, `outbox_events` and `inbox_messages`; the migration tool also maintains version metadata. IDs use TEXT, monetary amounts and wallet versions use BIGINT, and timestamps use TIMESTAMPTZ. Optional fields use SQL NULL.

Important database protections include:

- Unique `(player_id,currency)` wallets, non-negative balances and positive wallet versions.
- Uppercase currency syntax, valid transaction kinds/statuses, amount/reference rules and failure-code/status compatibility.
- Unique `(provider_id,external_transaction_id)` financial identity and provider-consistent key bindings.
- Composite resolved-reference FK preserving provider/external/internal identity. An unresolved external reference has no FK requiring its target to exist yet.
- A single processed REFUND or ROLLBACK per provider/reference through a partial unique index. Rejected operations do not consume it.
- Unique `(wallet_id,transaction_id)` ledger entry, transaction/wallet and wallet/currency FKs, and exact non-negative before/after arithmetic checks.
- Ledger and idempotency-binding UPDATE/DELETE/TRUNCATE protection; Outbox snapshot and completed Inbox protection.
- Internal/external transaction shape checks and one OPENING per wallet.

Schema checks protect persisted invariants; the application transaction coordinates balance, ledger and outcome consistency. Direct privileged SQL is not a supported financial write path. A schema owner can alter protections, so deployment runtime-role separation remains an operational responsibility.

Migrations run through the separate Compose migration service, never independently at application startup. `migrate down` changes schema versions; `docker compose down` stops/removes Compose resources and normally retains the PostgreSQL named volume. They are different operations.

At version 8, one migration DOWN removes retry scheduling columns/index and loses that scheduling history. Earlier downgrades can remove structures or refuse incompatible history: reference metadata, non-empty Inbox/Outbox and OPENING history have relevant downgrade guards. Migration 000003 requires an empty legacy wager dataset because original keys/results cannot be inferred. Do not treat all downgrades as lossless production rollback. Use isolated/disposable databases for round-trip verification; the README contains current commands.

## Financial transaction and locking

`Runner.WithinTransaction` owns one READ COMMITTED pgx transaction. Its financial adapter supplies Wallet, WagerTransaction, key, ledger, Inbox and Outbox repositories bound to that exact transaction. No nested financial transaction or generic UnitOfWork abstraction is introduced. Repository callbacks must not escape or run concurrently.

For a new external operation:

1. Validate local input and compute its canonical fingerprint.
2. Check the persistent key and financial identity; replay or conflict if already known.
3. Lock the wallet with `SELECT ... FOR UPDATE`, then validate player/currency and establish the financial identity using targeted `ON CONFLICT DO NOTHING`.
4. Bind the received key, validate references/business rules and perform the domain calculation.
5. Update balance with an expected-version guard only when it changes; insert the movement ledger entry.
6. Save the guarded transaction outcome and historical balance; insert applicable event snapshots.
7. Commit before returning success or publishing anything.

The wallet lock precedes transaction insertion/resolution locks. A losing identity insert performs a fresh READ COMMITTED SELECT before comparing fingerprints. Expected duplicates do not rely on issuing SQL after a unique violation has aborted the transaction. Terminal outcome updates are guarded; no generic Save/Upsert/status setter is exposed.

Callback errors roll back. Panic also triggers bounded cleanup. Rollback uses a context independent of caller cancellation and retains the original error alongside rollback diagnostics. Commit errors return no provisional success and may represent an unknown commit outcome; the runner never automatically retries a mutation. Persistent identity allows subsequent replay to determine the committed result. In-memory domain mutations are not undone by SQL rollback, so failed transaction objects are not reused as confirmed state.

Per-wallet row locks work across processes, prevent lost updates and permit unrelated wallets to progress. Expected-version/status updates provide additional stale-write guards. There is no global application mutex or FIFO ordering requirement for these guarantees.

## Persistent financial idempotency

`(providerId,externalTransactionId)` identifies one financial operation. `financial_idempotency_keys` binds `(providerId,idempotencyKey)` permanently to a transaction. The received key is mandatory and is not silently replaced with a calculated key. An equivalent new key may become an alias but cannot reapply the operation.

The fingerprint is SHA-256 over compact Go `encoding/json` output from string-keyed maps, whose keys are sorted. It includes provider/external identity, player, wallet, round, game, kind, Money and optional external reference. Money has fixed two-decimal amount and uppercase currency; an absent reference is null. Identifiers are not trimmed, case-folded or Unicode-normalized. JSON/HTML escaping and no trailing newline are part of the contract. Keys, internal IDs, timestamps and transport metadata are excluded.

Same identity/key plus equivalent content returns the stored outcome; conflicting content returns a conflict. PROCESSED/REJECTED results persist their original balance/currency, so later wallet changes cannot alter replay. Pending replay does not resolve a reference or extend its deadline. These persistence fields belong to application records, not additional mutable domain-entity fields.

## References and failure semantics

References resolve by `(providerId,referenceExternalTransactionId)`. Provider, player, wallet, currency and round must match; game ID intentionally does not. REFUND targets BET; ROLLBACK targets BET, WIN or REFUND; reference-bearing WIN targets BET. Reversals must equal the full reference amount; WIN amount is independent.

Only processed references produce movement. An invalid kind/context rejects immediately; a compatible pending target waits; a REJECTED/FAILED target causes `REFERENCE_UNSUCCESSFUL`. Multiple WINs may reference a BET, including a reversed one. The combined successful-reversal index prevents duplicate repayment of a debit. Rolling back a REFUND reverses that credit but does not remove the original BET's consumed reversal slot.

| Failure code | Business meaning |
| --- | --- |
| BET_INSUFFICIENT_FUNDS | BET cannot debit the wallet |
| REVERSAL_INSUFFICIENT_FUNDS | Reversing a credit would overdraw the wallet |
| REFERENCE_NOT_FOUND | Original reference deadline has expired |
| REFERENCE_NOT_ALLOWED | Reference kind is not eligible |
| REFERENCE_MISMATCH | Required context or reversal amount differs |
| REFERENCE_UNSUCCESSFUL | Reference terminated without success |
| ALREADY_REVERSED | Another successful reversal consumed exclusivity |
| CURRENCY_MISMATCH | Operation and wallet currency differ |
| MONETARY_OVERFLOW | Monetary calculation exceeds representable capacity |

Business rejection is durable REJECTED with an unchanged wallet balance and no ledger movement. Go input/transport errors are separate from persisted failure codes. FAILED permits `PERMANENT_INFRASTRUCTURE_FAILURE` for permanent infrastructure classification; temporary database/SQS failures do not become fabricated business rejections or permanent failures. Version exhaustion/integrity errors roll back. The current financial processing paths return infrastructure errors rather than automatically persisting FAILED during an outage.

### Automatic pending-reference recovery

The first wait persists PENDING_REFERENCE, identity/bindings, `reference_deadline_at = processing time + TTL` and a pending event atomically. It creates no balance update or ledger entry. `REFERENCE_PENDING_TTL` defaults to 24 hours. At/after the original deadline, resolution rejects even if a reference has just appeared.

The PostgreSQL worker discovers pending rows and atomically advances `reference_attempts`/`reference_next_attempt_at` with a short `FOR UPDATE SKIP LOCKED` claim. It releases that claim before calling the existing `ResolvePendingReference`; that method locks the wallet before locking/re-reading the transaction and invokes the shared execution rules. Terminal replay has no financial effects.

Persisted delays are 1, 2, 4, 8, 16, 32, then 60 seconds, capped at the original deadline. The attempt counter saturates at 30 to bound scheduling arithmetic; it is not a maximum-attempt rejection policy. TTL alone determines business exhaustion. After expiration, infrastructure failures can retry every 60 seconds until the durable rejection can commit.

A crash after claiming leaves a future due time, so another instance resumes without losing work or resetting the schedule. The schedule is not an exclusive financial lease: an overlapping slow attempt is safe because the resolver uses wallet locks and terminal guards. Each attempt is bounded to 20 seconds; empty/error polling waits one second. Cancellation joins the worker through Fx lifecycle management.

## Persistent Inbox and SQS consumer

The required envelope is `messageId`, `type=WagerTransactionRequested`, `occurredAt` and typed financial `data`, including `idempotencyKey`. HTTP and SQS both parse external Money as strings with BRL-only input. Transport occurrence time is metadata, not the financial processing clock.

Inbox identity is `(consumer_name,envelope.messageId)`, not SQS transport MessageId or receipt handle. SHA-256 covers the original received body bytes, separate from the canonical financial fingerprint. Same identity/body after completion is a durable duplicate; different bytes conflict. Consumer name must remain stable across replicas/restarts.

`ProcessMessage` inserts Inbox with targeted conflict handling, invokes shared financial execution and completes Inbox in one SQL transaction. Wallet, transaction, key bindings, ledger and Outbox share that transaction. Confirmed incomplete Inbox records are integrity errors, not successful acknowledgments. Rollback leaves no accepted partial Inbox work. A persisted pending reference permits Inbox completion because the automatic resolver owns continuation.

The consumer deletes only after confirmed durable processing. Malformed commands, hash conflicts and infrastructure/ambiguous-commit failures are retained. Visibility backoff doubles from 1 second to 60 seconds; default visibility is 60 seconds, processing timeout 20 seconds, long poll 20 seconds, consumer concurrency 4 and native redrive max receive count 5. The application does not manually send to DLQ and then delete.

Producer guidance uses SHA-256(walletId) for MessageGroupId and SHA-256(envelope messageId) for MessageDeduplicationId. FIFO deduplication is not durable financial idempotency. Tests use distinct transport dedup IDs when proving repeated envelope receipt.

## Transactional Outbox and events

Typed constructors create immutable v1 snapshots for:

| Event | Trigger |
| --- | --- |
| WagerTransactionProcessed | Successful operation, including LOSS and internal OPENING |
| WagerTransactionRejected | Durable business rejection |
| WalletBalanceChanged | Actual financial balance movement |
| WagerTransactionPendingReference | First persisted reference wait |

The envelope carries `eventId`, `eventType`, `aggregateId`, `correlationId`, nullable `causationId`, `occurredAt`, `version` and typed `data`. Correlation uses transaction identity; causation is currently null. Transaction events carry external context when applicable, kind, Money, reference, status/failure and resulting balance. OPENING omits inapplicable external metadata. Balance events carry wallet/transaction IDs, direction, amount, before/after balances and wallet version. Values use decimal strings and UTC RFC3339-compatible timestamps.

Snapshots are inserted with financial state before commit, never sent to SQS inside that transaction. Logical event uniqueness and database snapshot protection prevent replacement/deletion of audit payloads. Payload access returns copies.

The delivery service claims one due event in a short READ COMMITTED transaction using SKIP LOCKED, then publishes outside SQL. Random lease tokens fence completion/rescheduling; expired leases permit abandoned work to be reclaimed. Defaults are a 10-second publish timeout, 30-second lease and exponential retry from 1 second to 5 minutes. Failure diagnostics persisted in Outbox are fixed safe categories, not raw transport errors.

Successful send precedes `MarkPublished`. A send may succeed before an error/crash prevents publication marking; a later attempt republishes the same immutable payload and event ID. This is at-least-once delivery, not exactly-once delivery. Downstream consumers must deduplicate event IDs. The Standard output queue is `wager-events`, separate from commands. Ordering is not guaranteed: consumers must not regress terminal states and should use walletVersion when applying balance snapshots.

## HTTP, authentication and authorization

HTTP implements wallet creation/read/ledger/reconciliation, financial submission and provider-scoped transaction lookups. Input is limited to 64 KiB with unknown/trailing JSON rejection. Authenticated handling has a 20-second deadline. HTTP success, pending, rejection, conflict, authentication and infrastructure outcomes have distinct statuses documented in the README.

Keycloak realm import provisions confidential client-credentials clients `provider-a`, `provider-b` and `wallet-internal`, with `wager-api` audience. Interactive/direct-password flows are disabled for these clients. The coreos OIDC verifier uses discovery/JWKS and verifies RS256 signature, issuer, audience and expiration; the adapter additionally checks `nbf`.

Verified `azp` maps to provider through `OIDC_PROVIDER_CLIENTS`. Only `OIDC_INTERNAL_CLIENT` receives internal permissions. Request providerId cannot select another financial namespace. SQL reads include authenticated provider identity; mismatched providers and hidden transactions return 404. Internal credentials cannot implicitly impersonate a provider. Authentication is never bypassed in normal composition; test verifiers are test-only.

The SQS queue is a trusted internal producer boundary. Fake LocalStack credentials do not enforce production IAM. `deploy/iam` provides separate scoped producer and combined runtime policies. Runtime permissions cover consumption, output publication and required queue metadata; provisioner/admin permissions are not runtime permissions. Untrusted providers must use authenticated HTTP. Actual AWS policy enforcement is a deployment responsibility, not claimed as an emulator test result.

## Wallet opening, reads and reconciliation

Positive wallet creation commits wallet version 1, processed OPENING, one CREDIT entry from zero, WagerTransactionProcessed and WalletBalanceChanged together. Zero creation emits no financial operation/entry/event. Duplicate player/currency creation is a database-enforced conflict.

Ledger reads use ascending `(created_at,id)` with an opaque base64url cursor bound to the wallet, default limit 50 and maximum 100. This is stable keyset pagination, not a multi-page snapshot guarantee.

Reconciliation uses one PostgreSQL statement/snapshot for stored balance and signed ledger sum, including opening. NUMERIC is used only for exact aggregate accumulation before checked int64 conversion. Difference is stored minus calculated. Divergence appears in the response, a JSON warning and an internal metric; reconciliation never changes the wallet.

## Observability

Operational JSON logs cover HTTP method/route/status, financial outcomes and available safe IDs, SQS handling, Outbox publication and reference attempts. Financial records include transaction correlation identity where available. Raw transport errors, tokens, credentials and full payloads are excluded from these operational records. Fx retains its standard lifecycle diagnostics.

Internal-only GET `/metrics` exposes outcomes by status, duplicates/replays, retries, PostgreSQL serialization/deadlock conflicts, processing latency and reconciliation divergences. A bounded sampler reads oldest unpublished Outbox age and approximate visible DLQ depth every 30 seconds. Counters reset per process; gauges retain the last successful sample and are not globally aggregated or durable audit records. Latency formatting uses a floating-point duration conversion, never a monetary conversion.

Public `/health/live` reports liveness; `/health/ready` checks PostgreSQL and input/output SQS queue availability with a three-second deadline. No tracing, dashboard or monitoring server is provisioned.

## Local packaging and verification

Compose pins PostgreSQL 17.11-bookworm, LocalStack 4.0.3, Keycloak 26.0.7 and golang-migrate v4.18.3. PostgreSQL has a persistent named volume. Queue initialization is idempotent and Keycloak imports test identities automatically. Host ports are PostgreSQL 5432, LocalStack 4566 and Keycloak 8081. Health checks verify the dependencies before the documented migration/application steps.

The Dockerfile pins Go 1.26.5 and a non-root distroless runtime by digest, with a static trimmed build and `/api` entrypoint. The application normally runs on the host at port 8080 and is not a Compose service. A container must reach an OIDC issuer URL identical to Keycloak's advertised issuer; the default localhost issuer is for host execution. Follow the complete README startup procedure, not a PostgreSQL-only setup.

Unit tests cover domain invariants, exact arithmetic, fingerprinting, event construction, authorization and delivery policy. Tagged integrations use real PostgreSQL, LocalStack and Keycloak. PostgreSQL fixtures apply all eight migrations in isolated temporary schemas; SQS fixtures use temporary queues. Migration round trips and protected downgrade cases are tested without deleting existing application data.

`TestThreeIndependentProcesses` launches three independent OS processes/pools and observes PostgreSQL lock waits before releasing the wallet barrier. It asserts the exact two-80.00-BET result against 100.00, independent-wallet progress, duplicates and replacement-process replay. `TestReferenceWorkerProcessRestart` verifies automatic continuation in a replacement process. `TestHTTPAndSQSConcurrentIdentity` observes real HTTP/SQS contention for the same identity and verifies one financial effect. Older goroutine/client start barriers alone are not described as proof of database contention.

Outbox/Inbox tests cover transactional rollback, ambiguous outcomes, stable-ID publication recovery and native redelivery/DLQ. OIDC tests combine cryptographic negative cases with a real Keycloak HTTP smoke flow. Lifecycle tests cover worker startup/shutdown. Manual scenarios supplement automated tests; the combined changed-Inbox-body-through-DLQ scenario is not claimed as one dedicated automated test. Exact commands and build tags are in the README.

## Deliberate trade-offs

- External BRL-only policy avoids a currency-registry dependency while preserving generic Money arithmetic.
- Pessimistic per-wallet locking favors straightforward financial consistency; unrelated wallets remain independent.
- Single-entry append-only ledger, full reversals and one combined successful reversal per reference keep financial rules explicit.
- Committed asynchronous work is represented by pending references; no separate intermediate PENDING acceptance pipeline is needed.
- Delivery is at-least-once with idempotent consumers, not an exactly-once transport claim.
- Local credentials, development Keycloak and emulator configuration are not production hardening or AWS authorization verification.
- Distributed tracing, dashboards, load testing, double-entry accounting, Kubernetes and cloud deployment automation are not implemented or required for the documented local solution.
