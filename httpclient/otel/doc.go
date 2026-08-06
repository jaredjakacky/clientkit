// Package otel adapts OpenTelemetry text-map propagation to Clientkit's HTTP
// header propagation contract. It injects active context into outbound headers
// but does not create spans or metrics.
//
// The package captures the global OpenTelemetry TextMapPropagator when New is
// called by default. Applications should configure the global propagator before
// construction, or supply an explicit propagator. OpenTelemetry may activate an
// initially captured delegating no-op when the first global is installed. The
// application remains responsible for OpenTelemetry SDK lifecycle and global
// propagator configuration.
//
// The root Clientkit OpenTelemetry observer creates operation spans and
// metrics, while this package independently injects their active context:
//
//	telemetry, err := clientkitotel.New()
//	if err != nil {
//		// handle error
//	}
//
//	headers := httpclientotel.New()
//
//	client, err := httpclient.New(httpclient.Config{
//		Config: clientkit.Config{
//			Name:     "payments",
//			Observer: telemetry,
//		},
//		BaseURL:   "https://payments.internal",
//		Propagator: headers,
//	})
//
// Non-nil observers and propagators replace the protocol client's automatic
// defaults, so these explicit adapters are not duplicated. Use
// clientkit.MultiObserver or httpclient.MultiHeaderPropagator for explicit
// additive composition. Injected header values may be sensitive and must never
// be used as telemetry labels.
package otel
