// Package slogobserver adapts Clientkit observer events to synchronous
// structured log/slog records. It does not configure a slog handler or own its
// lifecycle, and it creates no spans or metrics.
//
// Routine successful operations, attempts, and healthy checks use Debug by
// default. Retries and degraded, unhealthy, or unknown checks use Warn, while
// final operation failures use Error. Raw Go errors are omitted unless
// WithErrorDetails is selected.
//
// Clientkit-controlled records never include URLs, paths, headers, bodies,
// propagated identifiers, or certificate details from Clientkit events. The
// observer remains independently composable with the OpenTelemetry observer by
// using clientkit.MultiObserver.
//
// The example below uses these imports:
//
//	import (
//		"log/slog"
//		"net/http"
//		"os"
//
//		clientkit "github.com/jaredjakacky/clientkit"
//		httpclient "github.com/jaredjakacky/clientkit/httpclient"
//		httpclientotel "github.com/jaredjakacky/clientkit/httpclient/otel"
//		clientkitotel "github.com/jaredjakacky/clientkit/otel"
//		slogobserver "github.com/jaredjakacky/clientkit/slogobserver"
//	)
//
// Typical wiring looks like:
//
//	logger := slog.New(
//		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
//			Level: slog.LevelDebug,
//		}),
//	)
//
//	logs := slogobserver.New(
//		logger,
//		slogobserver.WithAttributes(
//			slog.String("component", "outbound-clients"),
//		),
//	)
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
//			Observer: clientkit.MultiObserver(
//				logs,
//				telemetry,
//			),
//		},
//		BaseURL:   "https://payments.internal",
//		HTTPClient: &http.Client{Transport: attemptTransport},
//	})
package slogobserver
