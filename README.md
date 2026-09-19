# Jungle Gaming — Go Financial Backend

A Go backend for processing gaming-provider transactions through HTTP and SQS with shared financial guarantees. The solution uses Uber Fx, PostgreSQL, Keycloak and LocalStack. It implements exact money, per-wallet database locking, persistent financial idempotency, an append-only ledger, transactional Inbox/Outbox and automatic pending-reference recovery.

This document describes the **implemented solution and how to run it**. The [challenge requirements summary](#challenge-requirements-summary) preserves the evaluation context separately. Detailed implementation decisions are in [ARCHITECTURE.md](ARCHITECTURE.md).

## Architecture and financial behavior

`cmd/api` composes the application. Domain packages contain Money, Wallet, WagerTransaction and ledger entries. Application code coordinates financial processing; HTTP and SQS share that execution path. PostgreSQL owns durable coordination. No process-local lock or FIFO deduplication is relied on for financial correctness.

External HTTP financial/wallet input and SQS commands support **BRL only**, including lowercase normalization. Amounts are decimal strings with up to two fractional digits; output always has two decimals. Internally, Money uses signed `int64` minor units with explicit overflow checks. There is no floating-point financial arithmetic.

| Operation | Implemented behavior |
| --- | --- |
| `BET` | Positive amount; debit or durable `BET_INSUFFICIENT_FUNDS` rejection. |
| `WIN` | Positive amount; credit. An optional reference must identify a compatible BET; WIN amount is independent of the BET amount. |
| `LOSS` | Zero amount (`"0.00"`); no balance/version change or ledger entry; produces a processed event. |
| `REFUND` | Full credit of a processed BET; reference required. |
| `ROLLBACK` | Reverses a processed BET, WIN or REFUND in full; reference required. Reversing a credit requires sufficient current balance. |
| `OPENING` | Internal only. Positive initial balance creates a processed opening, credit ledger entry and events atomically at wallet version 1. Zero initial balance creates only the wallet. HTTP/SQS reject external OPENING. |

References must match provider, player, wallet, currency and round; game ID is intentionally not compared. REFUND/ROLLBACK amounts must equal the referenced amount. One successful reversal, across REFUND and ROLLBACK combined, is permitted per reference. Rolling back a REFUND does not reopen its original BET for another reversal.

Unavailable compatible references produce durable `PENDING_REFERENCE`. Rejections are terminal and carry stable failure codes. See [reference and failure semantics](ARCHITECTURE.md#references-and-failure-semantics).

## Prerequisites

- Go **1.26.5**.
- Docker Engine/Desktop with Docker Compose supporting `--wait`.
- `curl` and Python 3 for the examples.
- A POSIX-compatible shell; run commands from the repository root.
- Available default ports: PostgreSQL **5432**, LocalStack **4566**, Keycloak **8081**, API **8080**.

Clone the submitted repository and enter its root before continuing. AWS CLI installation on the host is not required: the SQS example uses `awslocal` inside LocalStack.

## Local environment

Only if `.env` does not already exist, copy the example:

```sh
if [ ! -f .env ]; then
  cp .env.example .env
fi
```

Review the file and load this **trusted local development file** into the shell:

```sh
set -a
. ./.env
set +a
```

Docker Compose reads `.env`; sourcing it exports the same values for the host-run Go process. The application does **not** automatically load dotenv files. Repeat the sourcing step in every terminal used to run the application or integration tests. Do not overwrite an existing `.env`.

The example contains fake local credentials and usable database URLs. If you change PostgreSQL credentials/port, update both `DATABASE_URL` and `TEST_DATABASE_URL`. Changing database initialization variables does not change credentials in an existing PostgreSQL volume. If you change the LocalStack port, update `SQS_ENDPOINT` and `TEST_SQS_ENDPOINT` too.

## Start dependencies

```sh
docker compose config --quiet
docker compose up -d --wait postgres localstack keycloak
```

Compose automatically provisions:

- Persistent PostgreSQL storage and a health check.
- LocalStack input FIFO, FIFO DLQ/redrive and Standard output queue.
- Keycloak realm `wager`, confidential service clients and the `wager-api` audience.

The Go application itself is **not** a Compose service. The dependency health checks and explicit migration step precede host application startup.

## Database migrations

There are **eight migration versions**, using golang-migrate up/down SQL files in `migrations/`.

```sh
docker compose run --rm migrate up
docker compose run --rm migrate version
```

A fully migrated database reports version `8` without a dirty state. Migrations run through a separate tools-profile service, not independently inside every application instance.

For a **disposable development database**, stop application workers before testing reversal:

```sh
docker compose run --rm migrate down 1
docker compose run --rm migrate up
```

At version 8, `down 1` removes the reference scheduling columns/index, not a table. Reapplying version 8 does not restore the removed retry history. Earlier downgrades can remove financial structures or refuse incompatible/populated history; they are not a production rollback procedure. Integration tests exercise migrations in isolated schemas.

`migrate down` changes the database schema. `docker compose down` stops/removes Compose resources; the PostgreSQL named volume remains unless explicitly removed. Do not remove volumes containing data you want to preserve.

## Start the API

In the shell where `.env` was exported:

```sh
go run ./cmd/api
```

Use another terminal, also with `.env` exported, for requests:

```sh
curl -fsS http://localhost:8080/health/live
curl -fsS http://localhost:8080/health/ready
```

Liveness reports process availability. Readiness checks PostgreSQL and the configured SQS input/output queues. Shutdown stops new work and drains or cancels bounded work before closing dependencies.

## Authentication

Local Keycloak issuer: `http://localhost:8081/realms/wager`. These secrets are **fake development credentials only**.

| Client | Local secret | Permission |
| --- | --- | --- |
| `provider-a` | `local-provider-a-secret` | Provider A financial submissions and its own transaction reads/replays |
| `provider-b` | `local-provider-b-secret` | Provider B financial submissions and its own transaction reads/replays |
| `wallet-internal` | `local-wallet-internal-secret` | Wallet creation/reads, ledger, reconciliation and metrics |

Obtain client-credentials tokens in the request terminal:

```sh
TOKEN_URL="$OIDC_ISSUER/protocol/openid-connect/token"
INTERNAL_TOKEN=$(curl -fsS "$TOKEN_URL" \
  -d grant_type=client_credentials -d client_id=wallet-internal \
  -d client_secret=local-wallet-internal-secret \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
PROVIDER_TOKEN=$(curl -fsS "$TOKEN_URL" \
  -d grant_type=client_credentials -d client_id=provider-a \
  -d client_secret=local-provider-a-secret \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
PROVIDER_B_TOKEN=$(curl -fsS "$TOKEN_URL" \
  -d grant_type=client_credentials -d client_id=provider-b \
  -d client_secret=local-provider-b-secret \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
```

The validated token determines provider identity; the request's `providerId` must agree with it. A provider cannot select another provider's namespace or use wallet/internal endpoints. The internal principal does not implicitly impersonate a provider. Obtain new tokens when they expire.

## HTTP examples

Create a wallet and retain its returned ID. The player ID below changes per run to avoid colliding with an existing player/currency wallet.

```sh
PLAYER_ID="demo-$(python3 -c 'import uuid; print(uuid.uuid4().hex)')"
WALLET_JSON=$(curl -fsS http://localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER_ID\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}")
WALLET_ID=$(printf '%s' "$WALLET_JSON" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
EXTERNAL_ID="bet-$PLAYER_ID"
BET_BODY=$(python3 -c 'import json,sys; print(json.dumps({"providerId":"provider-a","externalTransactionId":sys.argv[1],"playerId":sys.argv[2],"walletId":sys.argv[3],"roundId":"demo-round","gameId":"demo-game","kind":"BET","money":{"amount":"80.00","currency":"BRL"}}))' \
  "$EXTERNAL_ID" "$PLAYER_ID" "$WALLET_ID")
BET_JSON=$(curl -fsS http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $EXTERNAL_ID" -d "$BET_BODY")
printf '%s\n' "$BET_JSON"
TRANSACTION_ID=$(printf '%s' "$BET_JSON" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["transactionId"])')
```

The BET returns `PROCESSED` and `{"amount":"20.00","currency":"BRL"}`. Repeating the same submission returns the saved result with `idempotentReplay: true`, without another debit. Reusing its key with different business content returns a conflict. A new key cannot reapply the same provider/external identity.

```sh
curl -fsS "http://localhost:8080/wagering/transactions/$TRANSACTION_ID" \
  -H "Authorization: Bearer $PROVIDER_TOKEN"
curl -fsS "http://localhost:8080/providers/provider-a/wagering/transactions/$EXTERNAL_ID" \
  -H "Authorization: Bearer $PROVIDER_TOKEN"
curl -fsS "http://localhost:8080/wallets/$WALLET_ID" \
  -H "Authorization: Bearer $INTERNAL_TOKEN"
curl -fsS "http://localhost:8080/wallets/$WALLET_ID/ledger?limit=50" \
  -H "Authorization: Bearer $INTERNAL_TOKEN"
curl -fsS -X POST "http://localhost:8080/wallets/$WALLET_ID/reconciliation" \
  -H "Authorization: Bearer $INTERNAL_TOKEN"
```

Ledger pagination returns an opaque `nextCursor`; URL-encode it when supplying `cursor`. Reconciliation includes OPENING, reports stored minus calculated balance and never repairs the wallet. Transaction lookups expose status/failure code and the saved balance. A provider-B lookup of the provider-A transaction returns 404.

| HTTP status | Meaning |
| --- | --- |
| 200 | Successful processing/replay or read/reconciliation |
| 201 | Wallet created |
| 202 | Pending processing/reference; normally `PENDING_REFERENCE` |
| 400 | Invalid JSON, Money, context or missing/invalid idempotency key |
| 401 / 403 | Invalid/missing authentication / unauthorized operation or provider mismatch |
| 404 | Unavailable resource or transaction hidden by provider isolation |
| 409 | Idempotency conflict or duplicate player/currency wallet |
| 422 | Durable business rejection, including rejected replay |
| 500 / 503 | Invalid persisted/internal state / infrastructure or capacity failure |

Boundary errors use `{"error":"CODE"}`, for example `{"error":"INVALID_MONEY"}`. Financial outcomes use `transactionId`, `status`, `balance`, `idempotentReplay` and nullable `failureCode`. A pending outcome has null balance; a rejected outcome includes its stable failure code and unchanged saved balance. Use `curl -sS -i` instead of `-f` to inspect expected error responses.

## Submit an SQS command

| Queue | Role |
| --- | --- |
| `wager-transactions.fifo` | Inbound commands |
| `wager-transactions-dlq.fifo` | Poison/exhausted commands through native redrive |
| `wager-events` | Standard queue for outbound integration events |

After the HTTP example, send the **same BET identity** through SQS. It should replay the financial result and create an Inbox completion without another debit. The generated envelope ID is separate from the financial identity.

```sh
MESSAGE_ID=$(python3 -c 'import uuid; print(uuid.uuid4().hex)')
MESSAGE_BODY=$(python3 -c 'import datetime,json,sys; data=json.loads(sys.argv[1]); data["idempotencyKey"]=sys.argv[2]; print(json.dumps({"messageId":sys.argv[3],"type":"WagerTransactionRequested","occurredAt":datetime.datetime.now(datetime.timezone.utc).isoformat(),"data":data}))' \
  "$BET_BODY" "$EXTERNAL_ID" "$MESSAGE_ID")
GROUP_ID=$(python3 -c 'import hashlib,sys; print(hashlib.sha256(sys.argv[1].encode()).hexdigest())' "$WALLET_ID")
DEDUP_ID=$(python3 -c 'import hashlib,sys; print(hashlib.sha256(sys.argv[1].encode()).hexdigest())' "$MESSAGE_ID")
INPUT_URL=$(docker compose exec -T localstack awslocal sqs get-queue-url \
  --queue-name wager-transactions.fifo --query QueueUrl --output text)
docker compose exec -T localstack awslocal sqs send-message \
  --queue-url "$INPUT_URL" --message-body "$MESSAGE_BODY" \
  --message-group-id "$GROUP_ID" --message-deduplication-id "$DEDUP_ID"
```

The consumer runs with the application. Processing is asynchronous; use the lookup and ledger commands above to inspect the result. For a distinct operation, use a new external identity and idempotency key.

Inbox identity is `(consumerName, envelope.messageId)`, **not** the SQS-generated transport `MessageId`. The original body is hashed; changing even whitespace under an existing envelope ID conflicts. FIFO transport deduplication and durable Inbox/financial idempotency are separate protections. Tests deliberately bypass FIFO deduplication when proving repeated application receipt.

Deletion happens only after confirmed durable processing, including business rejection or a persisted reference wait. Failures retain the message for retry. Default visibility is 60 seconds, processing timeout 20 seconds and max receive count 5; visibility backoff doubles from 1 second up to 60 seconds. Malformed messages and hash conflicts reach the DLQ rather than being acknowledged as successful financial operations.

The local emulator uses fake credentials. Trusted producers may choose provider identity; untrusted providers must use authenticated HTTP. Deployment permissions and emulator limitations are documented in [deploy/iam/README.md](deploy/iam/README.md).

## Inbox, Outbox and pending-reference recovery

Financial state, ledger, Inbox completion and applicable event snapshots are committed together. A separate Outbox worker publishes only committed records. Delivery is **at-least-once**: a crash after sending but before marking publication may cause a duplicate with the same `eventId`. Downstream consumers must deduplicate by event ID.

The output contracts are `WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged` and `WagerTransactionPendingReference`. Events have stable identity, type/version, aggregate/correlation IDs, optional causation ID, UTC occurrence time and typed data. Money is serialized as decimal strings. Output ordering is not guaranteed; use terminal-state protection and wallet versions when consuming snapshots. See [event contracts](ARCHITECTURE.md#transactional-outbox-and-events).

A worker automatically discovers durable `PENDING_REFERENCE` transactions. Scheduling survives restart and uses 1, 2, 4, 8, 16, 32, then 60-second delays, capped at the original deadline. Empty/error polling is bounded to one second. The configured `REFERENCE_PENDING_TTL` defaults to 24 hours and is the only business exhaustion policy. At/after the deadline, the existing resolver rejects with `REFERENCE_NOT_FOUND` and persists the rejection event. Infrastructure failures still retry; they do not erase pending work or extend its deadline.

## Tests and concurrency guarantees

Unit/static verification does not require running infrastructure:

```sh
go test ./...
go test -race ./...
go vet ./...
```

The sourced `.env.example` already supplies test settings. These explicit commands show the **default** values; use your configured equivalents if you changed ports/credentials:

```sh
export TEST_DATABASE_URL='postgres://wager:local_only@localhost:5432/wager?sslmode=disable'
export TEST_SQS_ENDPOINT='http://localhost:4566'
export TEST_OIDC_ISSUER='http://localhost:8081/realms/wager'

go test -tags=integration -count=1 ./...
go test -tags=integration,sqsintegration -count=1 ./...
go test -tags=integration,oidcintegration -count=1 ./...

go test -race -tags=integration -count=1 \
  -run '^TestThreeIndependentProcesses$' \
  ./internal/infrastructure/postgres

go test -race -tags=integration,sqsintegration,oidcintegration -count=1 ./...
```

`integration` requires real PostgreSQL; adding `sqsintegration` requires LocalStack; adding `oidcintegration` requires Keycloak. Tests apply all eight migrations in isolated temporary schemas and use temporary SQS queues. The test database account must be allowed to create schemas. The application need not already be running; tests construct their own processors/servers.

`TestThreeIndependentProcesses` launches independent OS processes with independent PostgreSQL pools. It holds a wallet row lock and observes both competing sessions waiting in PostgreSQL before releasing the barrier. A third process demonstrates independent-wallet progress; replay is also verified through a replacement process.

The critical assertion is:

**BRL 100.00 + two distinct concurrent BRL 80.00 BETs → one PROCESSED, one REJECTED with BET_INSUFFICIENT_FUNDS, final BRL 20.00 and exactly one BRL 80.00 debit.**

`TestHTTPAndSQSConcurrentIdentity` instead checks the **same** financial identity arriving over real HTTP and LocalStack concurrently, producing one effect and a consistent replay. The older `TestFinancialConcurrency` and `TestHTTPConcurrentInstances` synchronize starts; those start barriers alone are not proof of observed database contention.

Focused recovery/cross-transport commands:

```sh
go test -race -tags=integration -count=1 \
  -run '^(TestReferenceWorkerRecovery|TestReferenceWorkerProcessRestart|TestOutboxPublisherRecovery)$' \
  ./internal/infrastructure/postgres
go test -race -tags=integration,sqsintegration -count=1 \
  -run '^(TestHTTPAndSQSConcurrentIdentity|TestSQSInboundCommitDeleteAndRestart|TestSQSPoisonRedrive|TestSQSOutboundSnapshotAndAmbiguousSend)$' \
  ./internal/infrastructure/postgres
```

## Observability

Operational JSON logs cover HTTP outcomes, financial processing and safe identifiers, SQS handling, Outbox publication and reference recovery. Tokens, credentials and full financial payloads are not included in those operational records. Fx also emits its own lifecycle diagnostics.

```sh
curl -fsS http://localhost:8080/metrics \
  -H "Authorization: Bearer $INTERNAL_TOKEN"
```

Metrics expose financial outcomes by status, duplicates/replays, worker retries, PostgreSQL serialization/deadlock conflicts, processing latency, reconciliation divergences, oldest pending Outbox age and approximate visible DLQ depth. Counters are **per process and reset on restart**; there is no global aggregation. Queue/Outbox gauges are sampled every 30 seconds and retain the last successful sample. DLQ depth is not a cumulative redrive count.

## Docker and trade-offs

```sh
docker build -t backend-challenge-go:test .
```

The multi-stage Dockerfile pins Go 1.26.5 and the distroless runtime by digest, produces a static binary and runs as non-root with `/api` as entrypoint. HTTP defaults to port 8080. Host execution remains the documented local path; the application is not a Compose service.

For container execution, supply database/SQS settings and an issuer reachable from the container **whose URL exactly matches Keycloak's advertised issuer**. Replacing `localhost` with a service name only in `OIDC_ISSUER` is not sufficient. The default Keycloak configuration is intended for host access.

The solution chooses per-wallet pessimistic locking, a single-entry append-only ledger, strict full reversals and BRL-only external input. It does not implement tracing, dashboards, load benchmarks, double-entry accounting, Kubernetes or cloud deployment automation. These are not prerequisites for reproducing the challenge. Full decisions and limitations are in [ARCHITECTURE.md](ARCHITECTURE.md).

## Challenge requirements summary

This section preserves the original challenge's mandatory scope and evaluation context, separately from the execution guide above.

- Provide equivalent HTTP/SQS financial processing under at-least-once delivery, duplicate requests, out-of-order references, concurrent wallet updates, process interruptions and temporary PostgreSQL/SQS outages.
- Use Go Modules, Uber Fx, PostgreSQL, external OAuth2/OIDC, local SQS through LocalStack/MiniStack, Docker Compose, versioned reversible migrations and standard Go tests including `-race`.
- Keep the domain independent of infrastructure; validate creation/rehydration and classify errors. Money must be exact, currency-aware and overflow-safe, with two-decimal external serialization and no floating-point financial path.
- Enforce persistent financial identity/idempotency, non-negative balances, no lost updates, independent-wallet progress, append-only ledger and post-commit event publication. Wallet identity is unique per player/currency; financial identity is provider/external transaction ID.
- Implement all five external operations and internal OPENING, valid terminal-state transitions, full reference validation, durable pending-reference retry/TTL and stable rejection codes. A committed accepted operation must be recoverable; synchronous operations may complete without an intermediate accepted-state commit.
- Provide wallet creation/reads, cursor-paginated ledger, transaction reads, idempotent financial submission, consistent read-only reconciliation and public liveness/readiness. Provider identity comes from authentication; wallet operations are internal-only. Broker access requires scoped credentials/policies.
- Atomically coordinate financial state, ledger, persistent Inbox and typed Outbox events where applicable. Demonstrate redelivery, retry/DLQ, competing publishers and recovery around commit/publication/acknowledgment windows.
- Provide JSON operation logs, outcome/duplicate/retry/DLQ/conflict/Outbox-lag/latency/reconciliation metrics and graceful lifecycle management.
- Verify 50 parallel duplicate bets, the 100.00/two-80.00 scenario, independent wallets, at least three independent processes, HTTP/SQS identity overlap, reference recovery and real PostgreSQL/SQS/IdP integrations. Compare persisted balances with ledger results and document reproducible commands.

The evaluation weights are financial integrity 20, concurrency 20, idempotency 15, messaging/recovery 15, modeling/architecture 10, tests 10, observability 5 and documentation 5 points.

Eliminatory failures include ineffective business authentication, unauthorized access, floating-point money, concurrent negative balances, duplicate movements, memory-only idempotency, dependence on a single instance, publication before commit, no auditable ledger or replacing all real PostgreSQL/SQS/IdP integration with mocks. Double-entry accounting, tracing and load testing are optional; no minimum RPS is required. If load results are supplied, the challenge expects reproducible methodology, environment, throughput, latency percentiles, errors, conflicts and Outbox lag.
