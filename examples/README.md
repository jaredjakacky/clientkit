# Clientkit examples

These four programs are the minimum runnable path through Clientkit. They use
local servers and listeners, make no external requests, and require no
credentials or infrastructure.

## Read order

1. [http-basic](http-basic)
2. [http-retry-and-classification](http-retry-and-classification)
3. [tcp-tls](tcp-tls)
4. [kit-series-composition](kit-series-composition)

## What each example shows

- [http-basic](http-basic) constructs an HTTP client with ordinary production
  defaults, creates a relative request, uses `Do`, and closes the caller-owned
  response body.
- [http-retry-and-classification](http-retry-and-classification) combines a
  stable operation name, response classification, body replay, retry policy,
  semantic retry authorization, and structured results. It proves that adding
  POST to a policy does not authorize an unsafe retry by itself.
- [tcp-tls](tcp-tls) trusts a local certificate, establishes a verified TLS
  connection, performs an application-owned exchange, and closes the returned
  `net.Conn`.
- [kit-series-composition](kit-series-composition) composes Clientkit, Opskit,
  Workerkit, and Servekit. Workerkit refreshes Clientkit health while Servekit
  reads the cached readiness state without performing checks from `/readyz`.

## Run

The first three programs belong to Clientkit's root module:

```sh
go run ./examples/http-basic
go run ./examples/http-retry-and-classification
go run ./examples/tcp-tls
```

The cross-kit example is an isolated module so Servekit and Workerkit do not
enter Clientkit's published module graph:

```sh
go -C examples/kit-series-composition run .
```

Build and test the complete set with:

```sh
make build-examples
make verify
```

See the [examples guide](../docs/examples.md) for the learning and safety
boundary of each program.
