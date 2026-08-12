# clientkit

[![Release](https://img.shields.io/github/v/release/jaredjakacky/clientkit?sort=semver)](https://github.com/jaredjakacky/clientkit/releases)
[![CI](https://github.com/jaredjakacky/clientkit/actions/workflows/ci.yaml/badge.svg)](https://github.com/jaredjakacky/clientkit/actions/workflows/ci.yaml)
[![Go Support](https://img.shields.io/badge/go%20support-1.25.x%20%7C%201.26.x-00ADD8)](https://github.com/jaredjakacky/clientkit/actions/workflows/ci.yaml)
[![License](https://img.shields.io/github/license/jaredjakacky/clientkit)](https://github.com/jaredjakacky/clientkit/blob/main/LICENSE)

Clientkit is the outbound-client shell for Go services. It keeps HTTP and TCP
usage recognizable while adding immutable client identity, bounded execution
policy, safe retries, timeouts, propagation, observability, cached health, and
readiness integration.

Clientkit is independently useful. It integrates with the rest of the Kit
Series through [Opskit](https://github.com/jaredjakacky/opskit), but it does not
require Servekit, Workerkit, Configkit, or Dependkit.

## What Clientkit owns

| Clientkit owns | The application owns |
| --- | --- |
| Stable outbound-client identity | Endpoint and credential configuration |
| HTTP execution, retry, and timeout policy | Whether repeating an operation is semantically safe |
| TCP connection establishment and optional TLS | Protocol exchanges over a returned `net.Conn` |
| Trace propagation and observer events | OpenTelemetry SDK, exporter, and provider lifecycle |
| Cached client health and readiness projection | Scheduling active checks |
| Safe bounded outcome and failure classifications | Closing returned HTTP bodies and TCP connections |

Clientkit is not service discovery, client-side load balancing, a circuit
breaker, an authentication framework, a generated SDK system, or a raw TCP
connection pool. [Dependkit](https://github.com/jaredjakacky/dependkit) remains
the generic external-dependency health package; Clientkit does not require it.

## Installation

```sh
go get github.com/jaredjakacky/clientkit@latest
```

Import only the packages needed by the application. The root package depends
only on Opskit outside the standard library. OpenTelemetry API dependencies are
kept in the relevant protocol and adapter packages; Clientkit never initializes
an SDK or exporter.

## HTTP quick start

```go
package main

import (
	"context"
	"io"
	"log"
	"net/http"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func main() {
	client, err := httpclient.New(httpclient.Config{
		Config:  clientkit.Config{Name: "payments"},
		BaseURL: "https://payments.example/api/",
	})
	if err != nil {
		log.Fatal(err)
	}

	request, err := client.NewRequest(context.Background(), http.MethodGet, "status", nil)
	if err != nil {
		log.Fatal(err)
	}

	response, err := client.Do(request)
	if err != nil {
		log.Fatal(err)
	}
	defer response.Body.Close()

	_, _ = io.Copy(io.Discard, response.Body)
}
```

`httpclient.New` validates configuration without performing network I/O.
`Do` preserves ordinary `net/http` response/error semantics: a response
rejected by Clientkit's classifier is still returned with a nil error. The
caller owns every returned response body and must close it.

## TCP/TLS quick start

```go
client, err := tcpclient.New(tcpclient.Config{
	Config:  clientkit.Config{Name: "events"},
	Address: "events.example:443",
	TLS:     tcpclient.TLSConfig{Enabled: true},
})
if err != nil {
	log.Fatal(err)
}

conn, err := client.Dial(context.Background())
if err != nil {
	log.Fatal(err)
}
defer conn.Close()
```

With TLS enabled and no custom `tls.Config`, Clientkit verifies certificates,
infers the verification name from the configured address, and requires TLS 1.2
or newer. A successful connection is an ordinary caller-owned `net.Conn`.
Clientkit does not retain it or place it in a hidden pool.

## Production defaults

| Area | Default |
| --- | --- |
| Readiness policy | Optional |
| Health checks | Disabled; cached health begins unknown |
| HTTP response policy | Accept 2xx |
| HTTP total timeout | 30 seconds |
| HTTP attempt timeout | 10 seconds |
| HTTP retries | Up to 3 attempts for selected idempotent methods and retryable failures |
| HTTP origin policy | Cross-origin requests and host overrides rejected |
| HTTP transport | Bounded production transport with HTTP/2 attempts enabled |
| TCP dial timeout | 5 seconds |
| TCP keepalive | 30 seconds with the built-in dialer |
| TCP security | Plaintext unless TLS is explicitly enabled |
| TLS handshake timeout | 10 seconds |
| Default TLS minimum | TLS 1.2 when Clientkit creates the TLS policy |

Zero often selects a documented production default. Explicit disable fields
remove the corresponding Clientkit layer, while parent contexts and
caller-owned client behavior remain authoritative. Custom retry and TLS
configurations are complete policies rather than partial merges. Consult the
[API map](docs/api.md) and Go documentation before overriding defaults.

## Packages

| Package | Responsibility |
| --- | --- |
| `clientkit` | Transport-neutral identity, health, failure classification, observers, registry, and Opskit contracts |
| `httpclient` | HTTP request construction, execution, retries, timeouts, health checks, and results |
| `tcpclient` | Raw TCP connection establishment, optional TLS, probes, health checks, and results |
| `clientkit/otel` | Logical-operation, direct-remote, retry, and health OpenTelemetry adapter |
| `httpclient/otel` | Per-`RoundTrip` CLIENT spans, propagation, and optional standard HTTP metrics |
| `slogobserver` | Safe structured logging adapter |

Packages under `internal/` are implementation details and must not be imported.

## HTTP execution model

### `Do`, `Execute`, and `ExecuteWithOptions`

| Method | Use it when |
| --- | --- |
| `Do` | Ordinary `net/http` response/error semantics are enough |
| `Execute` | The caller needs Clientkit `Result`, `Outcome`, attempts, and `FailureClass` |
| `ExecuteWithOptions` | One operation needs an explicit name, classifier, retry policy, retry-safety assertion, or timeout override |

`Outcome` answers what happened to the logical operation. `FailureClass`
provides a stable, bounded classification suitable for policy and telemetry.
`Result.Err` remains the original caller-visible Go error. Response rejection
is policy information and does not manufacture a transport error.

### Retries require authorization

An HTTP retry occurs only when all three independent gates allow it:

1. The selected retry policy permits the method and failure or status.
2. `RetrySafety` says repeating the operation is semantically safe.
3. A request body is absent or mechanically replayable through `Request.GetBody`.

The default policy does not blindly retry POST, PATCH, CONNECT, or custom
methods. Authorizing a POST with `RetrySafetyIdempotent` is an application
assertion. Clientkit does not create or validate idempotency keys, and a retry
after a timeout can duplicate a side effect.

`http.NewRequest` and `http.NewRequestWithContext` populate `GetBody` for common
in-memory readers such as `bytes.Buffer`, `bytes.Reader`, and `strings.Reader`.
`ExecuteWithOptions` takes ownership of a non-nil request body and closes it,
including when validation prevents the first attempt.

See [Usage](docs/usage.md) for complete retry and body rules.

### Timeouts and response bodies

The total timeout spans attempts, retry delays, and final response-body use.
The attempt timeout restarts for each Clientkit attempt and also remains active
for the final body. A parent context, `http.Client.Timeout`, or transport limit
may end work earlier.

Logical and physical observations finish when final response headers or a
terminal error are available; they do not measure body consumption. Timeout
cleanup remains attached to the final body until EOF, body error, close, or
context completion. Always read or close the body promptly. If every timeout is
disabled and the parent has no deadline, abandoning the body can retain
resources indefinitely.

### URL and origin safety

`NewRequest` uses normal RFC 3986 reference resolution. The `BaseURL` path is a
convenient base, not a confinement boundary: root-relative and parent references
can replace or escape it. Absolute references and fragments are rejected.

Execution rejects changes to scheme, host, or effective port by default, as
well as `Request.Host` overrides. Enabling cross-origin execution may forward
caller-supplied headers or permit an HTTPS downgrade and should be paired with a
restrictive redirect policy.

A caller-supplied `*http.Client` remains caller-owned and is never mutated.
Clientkit copies its value to compose execution policy. Calling
`CloseIdleConnections` may affect other users when its transport is shared.

## Health and cached readiness

Protocol checks are disabled by default. `Check` and `Registry.CheckAll` are the
active operations that may contact dependencies. `Health`, `Snapshot`,
`Status`, `Readiness`, and `Inspect` only project cached state and never perform
synchronous dependency I/O.

Clientkit creates no scheduler or background goroutine. Applications may run
checks directly or use Workerkit to periodically execute the Registry's Opskit
`CheckGroup`. Servekit can then present the same cached state through Opskit.
See [Health and readiness](docs/health-readiness.md) and
[Composition](docs/composition.md).

## Observability

When Clientkit owns the default HTTP client and no observer is supplied, it
installs the complete default HTTP model:

- One logical Clientkit HTTP operation represented by an INTERNAL span.
- One CLIENT span for every physical `RoundTrip`, including retries and redirects.
- Trace-context injection from the corresponding physical attempt span.
- Low-cardinality Clientkit operation, attempt, retry, and health metrics.

A caller-owned HTTP client or a non-nil custom observer replaces part of that
automatic boundary. Use `httpclient/otel.NewTransport` and
`clientkit/otel.New` explicitly when those cases still need complete tracing.

Standard HTTP duration metrics and request-target span attributes are opt-in
because they introduce server identity. Raw errors are excluded by default from
OpenTelemetry and slog because they may contain URLs, hosts, certificate text,
or application data. Applications own SDK/exporter lifecycle and must configure
global providers and propagation before constructing Clientkit adapters.

See [Observability](docs/observability.md) and
[Operational safety](docs/operational-safety.md).

## Kit Series composition

```text
HTTP and TCP clients
        │
Clientkit Registry
        │
      Opskit
      ├── Workerkit periodically refreshes checks
      └── Servekit presents passive status/readiness/inspection
```

Clientkit's per-client readiness policy is separate from the policy used to
register the Clientkit Registry with Opskit. Register the Registry as required
when its client policies should gate service readiness. If Workerkit only
schedules those checks, disable that worker's independent readiness contribution
to avoid two gates for the same state.

## Documentation

- [Getting started](docs/getting-started.md)
- [Usage](docs/usage.md)
- [Health and readiness](docs/health-readiness.md)
- [Observability](docs/observability.md)
- [Operational safety](docs/operational-safety.md)
- [Kit Series composition](docs/composition.md)
- [API map](docs/api.md)
- [Examples](docs/examples.md)
- [Go package documentation](https://pkg.go.dev/github.com/jaredjakacky/clientkit)

## Examples

Runnable examples live under [`examples/`](examples/README.md). They use local
servers and listeners and require no external service or credentials.

```sh
go run ./examples/http-basic
go -C examples/kit-series-composition run .
```

## Development

```sh
make help
make verify
make test-race
make govulncheck
```

`make verify` checks formatting, the root dependency boundary, vet, tests,
runnable examples, and tidy module state.

## Maintenance and releasing

Report security issues using [SECURITY.md](SECURITY.md). Use the repository's
issue tracker for ordinary defects and proposals.

Releases are created through the manual GitHub Actions release workflow. The
workflow validates the requested semantic version and target commit, runs the
release verification gate, and creates the tag and GitHub Release only after
those checks pass. Do not manually push a release tag around that gate.

Clientkit intentionally keeps its scope narrow. New transports or policy
systems should be added only when a concrete outbound-client requirement cannot
be expressed through ordinary Go and the existing package boundaries.

## License

Clientkit is licensed under the terms in [LICENSE](LICENSE).
