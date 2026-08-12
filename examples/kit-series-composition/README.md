# Kit Series composition

This bounded local program proves Clientkit's intended Kit Series architecture:

- Clientkit owns outbound HTTP execution, cached client health, and per-client
  readiness.
- Clientkit's Registry supplies Opskit component, readiness, inspection, and
  check-group contracts.
- Workerkit periodically executes the Registry's check group.
- A shared Opskit Registry composes Clientkit and Workerkit state.
- Servekit presents cached state through `/readyz` and protected read-only
  component inspection.

The program verifies that neither `/readyz` nor component inspection performs a
dependency check. Only the Workerkit loop contacts the local dependency.

Dependkit is intentionally absent because the health belongs to a Clientkit
outbound client. No pairwise Clientkit/Workerkit or Clientkit/Servekit adapter is
needed; Opskit is the shared contract.

## Run

From the Clientkit repository root:

```sh
go -C examples/kit-series-composition run .
```

Expected output has this shape:

```text
before readyz_status=503 health_checks=0
after readyz_status=200 health_checks=1
inspection unauthorized_status=401 authorized_status=200 health_checks=1
```

The example uses Servekit's in-process `Handler` and local `httptest` servers so
it exits deterministically without a real port, signal handling, credentials,
or external infrastructure. The hard-coded token is only a concise development
authorization gate.

## Why this is a nested module

Servekit and Workerkit are application-composition dependencies, not Clientkit
library dependencies. This directory has its own `go.mod` so those modules do
not enter Clientkit's published root module graph. The replacement points only
Clientkit at the current checkout; sibling kits use released module versions.
