package otel_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	clientkitotel "github.com/jaredjakacky/clientkit/otel"
	apiotel "go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestAttributeVocabulary(t *testing.T) {
	want := map[string]string{
		"client name":         clientkitotel.AttributeClientName,
		"protocol":            clientkitotel.AttributeProtocol,
		"operation":           clientkitotel.AttributeOperation,
		"outcome":             clientkitotel.AttributeOutcome,
		"succeeded":           clientkitotel.AttributeSucceeded,
		"operation attempts":  clientkitotel.AttributeOperationAttempts,
		"attempt number":      clientkitotel.AttributeAttemptNumber,
		"retry after attempt": clientkitotel.AttributeRetryAfterAttempt,
		"retry cause":         clientkitotel.AttributeRetryCause,
		"retry delay":         clientkitotel.AttributeRetryDelay,
		"health state":        clientkitotel.AttributeHealthState,
		"failure class":       clientkitotel.AttributeFailureClass,
	}
	exact := map[string]string{
		"client name":         "clientkit.client.name",
		"protocol":            "clientkit.protocol",
		"operation":           "clientkit.operation",
		"outcome":             "clientkit.outcome",
		"succeeded":           "clientkit.succeeded",
		"operation attempts":  "clientkit.operation.attempts",
		"attempt number":      "clientkit.attempt.number",
		"retry after attempt": "clientkit.retry.after_attempt",
		"retry cause":         "clientkit.retry.cause",
		"retry delay":         "clientkit.retry.delay",
		"health state":        "clientkit.health.state",
		"failure class":       "clientkit.failure.class",
	}
	if !reflect.DeepEqual(want, exact) {
		t.Fatalf("attribute vocabulary = %#v, want %#v", want, exact)
	}
}

func TestNewCreatesDocumentedInstrumentation(t *testing.T) {
	traces := newRecordingTracerProvider()
	meters, metrics := newRecordingMeterProvider()
	observer, err := clientkitotel.New(
		nil,
		clientkitotel.WithTracerProvider(traces),
		clientkitotel.WithMeterProvider(meters),
		clientkitotel.WithInstrumentationVersion("1.2.3"),
	)
	if err != nil || observer == nil {
		t.Fatalf("New() = (%v, %v), want observer", observer, err)
	}

	wantScope := []scopeRecord{{Name: "github.com/jaredjakacky/clientkit/otel", Version: "1.2.3"}}
	if got := traces.scopeRecords(); !reflect.DeepEqual(got, wantScope) {
		t.Fatalf("tracer scopes = %#v, want %#v", got, wantScope)
	}
	if got := metrics.scopeRecords(); !reflect.DeepEqual(got, wantScope) {
		t.Fatalf("meter scopes = %#v, want %#v", got, wantScope)
	}

	wantInstruments := map[string]instrumentRecord{
		"clientkit.operations":         {name: "clientkit.operations", kind: "int64_counter", unit: "{operation}", description: "Completed client operations."},
		"clientkit.operation.duration": {name: "clientkit.operation.duration", kind: "float64_histogram", unit: "s", description: "Client operation duration."},
		"clientkit.operation.attempts": {name: "clientkit.operation.attempts", kind: "int64_histogram", unit: "{attempt}", description: "Attempts per client operation."},
		"clientkit.attempts":           {name: "clientkit.attempts", kind: "int64_counter", unit: "{attempt}", description: "Completed client attempts."},
		"clientkit.attempt.duration":   {name: "clientkit.attempt.duration", kind: "float64_histogram", unit: "s", description: "Client attempt duration."},
		"clientkit.retries":            {name: "clientkit.retries", kind: "int64_counter", unit: "{retry}", description: "Scheduled client retries."},
		"clientkit.retry.delay":        {name: "clientkit.retry.delay", kind: "float64_histogram", unit: "s", description: "Selected client retry delay."},
		"clientkit.health.checks":      {name: "clientkit.health.checks", kind: "int64_counter", unit: "{check}", description: "Completed client health checks."},
		"clientkit.health.duration":    {name: "clientkit.health.duration", kind: "float64_histogram", unit: "s", description: "Client health-check duration."},
	}
	gotInstruments := metrics.instrumentRecords()
	if len(gotInstruments) != len(wantInstruments) {
		t.Fatalf("instruments = %d, want %d: %#v", len(gotInstruments), len(wantInstruments), gotInstruments)
	}
	seen := make(map[string]struct{}, len(gotInstruments))
	for _, instrument := range gotInstruments {
		if _, duplicate := seen[instrument.name]; duplicate {
			t.Errorf("instrument %q was created more than once", instrument.name)
		}
		seen[instrument.name] = struct{}{}
		want, ok := wantInstruments[instrument.name]
		if !ok || instrument != want {
			t.Errorf("instrument %q = %#v, want %#v", instrument.name, instrument, want)
		}
	}
}

func TestNewReportsEveryInstrumentCreationFailure(t *testing.T) {
	wantErr := errors.New("instrument creation failed")
	for _, name := range []string{
		"clientkit.operations",
		"clientkit.operation.duration",
		"clientkit.operation.attempts",
		"clientkit.attempts",
		"clientkit.attempt.duration",
		"clientkit.retries",
		"clientkit.retry.delay",
		"clientkit.health.checks",
		"clientkit.health.duration",
	} {
		t.Run(name, func(t *testing.T) {
			meters, recorder := newRecordingMeterProvider()
			recorder.failInstrument = name
			recorder.instrumentError = wantErr
			observer, err := clientkitotel.New(
				clientkitotel.WithTracerProvider(tracenoop.NewTracerProvider()),
				clientkitotel.WithMeterProvider(meters),
			)
			if observer != nil || !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "create "+name) {
				t.Fatalf("New() = (%v, %v), want wrapped %q failure", observer, err, name)
			}
		})
	}
}

func TestNewCapturesGlobalProvidersAtConstruction(t *testing.T) {
	// This test changes process-global OTel providers and must not run in parallel.
	previousTracer := apiotel.GetTracerProvider()
	previousMeter := apiotel.GetMeterProvider()
	t.Cleanup(func() {
		apiotel.SetTracerProvider(previousTracer)
		apiotel.SetMeterProvider(previousMeter)
	})

	firstTraces := newRecordingTracerProvider()
	firstMeters, firstMetrics := newRecordingMeterProvider()
	apiotel.SetTracerProvider(firstTraces)
	apiotel.SetMeterProvider(firstMeters)
	first, err := clientkitotel.New(
		clientkitotel.WithTracerProvider(nil),
		clientkitotel.WithMeterProvider(nil),
	)
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}

	secondTraces := newRecordingTracerProvider()
	secondMeters, secondMetrics := newRecordingMeterProvider()
	apiotel.SetTracerProvider(secondTraces)
	apiotel.SetMeterProvider(secondMeters)
	second, err := clientkitotel.New()
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}

	ctx, firstOperation := first.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	firstOperation.End(ctx, clientkit.OperationEndEvent{Succeeded: true})
	ctx, secondOperation := second.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	secondOperation.End(ctx, clientkit.OperationEndEvent{Succeeded: true})

	if len(firstTraces.spans()) != 1 || len(secondTraces.spans()) != 1 {
		t.Fatalf("captured trace counts = (%d, %d), want one per constructed observer", len(firstTraces.spans()), len(secondTraces.spans()))
	}
	if len(firstMetrics.records()) == 0 || len(secondMetrics.records()) == 0 {
		t.Fatalf("captured metric counts = (%d, %d), want telemetry from each constructed observer", len(firstMetrics.records()), len(secondMetrics.records()))
	}
}

func TestNewAcceptsNoopProviders(t *testing.T) {
	observer, err := clientkitotel.New(
		clientkitotel.WithTracerProvider(tracenoop.NewTracerProvider()),
		clientkitotel.WithMeterProvider(metricnoop.NewMeterProvider()),
	)
	if err != nil || observer == nil {
		t.Fatalf("New() = (%v, %v), want observer backed by no-op providers", observer, err)
	}
}
