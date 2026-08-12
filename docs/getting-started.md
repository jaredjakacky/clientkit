# Getting started

Clientkit adds production policy and operational state to ordinary outbound Go
clients. Construction validates immutable configuration but does not perform
network I/O or start background goroutines.

## Install Clientkit

```sh
go get github.com/jaredjakacky/clientkit@latest
```

The examples below use only Clientkit and the standard library. Applications
configure endpoint values and credentials through their own configuration
system.

## Make an HTTP request

```go
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func main() {
	payments, err := httpclient.New(httpclient.Config{
		Config:  clientkit.Config{Name: "payments"},
		BaseURL: "https://payments.example/api/",
	})
	if err != nil {
		log.Fatal(err)
	}

	request, err := payments.NewRequest(
		context.Background(),
		http.MethodGet,
		"status",
		nil,
	)
	if err != nil {
		log.Fatal(err)
	}

	response, err := payments.Do(request)
	if err != nil {
		log.Fatal(err)
	}
	defer response.Body.Close()

	_, _ = io.Copy(io.Discard, response.Body)
	fmt.Println(response.Status)
}
```

The client is named `payments` for logs, traces, metrics, registry inspection,
and readiness. Names are stable identifiers declared in source or trusted
configuration; do not derive them from URLs, users, tenants, or request IDs.

`Do` follows ordinary `net/http` response/error conventions. A server response
rejected by the configured classifier is still a response, not a transport
error. The caller owns and must close every returned response body.

Use `Execute` when the caller needs structured outcome information:

```go
result := payments.Execute(request)
if result.Response != nil {
	defer result.Response.Body.Close()
}

fmt.Printf("outcome=%s failure_class=%s attempts=%d\n",
	result.Outcome,
	result.FailureClass,
	len(result.Attempts),
)
```

Create a fresh request for each execution. `ExecuteWithOptions` takes ownership
of a non-nil request body and closes it.

## Establish a TCP/TLS connection

```go
package main

import (
	"context"
	"log"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/tcpclient"
)

func main() {
	events, err := tcpclient.New(tcpclient.Config{
		Config:  clientkit.Config{Name: "events"},
		Address: "events.example:443",
		TLS:     tcpclient.TLSConfig{Enabled: true},
	})
	if err != nil {
		log.Fatal(err)
	}

	conn, err := events.Dial(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// Use conn with the application's framing or protocol implementation.
}
```

TCP Clientkit establishes one raw connection. It does not pool connections,
frame messages, select a codec, or retain a successful connection. The caller
owns the returned `net.Conn`.

TLS is opt-in. With TLS enabled and no custom policy, Clientkit verifies the
certificate, infers the server name from the configured address, and requires
TLS 1.2 or newer.

## Defaults after construction

- Readiness is optional.
- Cached health is unknown.
- Active health checks are disabled.
- HTTP uses bounded total and attempt timeouts and a safe retry policy.
- HTTP requests cannot leave their configured origin by default.
- TCP uses a five-second dial timeout and is plaintext unless TLS is enabled.
- A nil observer lets built-in clients install their default OpenTelemetry
  adapter. Without an application SDK, the OpenTelemetry API remains a no-op.

Read [Usage](usage.md) before changing retries, timeout layers, caller-owned
HTTP clients, custom dialers, or TLS. Read [Health and readiness](health-readiness.md)
before making a client block service readiness.

## Run the repository examples

```sh
go run ./examples/http-basic
go run ./examples/http-retry-and-classification
go run ./examples/tcp-tls
go -C examples/kit-series-composition run .
```

The examples use local servers and listeners. They need no credentials or
external infrastructure.

## Next steps

- [Usage](usage.md) explains execution and ownership.
- [Operational safety](operational-safety.md) covers trust boundaries.
- [Composition](composition.md) connects Clientkit to Opskit, Workerkit, and
  Servekit.
- [API map](api.md) points to the canonical Go documentation.
