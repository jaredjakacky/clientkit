package otel

import (
	"context"
	"net/http"

	"github.com/jaredjakacky/clientkit/httpclient"
	globalotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var _ httpclient.HeaderPropagator = (*Propagator)(nil)

type config struct {
	propagator             propagation.TextMapPropagator
	tracerProvider         trace.TracerProvider
	meterProvider          metric.MeterProvider
	instrumentationVersion string
	spanAttributes         []attribute.KeyValue
	metricAttributes       []attribute.KeyValue
	requestTargets         bool
	standardMetrics        bool
}

// Option configures a Transport during construction.
type Option func(*config)

// WithTextMapPropagator uses propagator for Transport outbound injection. A nil
// value falls back to the global OpenTelemetry propagator during construction.
func WithTextMapPropagator(propagator propagation.TextMapPropagator) Option {
	return func(cfg *config) {
		cfg.propagator = propagator
	}
}

// WithTracerProvider uses provider for per-RoundTrip client spans. A nil
// provider falls back to the global provider during transport construction.
func WithTracerProvider(provider trace.TracerProvider) Option {
	return func(cfg *config) {
		cfg.tracerProvider = provider
	}
}

// WithMeterProvider uses provider for explicitly enabled standard HTTP client
// metrics. A nil provider falls back to the global provider during transport
// construction.
func WithMeterProvider(provider metric.MeterProvider) Option {
	return func(cfg *config) {
		cfg.meterProvider = provider
	}
}

// WithInstrumentationVersion sets the instrumentation scope version used by
// transports.
func WithInstrumentationVersion(version string) Option {
	return func(cfg *config) {
		cfg.instrumentationVersion = version
	}
}

// WithSpanAttributes adds attributes to each physical HTTP CLIENT span. Values
// may have trace-appropriate cardinality but must not contain secrets.
func WithSpanAttributes(attributes ...attribute.KeyValue) Option {
	cloned := append([]attribute.KeyValue(nil), attributes...)
	return func(cfg *config) {
		cfg.spanAttributes = append(cfg.spanAttributes, cloned...)
	}
}

// WithMetricAttributes adds bounded attributes to explicitly enabled standard
// HTTP client metrics. Values must be stable and low-cardinality.
func WithMetricAttributes(attributes ...attribute.KeyValue) Option {
	cloned := append([]attribute.KeyValue(nil), attributes...)
	return func(cfg *config) {
		cfg.metricAttributes = append(cfg.metricAttributes, cloned...)
	}
}

// WithRequestTargetAttributes includes server.address, server.port, and a URL
// with user information, query, and fragment omitted on physical HTTP spans. It
// is disabled by default because paths and endpoint identity may be sensitive.
func WithRequestTargetAttributes() Option {
	return func(cfg *config) {
		cfg.requestTargets = true
	}
}

// WithStandardClientMetrics enables http.client.request.duration. Enabling it
// explicitly consents to server.address and server.port metric dimensions,
// which are required by the OpenTelemetry HTTP semantic conventions.
func WithStandardClientMetrics() Option {
	return func(cfg *config) {
		cfg.standardMetrics = true
	}
}

// Propagator injects OpenTelemetry context into outbound HTTP headers without
// creating spans or metrics. It may be called concurrently.
type Propagator struct {
	propagator propagation.TextMapPropagator
}

// New constructs a Propagator and captures the global OpenTelemetry text-map
// propagator.
func New() *Propagator {
	return NewWithTextMapPropagator(nil)
}

// NewWithTextMapPropagator constructs a Propagator using propagator. A nil
// value captures the global OpenTelemetry text-map propagator.
func NewWithTextMapPropagator(propagator propagation.TextMapPropagator) *Propagator {
	if propagator == nil {
		propagator = globalotel.GetTextMapPropagator()
	}
	return &Propagator{propagator: propagator}
}

// Inject writes the configured OpenTelemetry propagation fields into headers.
// It does not extract context, create telemetry, or retain the header map.
func (p *Propagator) Inject(ctx context.Context, headers http.Header) {
	if p == nil || p.propagator == nil || headers == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.propagator.Inject(ctx, propagation.HeaderCarrier(headers))
}
