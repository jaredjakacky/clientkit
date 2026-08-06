// Package clientkit defines the protocol-neutral contracts shared by
// production-oriented outbound clients.
//
// The package provides stable client identity, readiness policy, cached health,
// failure classification, backend-neutral observation, and registry
// integration. Protocol packages such as httpclient and tcpclient build on
// these contracts to perform network operations.
//
// Registry status and readiness evaluation are passive: they read cached health
// and never contact dependencies. Registry.CheckAll is the explicit boundary
// for active health checks.
//
// A nil Observer passed directly to New becomes a no-op observer. Protocol
// constructors may install their documented default observer before creating
// the shared Client. Applications can replace or compose observers without
// changing protocol execution.
package clientkit
