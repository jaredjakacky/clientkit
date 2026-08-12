package otel

import (
	"context"
	"errors"
	"net/http"

	"github.com/jaredjakacky/clientkit/internal/httpotel"
	globalotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Transport records one OpenTelemetry CLIENT span for each RoundTrip. Spans
// end after response headers or a transport error; response-body use does not
// extend their lifecycle.
type Transport struct {
	transport *httpotel.Transport
}

// NewTransport wraps base with Clientkit's per-RoundTrip OpenTelemetry
// instrumentation. A nil base selects http.DefaultTransport. The global
// providers and text-map propagator are captured during construction unless
// explicit options are supplied.
func NewTransport(base http.RoundTripper, options ...Option) (*Transport, error) {
	cfg := config{}
	for _, option := range options {
		if option != nil {
			option(&cfg)
		}
	}
	if cfg.propagator == nil {
		cfg.propagator = globalotel.GetTextMapPropagator()
	}

	instrumented, err := httpotel.NewTransport(base, httpotel.Config{
		TracerProvider:          cfg.tracerProvider,
		MeterProvider:           cfg.meterProvider,
		InstrumentationVersion:  cfg.instrumentationVersion,
		Inject:                  textMapInjection(cfg.propagator),
		SpanAttributes:          cfg.spanAttributes,
		MetricAttributes:        cfg.metricAttributes,
		RequestTargetAttributes: cfg.requestTargets,
		StandardMetrics:         cfg.standardMetrics,
	})
	if err != nil {
		return nil, err
	}
	return &Transport{transport: instrumented}, nil
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t == nil || t.transport == nil {
		return nil, errors.New("clientkit: HTTP telemetry transport is not configured")
	}
	return t.transport.RoundTrip(request)
}

// CloseIdleConnections forwards idle-pool cleanup to the wrapped transport
// when it exposes the standard net/http optional capability.
func (t *Transport) CloseIdleConnections() {
	if t == nil || t.transport == nil {
		return
	}
	t.transport.CloseIdleConnections()
}

func textMapInjection(propagator propagation.TextMapPropagator) func(context.Context, http.Header) {
	return func(ctx context.Context, headers http.Header) {
		if propagator == nil || headers == nil {
			return
		}
		propagator.Inject(ctx, propagation.HeaderCarrier(headers))
	}
}
