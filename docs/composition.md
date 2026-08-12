# Kit Series composition

Clientkit can be used alone. When a service uses other Kit Series packages,
Opskit is the contract boundary that keeps ownership clear.

## Responsibilities

| Package | Responsibility |
| --- | --- |
| Clientkit | Outbound execution policy, propagation, telemetry, cached client health, and client readiness |
| Opskit | Neutral status, readiness, inspection, check, and command composition |
| Workerkit | Lifecycle and scheduling for periodic check execution |
| Servekit | HTTP service lifecycle and passive operational presentation |
| Dependkit | Generic external-dependency health not already owned by a domain kit |
| Application root | Configuration, credentials, telemetry SDK, wiring, startup, drain, and shutdown |

Clientkit does not import Servekit, Workerkit, Configkit, or Dependkit in its root
or domain packages. An application imports the kits it chooses and composes them
at its root.

## Standalone Clientkit

Applications that do not need operational aggregation can use an HTTP or TCP
client directly:

```go
payments, err := httpclient.New(httpclient.Config{
	Config:  clientkit.Config{Name: "payments"},
	BaseURL: "https://payments.example/",
})
```

The client still provides execution policy, results, observability, and optional
cached health. Opskit is present only as a foundational contract dependency in
the root module.

## Build the Clientkit Registry

Construct protocol clients, then register them statically:

```go
clients := clientkit.NewRegistry()
clients.MustRegisterAll(payments, events)
```

Registration captures stable identity and readiness metadata. The registry
implements:

- `opskit.Component`.
- `opskit.ReadinessContributor`.
- `opskit.Inspector`.
- `opskit.CheckGroup`.

It is both the passive operational view and the active group-check entry point.

## Register with Opskit

```go
ops := opskit.NewRegistry()
ops.MustRegister(clients, opskit.Required())
```

There are two policy layers:

1. Each Clientkit client has `ReadinessRequired`, `ReadinessDegradedAllowed`,
   `ReadinessOptional`, or `ReadinessInformational`.
2. Opskit decides whether the Clientkit Registry component blocks the overall
   application readiness.

Register the Registry with `opskit.Required()` when the per-client decisions
should gate the service. Use an optional or informational parent policy only
when the application intentionally wants Clientkit state to be nonblocking at
the application level.

## Schedule checks with Workerkit

Clientkit does not start background goroutines. Workerkit can own periodic
execution:

```go
workers, err := workerkit.New(workerkit.Identity{Name: "workers"})
if err != nil {
	return err
}

err = workers.Register(workerkit.WorkerSpec{
	Name:        "client-checks",
	Description: "Refreshes cached outbound-client health.",
	Worker: workerkit.NewCheckGroupLoop(
		clients,
		workerkit.WithCheckInterval(30*time.Second),
		workerkit.WithCheckTimeout(5*time.Second),
	),
}, workerkit.WithWorkerReadinessContribution(false))
```

The Workerkit loop owns scheduling, cancellation, panic recovery, and loop
failure reporting. Clientkit continues to own the actual checks and cached
health.

Disabling the worker's readiness contribution is appropriate when the same
Clientkit Registry is already required through Opskit. Otherwise one failed
check can create both a worker-readiness failure and a Clientkit-readiness
failure for the same state.

## Present state with Servekit

Servekit can use the same Opskit Registry:

```go
server := servekit.New(
	servekit.WithOps(ops),
)
```

Servekit's operational routes read Clientkit's passive cached projections. They
do not trigger outbound checks. Optional Opskit component-admin routes require
an explicit `servekit.WithOpsAdmin()` and should use an authorization gate
before exposure beyond a trusted network.

## Clientkit and Dependkit

Use Clientkit health when the dependency is accessed through a Clientkit HTTP or
TCP client and Clientkit already owns its execution and failure semantics.

Use Dependkit when the dependency is not a Clientkit-owned outbound client, for
example:

- A database driver or pool.
- A message broker SDK.
- A filesystem or cloud control-plane dependency.
- An application-specific check without Clientkit execution semantics.

The packages can coexist in one Opskit Registry. Do not register the same remote
dependency in both simply to obtain another health surface; duplicate checks can
create load, disagreement, and two readiness gates.

## Recommended startup order

1. Load and validate application configuration.
2. Configure OpenTelemetry providers, exporters, resources, and propagation.
3. Construct Clientkit observers and protocol clients.
4. Register clients in a Clientkit Registry.
5. Register Clientkit and other components with Opskit.
6. Construct Workerkit check loops where periodic refresh is needed.
7. Construct Servekit with the Opskit Registry.
8. Start managed runtimes and begin serving.

Configuring telemetry before Clientkit construction matters because default
adapters capture applicable global providers and propagation at construction.

## Runtime data flow

```text
application request
    └── Clientkit HTTP/TCP execution
            ├── caller-visible result
            └── spans, metrics, or logs

Workerkit timer
    └── Clientkit Registry.CheckAll
            └── sanitized cached health
                    └── Opskit
                            └── Servekit status/readiness/inspection
```

Readiness presentation remains passive even while Workerkit is refreshing the
same cached state concurrently.

## Recommended shutdown order

1. Stop admitting new application work.
2. Stop Workerkit scheduling and other background producers.
3. Drain active HTTP operations and application-owned TCP work.
4. Call `Clientkit.Registry.CloseIdleConnections`.
5. Shut down servers and remaining resources according to application policy.
6. Flush and shut down telemetry providers.

`CloseIdleConnections` does not cancel active work or close successful
caller-owned TCP connections. If an HTTP transport is shared, idle cleanup may
affect its other users.

## Dependency boundary

The architecture should remain directed:

```text
application
├── clientkit/httpclient or clientkit/tcpclient
├── workerkit
├── servekit
├── dependkit (when needed)
└── opskit

clientkit root ──► opskit
```

Do not move application composition into Clientkit or add cross-kit imports to
its domain packages. Opskit is the stable shared contract.

## Related material

- [Runnable Kit Series composition example](../examples/kit-series-composition)
- [Health and readiness](health-readiness.md)
- [Observability](observability.md)
- [Operational safety](operational-safety.md)
- [Opskit](https://github.com/jaredjakacky/opskit)
- [Workerkit](https://github.com/jaredjakacky/workerkit)
- [Servekit](https://github.com/jaredjakacky/servekit)
- [Dependkit](https://github.com/jaredjakacky/dependkit)
