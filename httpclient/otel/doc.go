// Package otel provides Clientkit's OpenTelemetry HTTP propagation and
// per-RoundTrip transport instrumentation.
//
// New constructs only a HeaderPropagator. NewTransport wraps an
// http.RoundTripper and creates one CLIENT span for each physical RoundTrip,
// including redirects and Clientkit retries. It injects trace context from that
// attempt span and ends the span when response headers or a transport error are
// available; response-body reads and closes do not alter the span.
//
// By default the transport omits server address, port, URL, and standard HTTP
// metrics. WithRequestTargetAttributes explicitly adds server identity and a
// url.full whose query values are redacted to spans. WithStandardClientMetrics
// explicitly enables http.client.request.duration and its required
// server.address and server.port dimensions. Metric attributes are configured
// independently from span attributes; Clientkit never adds URLs or raw errors
// to metrics automatically. Callers remain responsible for keeping explicitly
// supplied metric attributes safe and low-cardinality.
//
// Both constructors capture applicable global OpenTelemetry providers or the
// TextMapPropagator during construction unless explicit options are supplied.
// Applications should configure globals first and remain responsible for SDK,
// exporter, provider, and propagator lifecycle.
//
// Clientkit automatically installs the logical observer and physical transport
// instrumentation only when httpclient owns the default HTTP client and
// Config.Observer is nil. Explicit wiring for a caller-owned HTTP client or a
// custom observer looks like:
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
// Non-nil observers and caller-owned HTTP clients replace the automatic
// instrumentation boundary, so these explicit adapters are not duplicated. Use
// clientkit.MultiObserver or httpclient.MultiHeaderPropagator for explicit
// additive composition. Injected header values may be sensitive and must never
// be used as telemetry labels.
package otel
