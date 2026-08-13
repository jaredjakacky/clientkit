# Operational safety

Clientkit's default operational surfaces are designed for bounded cardinality
and minimal disclosure. Safe defaults do not remove application responsibility:
endpoint configuration, extension callbacks, health messages, and explicit
telemetry options remain trust boundaries.

## Data excluded by default

Clientkit does not automatically expose the following through registry
snapshots, Opskit inspection, metrics, spans, or slog records:

- Full URLs or request paths.
- Hosts, addresses, and ports.
- Query strings or fragments.
- User information or credentials.
- Authentication and arbitrary request headers.
- Request or response bodies.
- Connection strings.
- Raw Go errors.
- Certificate details.
- Arbitrary caller data or unbounded labels.

Registry inspection contains stable client name, bounded protocol identity,
readiness policy, health-check enablement, and sanitized cached health. It is an
operational view, not a network configuration dump.

## Identity and cardinality

Client names and HTTP operation names appear in telemetry. Declare them from a
fixed vocabulary in source or trusted static configuration.

Safe examples include `payments`, `events`, `payments.create`, and
`catalog.lookup`. Unsafe examples include URLs, tenant names, user IDs, request
IDs, random values, and database keys.

Application-supplied OpenTelemetry and slog attributes are not sanitized by
Clientkit. Keep metric values particularly small and bounded.

## HTTP URL boundaries

`BaseURL` rejects user information, query parameters, and fragments, but its
path is not a confinement boundary. `NewRequest` uses ordinary RFC 3986
resolution, so `/health` replaces a base path and `../resource` can move to a
parent path.

Do not place untrusted input into a relative reference without validating and
escaping the intended path segment. URL parsing is not authorization.

By default Clientkit rejects:

- Scheme, host, or effective-port changes.
- Cross-origin redirects.
- `Request.Host` overrides.
- Non-HTTP URL schemes.

`AllowCrossOrigin` can forward caller-supplied headers to another origin or
allow an HTTPS-to-HTTP downgrade. Enable it only with an application-owned
destination policy and a restrictive `http.Client.CheckRedirect` function.

Clientkit does not own authentication policy. Keep credentials out of
`BaseURL`, telemetry attributes, health messages, and operation names. Review
which headers a redirect or cross-origin exception can forward.

## Request metadata and propagation

Header propagators intentionally write data onto outbound requests. Trace
identifiers, baggage, correlation values, and application metadata may be
sensitive even when they are not credentials.

Context-header bindings and request metadata providers must:

- Use a bounded set of header names.
- Produce bounded values.
- Avoid secrets unless the remote protocol explicitly requires them.
- Never copy propagated values into metrics or operational snapshots.
- Be safe for concurrent use and honor the documented callback contract.

An explicit propagator replaces the default. Composition must use
`MultiHeaderPropagator`; do not assume two independent injectors will merge
implicitly.

## Request and response ownership

`ExecuteWithOptions` owns a non-nil request body once execution starts. Do not
reuse or mutate the request or its body concurrently. Clientkit closes the body,
including when validation prevents a first attempt.

Every final HTTP response body remains caller-owned. Read it to EOF or close it
promptly. Abandoning a body can prevent connection reuse and retain timeout
contexts or transport resources. Disabling all timeout layers makes prompt
caller cleanup even more important.

Clientkit performs bounded connection hygiene only for responses it owns and
discards. Eligible HTTP/1.x retry and health-check bodies are read to EOF or to
a 64 KiB threshold plus one EOF-probe byte, then closed. The existing attempt or
check deadline remains authoritative; without a live deadline, Clientkit closes
without reading. HTTP/2, protocol upgrades, known larger bodies, and
close-signaled responses are also close-only. Read or close errors are ignored
because cleanup must not replace the classified operation result, and response
content is never retained or emitted through telemetry.

A caller-supplied `*http.Client` remains caller-owned. Clientkit shallow-copies
its top-level fields during construction, while referenced transports, jars,
and callback state remain shared and caller-owned. `CloseIdleConnections` can
affect unrelated callers when the construction-time transport is shared. Stop
new work and drain active requests before application shutdown cleanup.

## Retry safety

Method spelling alone does not make an operation safe to repeat. A retry after a
timeout can duplicate a remote side effect even when no response was received.

Before using `RetrySafetyIdempotent`, confirm that at least one of the following
is true:

- The entire remote operation is intrinsically idempotent.
- The remote service implements application-level deduplication.
- A correctly scoped idempotency key is supplied and enforced remotely.

Clientkit does not generate, inspect, scope, or validate idempotency keys.
Request-body replayability is only a mechanical requirement; it says nothing
about semantic safety.

`RetrySafety` applies to both Clientkit-scheduled retries and method-preserving
307/308 redirects. The default rejects 307/308 for POST, PATCH, CONNECT, and
custom methods; `RetrySafetyNever` rejects every 307/308; and
`RetrySafetyIdempotent` explicitly authorizes them. Ordinary 301/302/303
redirect behavior remains unchanged. A non-empty body also needs `GetBody`, but
a bodyless unsafe operation still requires semantic authorization.

This policy does not guarantee exactly-once delivery. A caller-supplied
`RoundTripper`, the standard transport, intermediaries, or the remote system can
repeat or partially process work outside Clientkit's retry and redirect loops.
Application-level idempotency remains the authoritative protection.

## Transport failure retries

The production transport mode retries transient-looking or unclassified
failures, including refused, reset, and closed connections and DNS failures that
are not known to be not-found. It fails immediately for recognized TLS failures,
DNS not-found, and an invalid no-response/no-error `RoundTripper` result. Those
conditions normally require certificate, hostname, DNS, endpoint, or transport
implementation repair rather than another identical attempt.

`TransportRetryNone` disables non-timeout transport retries.
`TransportRetryAll` deliberately restores broad retry behavior for unusual
environments. `RetryTimeouts` remains a separate decision because a timed-out
operation may already have committed remotely. Every transport mode remains
subject to method policy, `RetrySafety`, body replayability, attempt limits, and
the operation context.

## TLS safety

HTTP uses ordinary `net/http` verification defaults. TCP TLS is opt-in and safe
by default once enabled: Clientkit verifies certificates, infers `ServerName`,
and requires TLS 1.2 or newer when it builds the TLS configuration.

An explicit `tls.Config` is a complete policy override. Review:

- `RootCAs` and client certificates.
- `ServerName` and name-inference settings.
- Minimum and maximum TLS versions.
- Verification and certificate callbacks.
- `InsecureSkipVerify`.

Clientkit clones the top-level configuration, but `tls.Config.Clone` retains
references to pools, callbacks, and some slices. Do not mutate referenced values
after constructing a client.

Do not use `InsecureSkipVerify` to work around a local certificate problem.
Install the intended trust root. If verification is deliberately replaced with
a callback, that callback becomes the complete application security boundary.

## Custom dialers and probes

A custom `DialContextFunc` replaces the built-in raw dialing behavior and is
responsible for context cooperation, concurrency safety, socket options, and
coherent connection/error results. Clientkit cannot forcibly interrupt arbitrary
code that ignores cancellation. Once the callback returns, a late result is
rejected and any returned connection is closed.

A `ConnectionProbe` runs against a Clientkit-owned health-check connection. It
must not retain or close that connection, must honor the context, and must return
quickly with bounded non-sensitive text. Clientkit contains probe panics and
closes the connection after the check.

Custom classifiers, sanitizers, propagators, and observers are also synchronous
trusted callbacks. They must be concurrency-safe and avoid blocking I/O.

## Health messages

The default sanitizer bounds and normalizes health values, but it cannot detect
secrets. Do not include:

- Remote error strings.
- Endpoints or resolved addresses.
- SQL or connection strings.
- Credentials or tokens.
- Certificate subjects or peer data.
- User, tenant, or resource identifiers.

Prefer short fixed messages such as `connection established`, `response
rejected`, or `check timed out`. `FailureClass` should carry the stable reason
category.

Disabling health sanitization transfers validation, cardinality, redaction, and
size ownership to the application.

## Telemetry opt-ins

Review these options as explicit disclosure decisions:

- `clientkit/otel.WithErrorDetails`.
- `slogobserver.WithErrorDetails`.
- `httpclient/otel.WithRequestTargetAttributes`.
- `httpclient/otel.WithStandardClientMetrics`.
- Application-supplied span, metric, and slog attributes.

Request-target attributes omit user information, query, and fragment, but paths
can still contain sensitive identifiers. Standard HTTP metrics introduce server
address and port dimensions. Raw errors can contain endpoint, certificate, and
application data. Clientkit never adds raw errors to metrics.

## Operational endpoints

Opskit and Servekit expose sanitized Clientkit state, but component names,
descriptions, and aggregate topology may still be operationally sensitive.
Keep readiness endpoints minimal and protect optional component-admin inspection
with authentication, authorization, network policy, and appropriate logging.

Do not add endpoint configuration to `ComponentInfo` attributes merely because
the inspection model permits safe attributes. Clientkit deliberately exposes
protocol identity rather than destinations.

## Production checklist

- Client and operation names come from a bounded static vocabulary.
- POST and custom-method retries have a real idempotency strategy.
- POST and custom-method 307/308 redirects have a real idempotency strategy.
- Request bodies used for retries are replayable.
- Every returned HTTP body and TCP connection is closed.
- Base URL path resolution and redirect policy have been reviewed.
- Cross-origin and host-override flags remain disabled unless required.
- TLS verification remains enabled with the intended server name and roots.
- Custom callbacks honor context and are concurrency-safe.
- Health messages contain no endpoint or error text.
- Telemetry opt-ins have completed a data/cardinality review.
- Active checks are scheduled outside readiness handlers.
- Opskit component-admin routes are protected.
- Shutdown drains active work before idle HTTP cleanup.

## Related material

- [Usage](usage.md)
- [Health and readiness](health-readiness.md)
- [Observability](observability.md)
- [Composition](composition.md)
