# Health and readiness

Clientkit separates active dependency checks from passive operational reads.
That boundary keeps readiness endpoints fast, bounded, and independent of a
dependency that may already be failing.

```text
active Check or Registry.CheckAll
              │
              ▼
       sanitized cached Health
      + exceptional Registry result
              │
        ┌─────┴──────────────┐
        ▼                    ▼
Snapshot / Status     Readiness / Inspect
        passive operational projections
```

## Active and passive operations

The following operations can perform dependency I/O:

- `httpclient.Client.Check`.
- `tcpclient.Client.Check`.
- `clientkit.Registry.CheckAll`, which invokes enabled registered checks.

The following operations are passive:

- Protocol-client `Health` and `Snapshot`.
- Registry `Snapshot`, `Status`, `Readiness`, and `Inspect`.
- Opskit and Servekit reads of those contracts.

Passive operations never refresh health synchronously. Calling `/readyz` must
not turn a dependency outage into an outbound request storm.

## Enabling a check

HTTP checks use a separate path, classifier, timeout, staleness, and retry
policy:

```go
payments, err := httpclient.New(httpclient.Config{
	Config: clientkit.Config{
		Name:            "payments",
		ReadinessPolicy: clientkit.ReadinessRequired,
	},
	BaseURL: "https://payments.example/",
	Check:   httpclient.DefaultCheckConfig("healthz"),
})
```

`DefaultCheckConfig` enables a GET check, accepts exactly HTTP 200, and performs
one attempt. Ordinary operations accept the complete 2xx status class by
default; health-check response policy is deliberately independent.

TCP checks connect and close:

```go
events, err := tcpclient.New(tcpclient.Config{
	Config: clientkit.Config{
		Name:            "events",
		ReadinessPolicy: clientkit.ReadinessDegradedAllowed,
	},
	Address: "events.example:443",
	TLS:     tcpclient.TLSConfig{Enabled: true},
	Check:   tcpclient.CheckConfig{Enabled: true},
})
```

An optional `ConnectionProbe` can perform a bounded application-level exchange
after connection establishment. It must honor context cancellation, be safe for
concurrent use, avoid retaining or closing the connection, and return bounded
non-sensitive assessment text. Clientkit always closes the check connection.

Built-in HTTP and TCP clients reject readiness-blocking policies when their
health check is disabled. This prevents a required client from remaining
permanently unknown without an active refresh strategy.

## Cached health

Before the first successful check completion, cached health is unknown. Every
active check records:

- A bounded health state.
- A stable failure class.
- UTC completion time.
- Complete check duration.
- Bounded operational text.

The default sanitizer removes control and Unicode formatting characters,
normalizes invalid values, and limits messages to 256 bytes. It cannot recognize
secrets. Custom checks and probes must never put credentials, endpoints, IDs, or
arbitrary remote data in health messages.

`StaleAfter` is evaluated when health is read. Staleness is a projection; it
does not mutate the cached assessment. Choose a value greater than the maximum
expected completion-to-completion refresh gap, including:

- Scheduler interval.
- Check-group queueing and execution.
- Positive scheduling jitter.
- Check timeout.
- Expected scheduler delay.

Disabling staleness is explicit and means an old assessment may continue to be
reported indefinitely.

Registered clients own their normal cached health. If `Registry.CheckAll`
creates a client-specific failure that the client could not persist—for example,
because the checker panicked, the outer context rejected a returned result, or
the Registry sanitizer panicked—the Registry retains only that exceptional
projection. `Snapshot`, `Status`, `Readiness`, and `Inspect` use it instead of an
older client assessment. A later accepted Registry check clears the projection;
a later direct client check supersedes it through its newer `CheckedAt` value.
The exceptional projection has no independent staleness timer: without a later
assessment, the Registry still lacks trustworthy health. The Registry does not
write into the client cache or duplicate ordinary health.

## The Registry

Register clients during deterministic startup:

```go
clients := clientkit.NewRegistry()
clients.MustRegister(payments)
clients.MustRegister(events)
```

Registration captures name, protocol, readiness policy, and health-check
enablement once. Membership is static: there is no replacement or unregister
operation. `RegisterAll` is atomic and `MustRegisterAll` is convenient at the
application composition root.

The zero-value Registry is usable. Its default bound allows four simultaneous
checks. `CheckAll`:

- Returns results in client-name order.
- Enforces its concurrency bound across overlapping calls.
- Serializes checks for the same client.
- Contains checker and sanitizer panics.
- Rejects results that return after the context has definitively ended.
- Keeps client-specific failures it synthesizes visible to passive projections
  until a later client assessment supersedes them.

Check implementations must still cooperate with context cancellation. Clientkit
does not create hidden goroutines to forcibly interrupt arbitrary callbacks.

## Readiness policies

| Policy | Appears in readiness | Blocks aggregate readiness when |
| --- | --- | --- |
| `ReadinessRequired` | Yes | State is not healthy |
| `ReadinessDegradedAllowed` | Yes | State is unknown or unhealthy |
| `ReadinessOptional` | Yes | Never |
| `ReadinessInformational` | No | Never |

Optional clients remain visible as readiness items, even when their state does
not block the aggregate decision. Informational clients remain visible through
status, checks, snapshots, and inspection but are omitted from readiness.

## Opskit policy is a separate layer

Clientkit's readiness policy belongs to each outbound client. Opskit separately
decides whether the Clientkit Registry component blocks the application's
aggregate readiness:

```go
ops := opskit.NewRegistry()
ops.MustRegister(clients, opskit.Required())
```

Use `opskit.Required()` when Clientkit's per-client decisions should affect the
service readiness result. Registering the Registry as optional would preserve
its detailed readiness but prevent it from blocking the Opskit aggregate.

## Scheduling with Workerkit

Clientkit starts no scheduler. Workerkit can periodically execute the Registry's
Opskit `CheckGroup`:

```go
runtime, err := workerkit.New(workerkit.Identity{Name: "workers"})
if err != nil {
	return err
}

err = runtime.Register(workerkit.WorkerSpec{
	Name:        "client-checks",
	Description: "Refreshes cached outbound-client health.",
	Worker: workerkit.NewCheckGroupLoop(
		clients,
		workerkit.WithCheckInterval(30*time.Second),
		workerkit.WithCheckTimeout(5*time.Second),
	),
}, workerkit.WithWorkerReadinessContribution(false))
```

The worker's readiness contribution is disabled in this example because the
Clientkit Registry is already registered with Opskit as required. That avoids
two readiness gates for the same dependency state. The Workerkit loop still
reports its own operational status and failures.

Ensure each protocol client's `StaleAfter` exceeds the realistic Workerkit
refresh gap. The Workerkit timeout is an outer cooperative bound; it does not
replace protocol-specific check timeouts.

## Presenting state with Servekit

Servekit can mount an Opskit Registry:

```go
server := servekit.New(
	servekit.WithOps(ops),
)
```

Servekit reads cached Clientkit status and readiness. It does not invoke
`CheckAll` from readiness endpoints. Opskit component-admin routes are optional
and should be protected before exposure outside a trusted administrative
boundary.

## Shutdown

Stop the scheduler first, stop admitting new application work, and wait for
active requests to drain. Then ask capable clients to release idle HTTP
connections:

```go
clients.CloseIdleConnections()
```

This does not cancel active requests, close caller-owned TCP connections, or
make clients unusable. Future HTTP operations can establish new connections.

## Related material

- [Usage](usage.md)
- [Composition](composition.md)
- [Operational safety](operational-safety.md)
- [Opskit](https://github.com/jaredjakacky/opskit)
- [Workerkit](https://github.com/jaredjakacky/workerkit)
- [Servekit](https://github.com/jaredjakacky/servekit)
