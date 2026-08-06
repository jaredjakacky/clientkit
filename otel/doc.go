// Package otel adapts Clientkit observer events to OpenTelemetry traces and
// metrics.
//
// The package does not initialize an OpenTelemetry SDK or exporter. New uses
// the global OpenTelemetry providers by default, and applications may supply
// explicit providers with options. The application owns provider shutdown.
//
// This adapter does not inject trace context into outbound requests;
// propagation is configured separately. Its default metric attributes are
// intentionally stable and low-cardinality. Raw operation errors are not
// recorded by default because they may contain sensitive endpoint or
// application data; WithErrorDetails opts into exception events explicitly.
//
// Typical wiring looks like:
//
//	telemetry, err := clientkitotel.New()
//	if err != nil {
//		// handle error
//	}
//
//	client, err := httpclient.New(httpclient.Config{
//		Config: clientkit.Config{
//			Name:     "payments",
//			Observer: telemetry,
//		},
//		BaseURL: "https://payments.internal",
//	})
//
// A non-nil configured observer replaces the protocol client's automatic
// OpenTelemetry observer. Use clientkit.MultiObserver when observation should
// be additive instead.
package otel
