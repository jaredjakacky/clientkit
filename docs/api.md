# API map

This page is a navigation aid. The canonical contracts, field semantics, nil
behavior, validation, and concurrency guarantees live in the
[Go package documentation](https://pkg.go.dev/github.com/jaredjakacky/clientkit).

## Package map

| Package | Import when the application needs |
| --- | --- |
| `clientkit` | Shared identity, health, readiness, observers, registry, or Opskit integration |
| `clientkit/httpclient` | Production HTTP execution |
| `clientkit/tcpclient` | Raw plaintext or TLS connection establishment |
| `clientkit/otel` | OpenTelemetry for logical and direct remote operations, retries, and health |
| `clientkit/httpclient/otel` | Physical HTTP transport spans, propagation, and optional standard metrics |
| `clientkit/slogobserver` | Structured Clientkit lifecycle logs |

## Root `clientkit`

### Client identity and configuration

- `Config` defines stable name, readiness policy, observer, and health sanitizer.
- `New` constructs the transport-neutral core used by protocol packages.
- `Client` exposes immutable identity, cached health, and observer behavior.
- `ValidateClientName` and `ValidateClientProtocol` validate bounded identity
  vocabularies.

Most applications construct `httpclient.Client` or `tcpclient.Client` rather
than using the root `Client` directly.

### Health and failure

- `HealthState` and `Health` describe a cached operational observation.
- `HealthAssessment` is the lifecycle-free decision returned by protocol
  probes.
- `FailureClass` is the stable bounded cause used across protocols.
- `HealthSanitizer` and `DefaultHealthSanitizer` control what enters operational
  and telemetry surfaces.

Raw errors remain on protocol results. `FailureClass` supplements ordinary Go
errors rather than replacing them.

### Readiness

- `ReadinessPolicy` defines required, degraded-allowed, optional, and
  informational client participation.
- `RegisteredClient` is the minimal identity/readiness/health contract accepted
  by `Registry`.
- `HealthChecker` and `HealthCheckConfigurable` are optional active-check
  capabilities.

### Registry and Opskit

- `NewRegistry` uses production defaults.
- `NewRegistryWithConfig` sets immutable check concurrency and Opskit identity.
- `Register` and `RegisterAll` perform checked static registration.
- `MustRegister` and `MustRegisterAll` are startup-composition conveniences.
- `Snapshot`, `Status`, `Readiness`, and `Inspect` are passive.
- `CheckAll` actively executes enabled checks.
- `CloseIdleConnections` invokes the optional `IdleConnectionCloser` capability.

`Registry` implements `opskit.Component`, `opskit.ReadinessContributor`,
`opskit.Inspector`, and `opskit.CheckGroup`.

### Observation

- `Observer` starts logical or direct remote operations and receives attempt,
  retry, and health events.
- `OperationObservation` completes one started operation.
- Event structures contain bounded protocol-neutral values plus the original
  error where the observer explicitly chooses to use it.
- `NopObserver` disables observation.
- `SafeObserver` contains custom-observer panics.
- `MultiObserver` composes observers explicitly.

## `httpclient`

### Construction and defaults

- `Config` combines root configuration with base URL, HTTP client, origin,
  classifier, timeout, check, retry, and propagation policy.
- `New` validates and constructs without I/O.
- `DefaultTransport` returns an independent production transport.
- `DefaultHTTPClient` returns an independent client using that transport.

### Requests and execution

- `NewRequest` resolves a relative RFC 3986 reference.
- `Do` returns ordinary `net/http` response/error semantics.
- `Execute` returns a detailed `Result`.
- `ExecuteWithOptions` applies immutable per-call policy.
- `OperationName` provides a low-cardinality semantic operation identity.

### Results and classification

- `Result` describes the complete logical operation.
- `Attempt` describes one Clientkit execution attempt.
- `Outcome` distinguishes success, response rejection, timeout, cancellation,
  and execution failure.
- `ResponseClassifier` and `ResponseDisposition` define acceptance policy.
- `AcceptStatus`, `AcceptAnyStatus`, `AcceptStatusClass`, and
  `AcceptStatusRange` construct immutable classifiers.
- `SafeResponseClassifier` contains panics and invalid dispositions.

### Retries and per-call options

- `RetryConfig` is one complete retry policy.
- `DefaultRetryConfig` returns an independently mutable copy of defaults.
- `NoRetryConfig` allows one Clientkit attempt.
- `RetrySafety` independently authorizes semantic repetition.
- `ExecutionRetry` selects an inherited, disabled, or complete per-call policy.
- `ExecutionTimeouts` overrides total and attempt limits independently.
- `ExecuteOptions` combines these per-call choices.

### Propagation

- `HeaderPropagator` injects outbound headers.
- `NopHeaderPropagator`, `SafeHeaderPropagator`, and
  `MultiHeaderPropagator` disable, protect, and compose propagation.
- Context and request-metadata helpers bind application-owned accessors to a
  bounded fixed header vocabulary.

### Health and cleanup

- `CheckConfig` defines an independent health request.
- `DefaultCheckConfig` enables a production check with no automatic retries.
- `Check`, `Health`, and `HealthCheckEnabled` expose active and passive behavior.
- `CloseIdleConnections` releases currently idle HTTP connections.

## `tcpclient`

### Construction and dialing

- `Config` defines immutable network, endpoint, dialer, timeout, keepalive,
  check, and TLS policy.
- `New` validates and constructs without I/O.
- `Dial` returns an ordinary caller-owned `net.Conn`.
- `DialResult` returns the connection plus bounded result metadata.
- `DialContext` provides an endpoint-bound integration method.
- `DialContextFunc` is the custom raw-dial extension point.

### TLS

- `TLSConfig` enables TLS, supplies an optional complete `tls.Config`, controls
  server-name inference, and sets the handshake timeout.
- `DefaultTLSConfig` creates a verifying policy with Clientkit's minimum TLS
  version.

### Health

- `CheckConfig` enables connect-and-close checks.
- `ConnectionProbe` and `ConnectionProbeFunc` optionally perform a protocol
  exchange over the check connection.
- `Check`, `Health`, and `HealthCheckEnabled` expose active and cached behavior.

TCP Clientkit deliberately has no idle-connection closer because it never owns
successful connections after return.

## OpenTelemetry adapters

### `clientkit/otel`

- `New` constructs the root Observer adapter.
- Provider options select explicit tracer and meter providers.
- Span and metric common-attribute options are independent.
- `WithErrorDetails` explicitly opts into recording raw errors on traces.

This adapter creates INTERNAL spans for logical operations and CLIENT spans for
direct remote operations such as TCP dialing. It does not instrument HTTP
`RoundTrip` or inject headers.

### `httpclient/otel`

- `New` constructs an OpenTelemetry header propagator.
- `NewWithTextMapPropagator` uses an explicit propagation policy.
- `NewTransport` wraps an `http.RoundTripper` with one CLIENT span per physical
  send.
- `WithRequestTargetAttributes` opts into server and sanitized URL span data.
- `WithStandardClientMetrics` opts into standard HTTP duration metrics and
  required server dimensions.

## `slogobserver`

- `New` constructs a synchronous structured-log Observer.
- `DefaultLevelConfig` returns production levels.
- `WithLevels` completely replaces the level policy.
- `WithAttributes` adds application-controlled common attributes.
- `WithErrorDetails` opts into raw error logging.

## Default and override semantics

| Location | Zero value |
| --- | --- |
| `clientkit.Config.ReadinessPolicy` | Optional |
| `RegistryConfig.MaxConcurrentChecks` | Production default of four |
| HTTP client timeout fields | Production durations |
| `httpclient.Config.Retry` | `DefaultRetryConfig` |
| `httpclient.CheckConfig` | Disabled |
| `httpclient.CheckConfig.Retry` | One attempt |
| Per-call `ExecutionRetry` | Inherit client policy |
| Per-call `ExecutionTimeouts` | Inherit client policy field by field |
| `tcpclient.Config.Network` | `tcp` |
| TCP timeout and keepalive fields | Production durations |
| `tcpclient.TLSConfig` | Plaintext |
| `tcpclient.CheckConfig` | Disabled |

Disable flags are explicit and cannot be combined with a positive value for the
same setting. Non-zero custom retry policies and custom TLS configurations are
complete replacements unless the field documentation says otherwise.

## Suggested reading order

1. [Getting started](getting-started.md).
2. [Usage](usage.md).
3. [Operational safety](operational-safety.md).
4. [Health and readiness](health-readiness.md) when enabling checks.
5. [Observability](observability.md) before configuring adapters.
6. Canonical Go documentation for the exact symbols being used.
