# Architecture

This living document records implemented architecture and agreed design decisions. The [README](README.md) defines the challenge requirements; those requirements do not imply that a design has been selected or implemented. Update this document as decisions are made.

## Architecture direction

**Decided; partially implemented.** The Go application is composed with Uber Fx. The domain must remain independent of Fx, HTTP, SQS, and persistence libraries. HTTP handlers and the SQS consumer will eventually call the same application use cases. Infrastructure adapters will implement interfaces required by the application/domain layers.

Only configuration, application composition, and an HTTP server exist today. Domain models, application use cases, persistence adapters, and messaging components are not implemented yet.

## Application composition and lifecycle

**Implemented.** [`cmd/api/main.go`](cmd/api/main.go) is the composition root. Fx provides constructor dependency injection through `fx.Provide` and instantiates the HTTP server through `fx.Invoke`.

The HTTP server uses `net/http` and `fx.Lifecycle`: `OnStart` launches `ListenAndServe` in a goroutine, and `OnStop` calls `Shutdown` with the lifecycle context for graceful shutdown. Currently, serving errors are printed and are not propagated to Fx; successful startup does not confirm that the listener bound successfully. Worker lifecycle and resource shutdown ordering remain undecided.

## Configuration

**Implemented.** [`internal/config/config.go`](internal/config/config.go) reads environment variables.

| Variable | Default |
| --- | --- |
| `HTTP_HOST` | `0.0.0.0` |
| `HTTP_PORT` | `8080` |

Defaults apply when variables are absent. An explicitly empty `HTTP_PORT` is rejected. Further configuration validation and dependency settings are not implemented.

## Money

**Decided; not implemented.** `Money` will be an immutable domain value object containing an `int64` amount in minor units and a currency. Monetary values must never pass through `float32` or `float64` during parsing, calculation, serialization, or persistence.

External amounts use decimal strings with a fixed maximum scale of two decimal places. Operations across different currencies are invalid. Parsing and arithmetic must explicitly detect overflow, including addition, subtraction, and negation.

Accepted equivalent input forms, normalization, serialization details, and database mapping are **To be decided**.

## Concurrency direction

**Decided; not implemented.** Correctness must not rely on process-local locks or global locks. Coordination must work across multiple application instances, and independent wallets must be able to progress in parallel. The exact PostgreSQL locking strategy is not finalized.

## Future decisions

The following mechanisms are required by the challenge, but their designs have not been selected.

### PostgreSQL transaction boundaries — To be decided

Database access library, transaction ownership across repositories, isolation levels, and atomic grouping of wallet state, transactions, ledger, inbox, and outbox writes.

### Wallet concurrency control — To be decided

PostgreSQL locking or conditional update strategy, wallet version handling, lock ordering, and conflict retry limits.

### Persistent idempotency — To be decided

Schema and uniqueness rules, deterministic payload hashing and normalization shared by HTTP/SQS, conflict handling, and storage of original results for replay.

### Ledger persistence and immutability — To be decided

Schema, database protections against edits/deletions, financial constraints, opening entries, and consistent reconciliation reads.

### Inbox processing — To be decided

Message identity/hash storage, duplicate handling, transaction integration, and durable completion before SQS acknowledgment.

### Transactional outbox — To be decided

Event contracts and snapshots, output destination, concurrent publisher coordination, retries, and recovery with stable event identities.

### Reference and reversal processing — To be decided

Transaction state transitions, durable pending-reference scheduling and expiration, reference validation, and policies preventing duplicate financial reversals.

### OAuth2/OIDC authorization — To be decided

External identity provider, credential validation, provider identity mapping, permissions for internal wallet operations, and broker access policies. Authentication is not implemented.

### SQS retry and DLQ policy — To be decided

FIFO grouping/deduplication identifiers, visibility timeout, retry/backoff limits, redrive configuration, invalid-message handling, and shutdown behavior.

### Observability — To be decided

Structured logging, correlation propagation, metrics, and dependency readiness checks. Currently, only `GET /health/live` is implemented; HTTP serving errors use plain text output.

### Failure recovery — To be decided

Transient/permanent failure classification, durable resumption of accepted work, recovery of abandoned worker jobs, and validation through multi-instance crash/restart scenarios.
