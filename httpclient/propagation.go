package httpclient

import (
	"context"
	"net/http"

	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// HeaderPropagator injects context-derived metadata into a request-specific
// outbound header map once per transport RoundTrip. Implementations may be called
// concurrently, must return quickly, and must not retain or mutate the supplied
// header after Inject returns. Use Header.Set for single-valued metadata. Header
// values may contain sensitive data and must never be copied into telemetry
// attributes.
type HeaderPropagator interface {
	Inject(context.Context, http.Header)
}

// HeaderPropagatorFunc adapts a function to HeaderPropagator.
type HeaderPropagatorFunc func(context.Context, http.Header)

// Inject invokes fn when it is non-nil.
func (fn HeaderPropagatorFunc) Inject(ctx context.Context, headers http.Header) {
	if fn != nil {
		fn(ctx, headers)
	}
}

// NopHeaderPropagator performs no outbound header injection. Supplying it in
// Config explicitly disables the default OpenTelemetry trace propagator.
type NopHeaderPropagator struct{}

func (NopHeaderPropagator) clientkitSafeHeaderPropagator() {}

// Inject performs no work.
func (NopHeaderPropagator) Inject(context.Context, http.Header) {}

type defaultOpenTelemetryHeaderPropagator struct {
	propagator propagation.TextMapPropagator
}

func newDefaultOpenTelemetryHeaderPropagator() defaultOpenTelemetryHeaderPropagator {
	return defaultOpenTelemetryHeaderPropagator{
		propagator: apiotel.GetTextMapPropagator(),
	}
}

func (p defaultOpenTelemetryHeaderPropagator) Inject(ctx context.Context, headers http.Header) {
	if p.propagator == nil || headers == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.propagator.Inject(ctx, propagation.HeaderCarrier(headers))
}

// SafeHeaderPropagator prevents propagation panics from affecting HTTP
// requests. If injection panics, the attempt headers are restored to their
// prior state. A nil propagator becomes NopHeaderPropagator.
func SafeHeaderPropagator(propagator HeaderPropagator) HeaderPropagator {
	if propagator == nil {
		return NopHeaderPropagator{}
	}
	if _, ok := propagator.(interface{ clientkitSafeHeaderPropagator() }); ok {
		return propagator
	}
	return safeHeaderPropagator{propagator: propagator}
}

type safeHeaderPropagator struct {
	propagator HeaderPropagator
}

func (safeHeaderPropagator) clientkitSafeHeaderPropagator() {}

func (p safeHeaderPropagator) Inject(ctx context.Context, headers http.Header) {
	injectHeaderSafely(p.propagator, ctx, headers)
}

// MultiHeaderPropagator explicitly invokes non-nil propagators in registration
// order against the same attempt-specific header map. A panic from one
// propagator does not prevent later propagators from running.
func MultiHeaderPropagator(propagators ...HeaderPropagator) HeaderPropagator {
	usable := make([]HeaderPropagator, 0, len(propagators))
	for _, propagator := range propagators {
		if propagator != nil {
			usable = append(usable, propagator)
		}
	}
	if len(usable) == 0 {
		return NopHeaderPropagator{}
	}
	if len(usable) == 1 {
		return SafeHeaderPropagator(usable[0])
	}
	return multiHeaderPropagator{propagators: usable}
}

type multiHeaderPropagator struct {
	propagators []HeaderPropagator
}

func (multiHeaderPropagator) clientkitSafeHeaderPropagator() {}

func (p multiHeaderPropagator) Inject(ctx context.Context, headers http.Header) {
	for _, propagator := range p.propagators {
		injectHeaderSafely(propagator, ctx, headers)
	}
}

func injectHeaderSafely(propagator HeaderPropagator, ctx context.Context, headers http.Header) {
	backup := headers.Clone()
	completed := false
	defer func() {
		if recover() != nil || !completed {
			restoreHeaders(headers, backup)
		}
	}()
	propagator.Inject(ctx, headers)
	completed = true
}

func restoreHeaders(headers http.Header, backup http.Header) {
	clear(headers)
	for key, values := range backup {
		headers[key] = append([]string(nil), values...)
	}
}

type propagatingRoundTripper struct {
	base       http.RoundTripper
	propagator HeaderPropagator
}

func (t *propagatingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	SafeHeaderPropagator(t.propagator).Inject(cloned.Context(), cloned.Header)
	return base.RoundTrip(cloned)
}
