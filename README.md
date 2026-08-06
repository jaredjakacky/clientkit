# clientkit

Clientkit provides transport-neutral identity, cached health, registry snapshots, and readiness integration for outbound clients.

Transport implementations live in separate packages. The `httpclient` package contains the HTTP client configuration and request-oriented types, including checks, retries, attempts, results, and outcomes.
