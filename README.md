# clientkit

Clientkit provides transport-neutral identity, bounded client protocol categories, cached health, registry snapshots, and readiness integration for outbound clients.

Transport implementations live in separate packages. The `httpclient` package contains the HTTP client configuration and request-oriented types, including checks, retries, attempts, results, and outcomes.

Registry inspection exposes logical names, stable protocol categories, readiness policies, and sanitized cached health. It does not expose URLs, hosts, addresses, connection strings, credentials, or other network configuration.

The default HTTP observability model separates one logical Clientkit operation from its physical sends: the logical operation is an OpenTelemetry INTERNAL span, and every transport RoundTrip is a CLIENT span that owns outbound trace propagation. Clientkit-specific metrics remain low-cardinality; standard HTTP duration metrics and request-target span attributes are explicit opt-ins.
