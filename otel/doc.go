// Package otel adapts Clientkit observer events to OpenTelemetry traces and
// metrics.
//
// The package does not initialize an OpenTelemetry SDK or exporter. New uses
// the global OpenTelemetry providers by default, and applications may supply
// explicit providers with options. The application owns provider shutdown.
//
// This adapter does not inject trace context or create protocol-specific wire
// spans. It maps logical operations to INTERNAL spans and direct remote
// operations, such as TCP dialing, to CLIENT spans. HTTP RoundTrip spans and
// propagation are configured separately through httpclient/otel.
//
// Span-wide and metric-wide attributes are configured independently. Default
// metric attributes are intentionally stable and low-cardinality. Raw operation
// errors are not recorded by default because they may contain sensitive endpoint
// or application data; WithErrorDetails opts into exception events explicitly.
//
// Explicit HTTP wiring with a custom observer looks like:
//
//	telemetry, err := clientkitotel.New()
//	if err != nil {
//		// handle error
//	}
//
//	attemptTransport, err := httpclientotel.NewTransport(
//		httpclient.DefaultTransport(),
//	)
//	if err != nil {
//		// handle error
//	}
//
//	client, err := httpclient.New(httpclient.Config{
//		Config: clientkit.Config{
//			Name:     "payments",
//			Observer: telemetry,
//		},
//		BaseURL:   "https://payments.internal",
//		HTTPClient: &http.Client{Transport: attemptTransport},
//	})
//
// A non-nil configured observer replaces the protocol client's automatic
// OpenTelemetry observer. Use clientkit.MultiObserver when logical observation
// should be additive. A nil HTTP Observer and the owned default HTTP client
// install both adapters automatically.
package otel
