# Examples

Clientkit intentionally has four runnable examples. Together they cover the
normal HTTP path, the HTTP policy boundary most likely to cause production
mistakes, safe raw TLS ownership, and the complete Kit Series health/readiness
composition. A larger catalog would repeat the focused guides and Go package
documentation.

All endpoints are local. The examples require no credentials, checked-in
certificates, Docker, or external infrastructure.

## Reading order

### 1. Basic HTTP

[`http-basic`](../examples/http-basic/main.go) constructs an HTTP client with
ordinary production defaults, creates a relative request, uses `Do`, and closes
the response body.

```sh
go run ./examples/http-basic
```

This is the short path for callers that want ordinary `net/http` response and
error semantics. The example points to `Execute` without adding structured
policy to the first program.

### 2. Retry and classification

[`http-retry-and-classification`](../examples/http-retry-and-classification/main.go)
combines the advanced HTTP concepts that must be understood together:

- Stable application-declared operation names.
- A response classifier accepting application-defined terminal statuses.
- A retry policy derived from production defaults.
- Replayable request bodies.
- Application-owned idempotency keys.
- Explicit semantic retry authorization.
- `Result`, `Outcome`, `FailureClass`, and attempt count.

```sh
go run ./examples/http-retry-and-classification
```

The first POST has policy permission and a replayable body but no semantic
authorization, so it executes once. The second POST explicitly asserts
idempotency and retries once. Clientkit does not create or validate the example's
idempotency key.

`Retry-After` is deliberately omitted. Its bounded behavior is documented in Go
doc, while adding a real delay or a no-op header would make this example larger
without improving the central safety lesson.

### 3. TCP/TLS

[`tcp-tls`](../examples/tcp-tls/main.go) starts a local TLS server, supplies the
server certificate as an explicit trust root, establishes a verified connection,
performs a tiny application-owned exchange, and closes the returned `net.Conn`.

```sh
go run ./examples/tcp-tls
```

The example never disables certificate verification. It intentionally omits a
health probe because TCP health scheduling belongs in the composition story,
while the normal TCP lesson is connection establishment and caller ownership.

### 4. Kit Series composition

[`kit-series-composition`](../examples/kit-series-composition) demonstrates the
architecture rather than a business-domain application:

```text
local HTTP dependency
        │
Clientkit HTTP client and Registry
        │ Opskit component, readiness, inspection, and check-group contracts
        ├───────────────┐
        ▼               ▼
Workerkit loop     shared Opskit Registry
refreshes health          │
                         ▼
                    Servekit /readyz
```

```sh
go -C examples/kit-series-composition run .
```

The program proves that:

- Clientkit owns the cached client health.
- Workerkit's generic `NewCheckGroupLoop` performs the active refresh.
- The Clientkit Registry and Workerkit Runtime share an Opskit Registry.
- Servekit reads cached readiness from `/readyz`.
- `/readyz` and protected component inspection do not perform dependency I/O.
- The check worker does not duplicate Clientkit's readiness contribution.
- Dependkit and pairwise adapters are unnecessary.

The composition directory is a nested Go module so its Servekit and Workerkit
dependencies never enter Clientkit's published root module graph. Its bounded
test exercises the same `run` function as the program.

## Build and verify

```sh
make build-examples
make verify
make test-race
make govulncheck
```

The Makefile compiles the three root-module programs and separately builds,
vets, tests, race-tests, tidy-checks, and vulnerability-checks the nested
composition module.

## What is intentionally absent

There are no separate examples for health-only checks, slog, an OpenTelemetry
SDK, plaintext TCP, custom dialers, probes, service discovery, pooling, circuit
breakers, Configkit, or Dependkit. Those additions would repeat documentation or
teach behavior outside Clientkit's current ownership boundary.

See:

- [Usage](usage.md) for detailed execution rules.
- [Health and readiness](health-readiness.md) for scheduling and policies.
- [Observability](observability.md) for explicit telemetry wiring.
- [Operational safety](operational-safety.md) for trust boundaries.
