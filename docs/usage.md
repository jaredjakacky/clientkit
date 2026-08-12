# Usage

Clientkit clients are immutable after construction and safe for concurrent use
according to their documented contracts. Construct them once in the application
root, pass them to the code that needs them, and keep endpoint and credential
configuration outside operational metadata.

## HTTP clients

### Construction

```go
payments, err := httpclient.New(httpclient.Config{
	Config: clientkit.Config{
		Name: "payments",
	},
	BaseURL: "https://payments.example/api/",
})
```

Construction performs validation only. `BaseURL` must use HTTP or HTTPS, include
a host, and contain no user information, query, or fragment. Its optional path
is normalized as a directory base.

Nil policy interfaces select documented defaults. Client-level timeout zero
values select production defaults. The zero HTTP retry configuration selects
`DefaultRetryConfig`; a custom non-zero configuration is a complete replacement,
not a field-by-field merge.

### Request construction and origin binding

`NewRequest` accepts a relative RFC 3986 reference:

```go
request, err := payments.NewRequest(ctx, http.MethodGet, "invoices/42", nil)
```

Normal URL resolution applies. Given a base ending in `/api/`:

- `invoices/42` resolves below `/api/`.
- `/status` replaces the base path.
- `../status` can leave `/api/`.

The base path is not a security confinement boundary. Validate untrusted path
components before constructing the reference; do not concatenate arbitrary
input and assume the prefix will contain it.

Clientkit rejects absolute references and fragments in `NewRequest`. At
execution it rejects requests and redirects whose scheme, host, or effective
port differs from `BaseURL`. It also rejects a `Request.Host` override. These
checks can be disabled explicitly, but doing so may forward application headers
to another origin or permit an HTTPS downgrade.

### Choosing an execution method

`Do` is the smallest integration surface:

```go
response, err := payments.Do(request)
if err != nil {
	return err
}
defer response.Body.Close()
```

It returns standard `net/http` response/error semantics. A rejected status is
represented by a non-nil response and nil error.

`Execute` returns Clientkit's structured result:

```go
result := payments.Execute(request)
if result.Response != nil {
	defer result.Response.Body.Close()
}

switch result.Outcome {
case httpclient.OutcomeSuccess:
	// Use result.Response.
case httpclient.OutcomeResponseRejected:
	// Apply application policy to result.StatusCode.
default:
	return result.Err
}
```

`Outcome` describes the logical operation. `FailureClass` is a bounded safe
classification for policy and telemetry. `Err` remains the original Go error
for setup, request, and transport failures. Response rejection and classifier
policy failure do not synthesize an error.

Use `ExecuteWithOptions` for an immutable per-call override:

```go
result := payments.ExecuteWithOptions(request, httpclient.ExecuteOptions{
	Operation:   "payments.create",
	RetrySafety: httpclient.RetrySafetyIdempotent,
})
```

Operation names must come from a fixed low-cardinality vocabulary. Never derive
them from a URL, path, resource ID, user, tenant, or request identifier.

### The three retry gates

An attempt is repeated only when:

1. The selected `RetryConfig` permits the method and result.
2. `RetrySafety` authorizes repeating the complete operation.
3. The request has no body or has a working `GetBody` function.

The default policy retries selected transport failures, attempt timeouts, and
408, 429, 500, 502, 503, and 504 responses. It is limited to GET, HEAD, OPTIONS,
PUT, and DELETE. The maximum of three attempts includes the first request.

POST, PATCH, CONNECT, and custom methods do not pass the default semantic-safety
gate. To retry a POST, both the policy and operation must opt in:

```go
retry := httpclient.DefaultRetryConfig()
retry.Methods = append(retry.Methods, http.MethodPost)

client, err := httpclient.New(httpclient.Config{
	Config:  clientkit.Config{Name: "payments"},
	BaseURL: "https://payments.example/",
	Retry:   retry,
})

result := client.ExecuteWithOptions(request, httpclient.ExecuteOptions{
	RetrySafety: httpclient.RetrySafetyIdempotent,
})
```

That assertion is meaningful only when the remote operation is genuinely
idempotent or application-level deduplication is in place. Clientkit neither
creates nor validates an idempotency key. A timed-out request may already have
committed remotely.

Use `RetrySafetyNever` or `ExecutionRetry{Disable: true}` when an operation must
make only one Clientkit attempt. Redirects may still cause more than one
transport `RoundTrip`.

### Body replay and ownership

A nil body and `http.NoBody` are replayable. A non-empty body requires
`Request.GetBody`. Standard request constructors populate it for common
`bytes.Buffer`, `bytes.Reader`, and `strings.Reader` values.

Retry bodies are created lazily only when another attempt starts. A
non-replayable request can execute once but cannot retry.

`ExecuteWithOptions` takes ownership of a non-nil `Request.Body` and closes it,
including when validation fails before the first attempt. After a request is
passed for execution, do not concurrently read, replace, reuse, or close its
body.

The caller owns the final `Response.Body`. Read it to EOF or close it promptly.
Closing enables timeout-context cleanup and allows the transport to reuse the
connection where ordinary `net/http` rules permit.

### Timeout layers

The following limits coexist:

- The caller's parent context.
- Clientkit's total operation timeout.
- Clientkit's per-attempt timeout.
- A caller-supplied `http.Client.Timeout`.
- Transport dial, TLS, and response-header timeouts.

The earliest applicable cancellation or deadline wins. The total timeout spans
all attempts, retry waits, `Retry-After`, and final body use. A fresh attempt
timeout starts for each Clientkit attempt and includes redirects and final body
use for that attempt.

Per-call `ExecutionTimeouts` fields inherit client policy when zero. Positive
durations replace one layer; disable flags remove that Clientkit layer without
detaching the parent context or caller-owned HTTP client.

Result duration and spans stop when final headers or a terminal error arrive.
They deliberately do not measure application body consumption. If all timeout
layers are disabled and the caller abandons a response body, no Clientkit
deadline can guarantee cleanup.

### Caller-supplied HTTP clients

A non-nil `Config.HTTPClient` replaces Clientkit's owned default. Clientkit does
not mutate or claim it. Its value is copied so origin and redirect policy can be
composed, while its transport and timeout behavior remain in force.

Physical HTTP tracing is not automatically installed on a caller-owned client.
Wrap its transport explicitly with `httpclient/otel.NewTransport` when desired.

`Client.CloseIdleConnections` invokes the configured client's cleanup directly.
If the HTTP client or transport is shared, this can affect other users of the
same idle pool. Clientkit never calls it automatically.

## TCP clients

### Establishing a connection

```go
events, err := tcpclient.New(tcpclient.Config{
	Config:  clientkit.Config{Name: "events"},
	Network: "tcp",
	Address: "events.example:443",
	TLS:     tcpclient.TLSConfig{Enabled: true},
})

conn, err := events.Dial(ctx)
if err != nil {
	return err
}
defer conn.Close()
```

`Dial` returns ordinary Go connection/error semantics. `DialResult` additionally
reports bounded outcome, failure class, start time, and duration. A non-nil
successful connection is always caller-owned.

`DialContext` supports integrations that require the common dialer method shape,
but remains endpoint-bound. Its network and address arguments must match the
client's normalized immutable configuration.

### Address and network policy

The built-in dialer accepts `tcp`, `tcp4`, or `tcp6` and requires a host plus
numeric port in `net.SplitHostPort` form. IPv6 addresses therefore use brackets,
for example `[2001:db8::1]:443`.

A custom `DialContextFunc` receives the normalized network and trimmed address.
It replaces only the raw dial stage; Clientkit still applies its dial timeout to
the accepted result and performs configured TLS afterward.

Custom dialers must be safe for concurrent use, honor context cancellation,
return a coherent connection/error pair, and own their socket options. Clientkit
does not start a goroutine to interrupt a callback that ignores its context. If
the callback eventually returns after cancellation, Clientkit rejects the late
result and closes any returned connection.

### TLS policy

TCP is plaintext unless `TLS.Enabled` is true. Without a custom `tls.Config`,
Clientkit:

- Verifies the peer certificate.
- Infers `ServerName` from the configured address.
- Uses TLS 1.2 as the minimum version.
- Bounds the accepted handshake with a ten-second timeout.

An explicit `TLS.Config` is a complete override and is cloned during client
construction. As with `tls.Config.Clone`, referenced certificate pools,
callbacks, and slices must not be mutated afterward. An explicit `ServerName`
must not conflict with `TLS.Config.ServerName`.

`InsecureSkipVerify` is honored only as a deliberate caller override. It
disables standard verification and should not be used as a convenience for
development certificates; supply a trusted root instead.

Handshake errors and late results close the underlying connection. Clientkit
never returns a usable connection alongside a failure.

### Raw connection scope

TCP Clientkit establishes a raw connection. The application owns:

- Framing and codecs.
- Authentication handshakes above TLS.
- Request/response correlation.
- Multiplexing.
- Pooling and reconnect policy.
- All successful connection cleanup.

## Health checks

HTTP and TCP check configurations are independent from ordinary execution
policy and disabled by default. Checks cache sanitized health. Passive reads do
not contact the dependency. See [Health and readiness](health-readiness.md).

## Related guides

- [Getting started](getting-started.md)
- [Operational safety](operational-safety.md)
- [Observability](observability.md)
- [API map](api.md)
