package clientkit

import "context"

// RegisteredClient exposes the passive operational state required by Registry.
//
// RegisteredClient should normally be implemented by a pointer or value type.
// Registry registration rejects nil interfaces and typed-nil pointers. The
// registry captures Name, Protocol, and ReadinessPolicy once during
// registration, and implementations must keep that metadata stable. After
// registration, implementations must remain safe for concurrent operational
// use.
type RegisteredClient interface {
	// Name returns a stable name accepted by ValidateClientName.
	Name() string
	// Protocol returns a stable, low-cardinality client-family category accepted
	// by ValidateClientProtocol. It must not contain an endpoint or other
	// sensitive configuration.
	Protocol() string
	// ReadinessPolicy returns stable policy metadata.
	ReadinessPolicy() ReadinessPolicy
	// Health returns cached health without performing dependency I/O.
	Health() Health
}

// HealthChecker actively refreshes client health. Implementations must be safe
// for concurrent calls and honor context cancellation cooperatively. For an
// enabled check, Check must make its returned assessment subsequently visible
// through Health before it returns. Registry may retain a newer synthetic
// failure for its own passive projections when Check panics or Registry rejects
// a result; it does not mutate the client-owned cache.
type HealthChecker interface {
	RegisteredClient
	// Check actively assesses the dependency and updates cached health.
	Check(context.Context) Health
}

// HealthCheckConfigurable optionally reports whether a registered client's
// active health check is enabled. Registry captures this immutable configuration
// once during registration. The method must return quickly without performing
// I/O. HealthChecker implementations that do not implement this interface are
// treated as enabled.
type HealthCheckConfigurable interface {
	// HealthCheckEnabled reports whether Check is configured for active use.
	HealthCheckEnabled() bool
}

// IdleConnectionCloser releases currently idle reusable connections without
// canceling or waiting for active operations. It does not permanently close a
// client: implementations may remain usable, and future operations may create
// new connections. Calls are explicit, synchronous, and may be made
// concurrently.
//
// Applications using this capability during shutdown remain responsible for
// stopping new work and draining active work first. The capability is optional
// and pool-oriented; tcpclient does not implement it because successful raw TCP
// connections are caller-owned rather than tracked by Clientkit.
type IdleConnectionCloser interface {
	// CloseIdleConnections releases idle resources without closing the client.
	CloseIdleConnections()
}

// ClientSnapshot is one immutable registry-facing view of a client's identity,
// readiness policy, and effective cached health.
type ClientSnapshot struct {
	// Name is the client's stable registered identity.
	Name string `json:"name"`
	// Protocol is the client-family category captured during registration.
	Protocol string `json:"protocol"`
	// ReadinessPolicy is the policy captured during registration.
	ReadinessPolicy ReadinessPolicy `json:"readiness_policy"`
	// Health is the producer's effective health at snapshot time. Registry
	// snapshots may project a newer Registry-synthesized check failure over the
	// client-owned cache.
	Health Health `json:"health"`
}

// RegistrySnapshot is a deterministic point-in-time view of registered clients.
type RegistrySnapshot struct {
	// Clients is sorted by client name.
	Clients []ClientSnapshot `json:"clients"`
}
