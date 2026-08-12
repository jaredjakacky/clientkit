package otel_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	httpclientotel "github.com/jaredjakacky/clientkit/httpclient/otel"
	globalotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestPropagatorInjectsConfiguredOpenTelemetryContext(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	if err != nil {
		t.Fatalf("TraceIDFromHex() error = %v", err)
	}
	spanID, err := trace.SpanIDFromHex("0102030405060708")
	if err != nil {
		t.Fatalf("SpanIDFromHex() error = %v", err)
	}
	traceState, err := trace.ParseTraceState("vendor=value")
	if err != nil {
		t.Fatalf("ParseTraceState() error = %v", err)
	}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		TraceState: traceState,
	}))

	propagator := httpclientotel.NewWithTextMapPropagator(propagation.TraceContext{})
	headers := http.Header{"Existing": []string{"preserved"}}
	propagator.Inject(ctx, headers)

	if got := headers.Get("Traceparent"); got != "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01" {
		t.Fatalf("Traceparent = %q, want injected W3C context", got)
	}
	if got := headers.Get("Tracestate"); got != "vendor=value" {
		t.Fatalf("Tracestate = %q, want vendor=value", got)
	}
	if got := headers.Get("Existing"); got != "preserved" {
		t.Fatalf("Existing = %q, want preserved", got)
	}
}

func TestPropagatorCapturesConfigurationAtConstruction(t *testing.T) {
	// The OpenTelemetry propagator is process-global, so this test must remain
	// sequential and restore the application setting when it completes.
	previous := globalotel.GetTextMapPropagator()
	t.Cleanup(func() { globalotel.SetTextMapPropagator(previous) })

	globalotel.SetTextMapPropagator(headerTextMapPropagator{value: "global-first"})
	captured := httpclientotel.New()
	nilFallback := httpclientotel.NewWithTextMapPropagator(nil)
	explicit := httpclientotel.NewWithTextMapPropagator(headerTextMapPropagator{value: "explicit"})

	globalotel.SetTextMapPropagator(headerTextMapPropagator{value: "global-second"})
	later := httpclientotel.New()

	for _, test := range []struct {
		name       string
		propagator *httpclientotel.Propagator
		want       string
	}{
		{name: "captured global", propagator: captured, want: "global-first"},
		{name: "nil propagator falls back to global", propagator: nilFallback, want: "global-first"},
		{name: "explicit propagator", propagator: explicit, want: "explicit"},
		{name: "later construction captures replacement", propagator: later, want: "global-second"},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			test.propagator.Inject(context.Background(), headers)
			if got := headers.Get("X-Test-Trace"); got != test.want {
				t.Fatalf("X-Test-Trace = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPropagatorHandlesNilInputs(t *testing.T) {
	recorder := &recordingTextMapPropagator{}
	propagator := httpclientotel.NewWithTextMapPropagator(recorder)

	headers := http.Header{"Existing": []string{"preserved"}}
	var nilPropagator *httpclientotel.Propagator
	nilPropagator.Inject(context.Background(), headers)
	if got := headers.Get("Existing"); got != "preserved" {
		t.Fatalf("nil receiver changed Existing to %q", got)
	}

	propagator.Inject(context.Background(), nil)
	if recorder.calls != 0 {
		t.Fatalf("nil-header injection calls = %d, want 0", recorder.calls)
	}

	var nilContext context.Context
	propagator.Inject(nilContext, headers)
	if recorder.calls != 1 || !recorder.sawContext {
		t.Fatalf("nil-context injection = %#v, want one call with background context", recorder)
	}
}

func TestPropagatorSupportsConcurrentInjection(t *testing.T) {
	propagator := httpclientotel.NewWithTextMapPropagator(headerTextMapPropagator{value: "concurrent"})

	const goroutines = 32
	var waitGroup sync.WaitGroup
	errors := make(chan string, goroutines)
	for range goroutines {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			headers := make(http.Header)
			propagator.Inject(context.Background(), headers)
			if got := headers.Get("X-Test-Trace"); got != "concurrent" {
				errors <- got
			}
		}()
	}
	waitGroup.Wait()
	close(errors)
	for got := range errors {
		t.Errorf("X-Test-Trace = %q, want concurrent", got)
	}
}

type headerTextMapPropagator struct {
	value string
}

func (propagator headerTextMapPropagator) Inject(_ context.Context, carrier propagation.TextMapCarrier) {
	carrier.Set("X-Test-Trace", propagator.value)
}

func (headerTextMapPropagator) Extract(ctx context.Context, _ propagation.TextMapCarrier) context.Context {
	return ctx
}

func (headerTextMapPropagator) Fields() []string {
	return []string{"X-Test-Trace"}
}

type recordingTextMapPropagator struct {
	calls      int
	sawContext bool
}

func (propagator *recordingTextMapPropagator) Inject(ctx context.Context, _ propagation.TextMapCarrier) {
	propagator.calls++
	propagator.sawContext = ctx != nil
}

func (*recordingTextMapPropagator) Extract(ctx context.Context, _ propagation.TextMapCarrier) context.Context {
	return ctx
}

func (*recordingTextMapPropagator) Fields() []string {
	return nil
}
