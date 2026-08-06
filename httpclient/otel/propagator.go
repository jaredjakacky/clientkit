package otel

import (
	"context"
	"net/http"

	"github.com/jaredjakacky/clientkit/httpclient"
	globalotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

var _ httpclient.HeaderPropagator = (*Propagator)(nil)

type config struct {
	propagator propagation.TextMapPropagator
}

// Option configures a Propagator.
type Option func(*config)

// WithTextMapPropagator uses propagator for outbound injection. A nil value
// falls back to the global OpenTelemetry propagator during construction.
func WithTextMapPropagator(propagator propagation.TextMapPropagator) Option {
	return func(cfg *config) {
		cfg.propagator = propagator
	}
}

// Propagator injects OpenTelemetry context into outbound HTTP headers without
// creating spans or metrics. It may be called concurrently.
type Propagator struct {
	propagator propagation.TextMapPropagator
}

// New constructs a Propagator, capturing the global OpenTelemetry text-map
// propagator when no explicit propagator is supplied.
func New(options ...Option) *Propagator {
	cfg := config{}
	for _, option := range options {
		if option != nil {
			option(&cfg)
		}
	}
	if cfg.propagator == nil {
		cfg.propagator = globalotel.GetTextMapPropagator()
	}
	return &Propagator{propagator: cfg.propagator}
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
