module github.com/jaredjakacky/clientkit/examples/kit-series-composition

// renovate: datasource=custom.go-supported-floor depName=go-floor versioning=semver-coerced
go 1.25.0

require (
	github.com/jaredjakacky/clientkit v0.0.0
	github.com/jaredjakacky/opskit v0.6.0
	github.com/jaredjakacky/servekit v0.8.0
	github.com/jaredjakacky/workerkit v0.8.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
)

// Exercise the current checkout while keeping Servekit and Workerkit out of
// Clientkit's published root module graph.
replace github.com/jaredjakacky/clientkit => ../..
