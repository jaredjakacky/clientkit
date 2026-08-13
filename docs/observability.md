# Observability

Clientkit treats observability as part of outbound execution, while leaving SDK
and exporter lifecycle with the application. The neutral `Observer` contract
supports OpenTelemetry, slog, tests, and application-specific observers without
making the root package a telemetry framework.

## Ownership

The application owns:

- OpenTelemetry SDK construction.
- Tracer and meter providers.
- Exporters and sampling.
- Resource attributes.
- Global or explicit propagation policy.
- Provider shutdown and export flushing.

Configure global providers and the global text-map propagator before creating
Clientkit observers, transports, or clients. Adapters capture applicable global
values during construction.

Clientkit works without an SDK. The OpenTelemetry API behaves as a no-op until
the application installs providers.

## The Observer contract

The root `clientkit.Observer` receives logical operation, completed attempt,
scheduled retry, and health-check events. Observers are synchronous and may be
called concurrently. Implementations must return quickly and must not perform
unbounded network I/O.

Use:

- `clientkit.NopObserver` to disable observation explicitly.
- `clientkit.SafeObserver` to contain a custom observer panic.
- `clientkit.MultiObserver` for explicit additive composition.

`MultiObserver` chains returned contexts in registration order and completes
operation observations in reverse order. Built-in protocol clients protect
their lifecycle from observer panics.

## Default HTTP topology

One logical request with two retries produces three instrumented `RoundTrip`
invocations:

```text
application span
└── Clientkit logical HTTP operation (INTERNAL)
    ├── HTTP RoundTrip 1 (CLIENT)
    │   └── remote server span
    ├── HTTP RoundTrip 2 (CLIENT)
    │   └── remote server span
    └── HTTP RoundTrip 3 (CLIENT)
        └── remote server span
```

The logical span represents Clientkit policy: validation, attempts, retry waits,
and final classification. Each instrumented `RoundTrip` invocation owns a
CLIENT span and injects trace context from that span, so every remote server
span has the correct parent at that instrumentation boundary.

Redirects can create multiple `RoundTrip` spans inside one Clientkit execution
attempt. Attempt count and instrumented `RoundTrip` invocation count are
therefore related but not interchangeable.

When Clientkit rejects a 307/308 replay, only the completed redirect
`RoundTrip` is observed physically. That span records the 307/308 transport
response and is not itself a transport error. The single logical attempt and
operation end as `execution_error` with failure class `policy`, and no retry
event is emitted. When an authorized redirect is followed, both `RoundTrip`
spans carry the same Clientkit attempt number and the second carries resend
count one.

Transport spans count calls through the instrumented `RoundTripper`, not an
absolute number of network sends. A wrapped transport, intermediary, or remote
system can repeat work below that boundary. In particular, Go's standard
`http.Transport` can transparently retry a narrow set of safe reused-connection
failures inside one instrumented `RoundTrip` invocation; that does not create a
new Clientkit attempt or retry event.

When classified transport policy rejects repetition, the failed attempt and
logical operation retain the original bounded `FailureClass`, but no retry event
is emitted. When repetition is allowed, the retry event carries that same class.
Raw errors are never introduced as metric labels.

Logical and physical HTTP spans end when final headers or a terminal error are
available. Body reads and closes do not extend spans. Clientkit timeouts still
govern final body use and callers must close bodies promptly.

Best-effort bounded cleanup of a Clientkit-owned retry response occurs after
that attempt's header-based observation and before its retry event. Final
health-check cleanup occurs before the health record is completed, so health
duration includes it. Cleanup does not expose body content, create another
attempt, or change the recorded response classification.

## Automatic HTTP instrumentation

Automatic behavior depends on ownership:

| HTTP client | Root observer | Logical observation | Physical transport observation |
| --- | --- | --- | --- |
| Clientkit default | nil | Automatic | Automatic |
| Clientkit default | non-nil | Supplied observer | Explicit if wanted |
| Caller-supplied | nil | Automatic | Explicit if wanted |
| Caller-supplied | non-nil | Supplied observer | Explicit if wanted |

A non-nil observer replaces the protocol-client default. A caller-supplied
`*http.Client` is never mutated or automatically wrapped.

Explicit complete wiring looks like:

```go
telemetry, err := clientkitotel.New()
if err != nil {
	return err
}

transport, err := httpclientotel.NewTransport(
	httpclient.DefaultTransport(),
)
if err != nil {
	return err
}

payments, err := httpclient.New(httpclient.Config{
	Config: clientkit.Config{
		Name:     "payments",
		Observer: telemetry,
	},
	BaseURL: "https://payments.example/",
	HTTPClient: &http.Client{
		Transport: transport,
	},
})
```

Aliases keep the two `otel` packages clear:

```go
clientkitotel "github.com/jaredjakacky/clientkit/otel"
httpclientotel "github.com/jaredjakacky/clientkit/httpclient/otel"
```

Do not wrap a transport again if another library already provides the desired
HTTP client instrumentation. Duplicate wrappers create duplicate spans,
metrics, and propagation work.

## Propagation

The default HTTP propagator captures the global OpenTelemetry text-map
propagator. Every physical transport span injects from its own context.

A custom `HeaderPropagator` completely replaces the default. Use
`MultiHeaderPropagator` for intentional composition or `NopHeaderPropagator` to
disable propagation. Propagated values may contain identifiers and must not be
copied into metric labels or operational snapshots.

## TCP spans

TCP dialing is already one direct remote operation. The root OpenTelemetry
adapter represents it as a CLIENT span; there is no additional nested wire span.
The span covers raw dialing and configured TLS handshaking through the accepted
connection or terminal error.

## Metrics

The root OpenTelemetry observer emits Clientkit-specific instruments:

| Instrument | Meaning |
| --- | --- |
| `clientkit.operations` | Completed logical operations |
| `clientkit.operation.duration` | Logical operation duration |
| `clientkit.operation.attempts` | Attempts used by an operation |
| `clientkit.attempts` | Completed execution attempts |
| `clientkit.attempt.duration` | Attempt duration |
| `clientkit.retries` | Scheduled retries |
| `clientkit.retry.delay` | Selected retry delay |
| `clientkit.health.checks` | Completed health checks |
| `clientkit.health.duration` | Health-check duration |

Default metric dimensions use bounded client names, protocol, operation,
outcome, success, failure class, HTTP method/status class, TCP network/security,
and TLS version where applicable. Unknown HTTP methods collapse to a bounded
fallback.

`httpclient/otel.WithStandardClientMetrics` enables
`http.client.request.duration`. It is opt-in because current semantic
conventions require server address and port dimensions. Those values are often
appropriate for traces but may be sensitive or high-cardinality for metrics.

Common span attributes and common metric attributes use separate options. Keep
metric attributes stable, bounded, and free from tenant, user, request, URL, and
resource identifiers.

## Request targets and error details

The default HTTP transport omits server address, port, and URL. Enabling
`WithRequestTargetAttributes` adds server identity and a sanitized `url.full`
without user information, query, or fragment. Paths can still expose business
or resource information; treat this as a deliberate data-governance decision.

Raw errors are excluded from OpenTelemetry and slog by default. Enabling
`WithErrorDetails` may expose:

- URLs, hosts, and ports.
- Certificate and TLS details.
- Transport messages.
- Application-controlled error text.

The application must provide exporter/logger access controls and redaction when
enabling this option. Clientkit never puts raw errors on metrics.

## Structured logging

`slogobserver` emits synchronous structured records. Its default levels are:

- Debug for successful operations, attempts, and healthy checks.
- Warn for retries and degraded, unhealthy, or unknown checks.
- Error for failed logical operations.

```go
logs := slogobserver.New(logger)

telemetry, err := clientkitotel.New()
if err != nil {
	return err
}

observer := clientkit.MultiObserver(logs, telemetry)
```

Common slog attributes are application-owned. They must be stable and must not
contain secrets. Prefer service identity on the logger itself.

## Custom observers and tests

A custom observer is useful for deterministic tests, in-process counters, or an
existing application telemetry layer. Keep it protocol-neutral: protocol
details should travel through the bounded event attributes already supplied by
Clientkit.

Use a recording observer in tests to verify attempt, retry, outcome, and health
events without asserting SDK implementation details. Wrap externally supplied
observers with `SafeObserver` unless their panic behavior is intentionally part
of the test.

## Related material

- [Operational safety](operational-safety.md)
- [Usage](usage.md)
- [API map](api.md)
- [`clientkit/otel` Go documentation](https://pkg.go.dev/github.com/jaredjakacky/clientkit/otel)
- [`httpclient/otel` Go documentation](https://pkg.go.dev/github.com/jaredjakacky/clientkit/httpclient/otel)
