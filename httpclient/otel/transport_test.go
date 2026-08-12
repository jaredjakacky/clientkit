package otel_test

import (
	"context"
	"net/http"
	"testing"

	httpclientotel "github.com/jaredjakacky/clientkit/httpclient/otel"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestTransportPublicConstructorAndOptions(t *testing.T) {
	baseCalls := 0
	base := transportFunc(func(request *http.Request) (*http.Response, error) {
		baseCalls++
		if got := request.Header.Get("X-Test-Trace"); got != "attempt" {
			t.Fatalf("injected header = %q, want attempt", got)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	})
	transport, err := httpclientotel.NewTransport(
		base,
		httpclientotel.WithTextMapPropagator(testTextMapPropagator{}),
		httpclientotel.WithTracerProvider(tracenoop.NewTracerProvider()),
		httpclientotel.WithMeterProvider(metricnoop.NewMeterProvider()),
		httpclientotel.WithInstrumentationVersion("test-version"),
		httpclientotel.WithSpanAttributes(attribute.String("trace.only", "value")),
		httpclientotel.WithMetricAttributes(attribute.String("metric.only", "value")),
		httpclientotel.WithRequestTargetAttributes(),
		httpclientotel.WithStandardClientMetrics(),
	)
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/resource", nil)
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if baseCalls != 1 {
		t.Fatalf("base calls = %d, want 1", baseCalls)
	}
	if request.Header.Get("X-Test-Trace") != "" {
		t.Fatalf("caller request headers = %#v, want unchanged", request.Header)
	}
}

func TestTransportZeroValueReturnsConfigurationError(t *testing.T) {
	var transport *httpclientotel.Transport
	request, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if response, err := transport.RoundTrip(request); response != nil || err == nil {
		t.Fatalf("nil Transport.RoundTrip() = (%v, %v), want configuration error", response, err)
	}

	transport = &httpclientotel.Transport{}
	if response, err := transport.RoundTrip(request); response != nil || err == nil {
		t.Fatalf("zero Transport.RoundTrip() = (%v, %v), want configuration error", response, err)
	}
	transport.CloseIdleConnections()
}

func TestTransportForwardsCloseIdleConnections(t *testing.T) {
	base := &idleClosingTransport{}
	transport, err := httpclientotel.NewTransport(base)
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	transport.CloseIdleConnections()
	if base.closeCalls != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", base.closeCalls)
	}
	var nilTransport *httpclientotel.Transport
	nilTransport.CloseIdleConnections()
}

type transportFunc func(*http.Request) (*http.Response, error)

func (fn transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type idleClosingTransport struct {
	closeCalls int
}

func (*idleClosingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.Canceled
}

func (transport *idleClosingTransport) CloseIdleConnections() {
	transport.closeCalls++
}

type testTextMapPropagator struct{}

func (testTextMapPropagator) Inject(_ context.Context, carrier propagation.TextMapCarrier) {
	carrier.Set("X-Test-Trace", "attempt")
}

func (testTextMapPropagator) Extract(ctx context.Context, _ propagation.TextMapCarrier) context.Context {
	return ctx
}

func (testTextMapPropagator) Fields() []string { return []string{"X-Test-Trace"} }
