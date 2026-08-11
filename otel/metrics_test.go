package otel_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	clientkitotel "github.com/jaredjakacky/clientkit/otel"
	"github.com/jaredjakacky/opskit"
	"go.opentelemetry.io/otel/attribute"
)

func TestObserverRecordsEveryMetricWithValuesAndAttributes(t *testing.T) {
	traces := newRecordingTracerProvider()
	meters, metrics := newRecordingMeterProvider()
	observer, err := clientkitotel.New(
		clientkitotel.WithTracerProvider(traces),
		clientkitotel.WithMeterProvider(meters),
		clientkitotel.WithMetricAttributes(attribute.String("environment", "test")),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	type contextKey string
	base := context.WithValue(context.Background(), contextKey("request"), "request-42")
	ctx, operation := observer.StartOperation(base, clientkit.OperationStartEvent{})

	observer.ObserveAttempt(ctx, clientkit.AttemptEvent{
		Client:       "payments",
		Protocol:     "http",
		Operation:    "request",
		Number:       2,
		Duration:     500 * time.Millisecond,
		Outcome:      "timeout",
		FailureClass: clientkit.FailureTimeout,
		Attributes:   []opskit.Attribute{{Key: "http.request.method", Value: "GET"}},
	})
	observer.ObserveRetry(ctx, clientkit.RetryEvent{
		Client:       "payments",
		Protocol:     "http",
		Operation:    "request",
		AfterAttempt: 2,
		Delay:        250 * time.Millisecond,
		Cause:        "timeout",
		FailureClass: clientkit.FailureTimeout,
	})
	observer.ObserveHealth(ctx, clientkit.HealthEvent{
		Client:       "payments",
		Protocol:     "http",
		State:        clientkit.HealthDegraded,
		FailureClass: clientkit.FailureRemoteResponse,
		Duration:     125 * time.Millisecond,
	})
	operation.End(ctx, clientkit.OperationEndEvent{
		Client:       "payments",
		Protocol:     "http",
		Operation:    "request",
		Duration:     2500 * time.Millisecond,
		Attempts:     3,
		Outcome:      "rejected",
		FailureClass: clientkit.FailurePolicy,
	})

	records := metrics.records()
	wantValues := map[string]any{
		"clientkit.operations":         int64(1),
		"clientkit.operation.duration": float64(2.5),
		"clientkit.operation.attempts": int64(3),
		"clientkit.attempts":           int64(1),
		"clientkit.attempt.duration":   float64(0.5),
		"clientkit.retries":            int64(1),
		"clientkit.retry.delay":        float64(0.25),
		"clientkit.health.checks":      int64(1),
		"clientkit.health.duration":    float64(0.125),
	}
	if len(records) != len(wantValues) {
		t.Fatalf("metric records = %d, want %d: %#v", len(records), len(wantValues), records)
	}
	for name, want := range wantValues {
		record := onlyMetricRecord(t, records, name)
		if !reflect.DeepEqual(record.value, want) {
			t.Errorf("metric %q value = %#v, want %#v", name, record.value, want)
		}
		if record.context.Value(contextKey("request")) != "request-42" {
			t.Errorf("metric %q did not preserve measurement context", name)
		}
		if got := metricAttributeValue(t, record, "environment").AsString(); got != "test" {
			t.Errorf("metric %q environment = %q, want test", name, got)
		}
	}
	assertSameMetricAttributes(t, records,
		"clientkit.operations",
		"clientkit.operation.duration",
		"clientkit.operation.attempts",
	)
	assertSameMetricAttributes(t, records,
		"clientkit.attempts",
		"clientkit.attempt.duration",
	)
	assertSameMetricAttributes(t, records,
		"clientkit.retries",
		"clientkit.retry.delay",
	)
	assertSameMetricAttributes(t, records,
		"clientkit.health.checks",
		"clientkit.health.duration",
	)

	operationMetric := onlyMetricRecord(t, records, "clientkit.operations")
	if metricAttributeValue(t, operationMetric, clientkitotel.AttributeClientName).AsString() != "payments" ||
		metricAttributeValue(t, operationMetric, clientkitotel.AttributeOutcome).AsString() != "rejected" ||
		metricAttributeValue(t, operationMetric, clientkitotel.AttributeSucceeded).AsBool() ||
		metricAttributeValue(t, operationMetric, clientkitotel.AttributeFailureClass).AsString() != string(clientkit.FailurePolicy) {
		t.Fatalf("operation metric attributes = %#v, want normalized failure", operationMetric.attributes.ToSlice())
	}
	attemptMetric := onlyMetricRecord(t, records, "clientkit.attempts")
	if metricAttributeValue(t, attemptMetric, clientkitotel.AttributeOutcome).AsString() != "timeout" ||
		metricAttributeValue(t, attemptMetric, clientkitotel.AttributeSucceeded).AsBool() ||
		metricAttributeValue(t, attemptMetric, clientkitotel.AttributeFailureClass).AsString() != string(clientkit.FailureTimeout) ||
		metricAttributeValue(t, attemptMetric, "http.request.method").AsString() != "GET" {
		t.Fatalf("attempt metric attributes = %#v, want normalized timeout and method", attemptMetric.attributes.ToSlice())
	}
	assertNoMetricAttribute(t, attemptMetric, clientkitotel.AttributeAttemptNumber)
	retryMetric := onlyMetricRecord(t, records, "clientkit.retries")
	if metricAttributeValue(t, retryMetric, clientkitotel.AttributeRetryCause).AsString() != "timeout" ||
		metricAttributeValue(t, retryMetric, clientkitotel.AttributeFailureClass).AsString() != string(clientkit.FailureTimeout) {
		t.Fatalf("retry metric attributes = %#v, want bounded cause", retryMetric.attributes.ToSlice())
	}
	assertNoMetricAttribute(t, retryMetric, clientkitotel.AttributeRetryAfterAttempt)
	assertNoMetricAttribute(t, retryMetric, clientkitotel.AttributeRetryDelay)
	healthMetric := onlyMetricRecord(t, records, "clientkit.health.checks")
	if metricAttributeValue(t, healthMetric, clientkitotel.AttributeHealthState).AsString() != string(clientkit.HealthDegraded) ||
		metricAttributeValue(t, healthMetric, clientkitotel.AttributeFailureClass).AsString() != string(clientkit.FailureRemoteResponse) {
		t.Fatalf("health metric attributes = %#v, want degraded", healthMetric.attributes.ToSlice())
	}
}

func TestSpanAndMetricAttributesRemainSignalSpecific(t *testing.T) {
	telemetry := newTelemetryFixture(t,
		clientkitotel.WithSpanAttributes(attribute.String("trace.only", "trace-value")),
		clientkitotel.WithMetricAttributes(attribute.String("metric.only", "metric-value")),
	)
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	operation.End(ctx, clientkit.OperationEndEvent{Succeeded: true})

	span := telemetry.traces.spans()[0]
	if got := attributeValue(t, span.Attributes, "trace.only").AsString(); got != "trace-value" {
		t.Fatalf("trace.only = %q, want trace-value", got)
	}
	assertNoAttribute(t, span.Attributes, "metric.only")

	metricRecord := onlyMetricRecord(t, telemetry.metrics.records(), "clientkit.operations")
	if got := metricAttributeValue(t, metricRecord, "metric.only").AsString(); got != "metric-value" {
		t.Fatalf("metric.only = %q, want metric-value", got)
	}
	assertNoMetricAttribute(t, metricRecord, "trace.only")
}

func TestNegativeMetricValuesOmitOnlyHistograms(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	telemetry.observer.ObserveAttempt(ctx, clientkit.AttemptEvent{Duration: -time.Second})
	telemetry.observer.ObserveRetry(ctx, clientkit.RetryEvent{Delay: -time.Second})
	telemetry.observer.ObserveHealth(ctx, clientkit.HealthEvent{Duration: -time.Second})
	operation.End(ctx, clientkit.OperationEndEvent{Duration: -time.Second, Attempts: -1})

	records := telemetry.metrics.records()
	got := make(map[string]int)
	for _, record := range records {
		got[record.name]++
	}
	want := map[string]int{
		"clientkit.attempts":      1,
		"clientkit.retries":       1,
		"clientkit.health.checks": 1,
		"clientkit.operations":    1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metric names = %v, want counters only %v", got, want)
	}
}

func TestZeroMetricValuesAreRecorded(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	telemetry.observer.ObserveAttempt(ctx, clientkit.AttemptEvent{})
	telemetry.observer.ObserveRetry(ctx, clientkit.RetryEvent{})
	telemetry.observer.ObserveHealth(ctx, clientkit.HealthEvent{})
	operation.End(ctx, clientkit.OperationEndEvent{})

	records := telemetry.metrics.records()
	for _, name := range []string{
		"clientkit.operation.duration",
		"clientkit.operation.attempts",
		"clientkit.attempt.duration",
		"clientkit.retry.delay",
		"clientkit.health.duration",
	} {
		record := onlyMetricRecord(t, records, name)
		switch value := record.value.(type) {
		case int64:
			if value != 0 {
				t.Errorf("metric %q = %d, want 0", name, value)
			}
		case float64:
			if value != 0 {
				t.Errorf("metric %q = %f, want 0", name, value)
			}
		default:
			t.Errorf("metric %q has unexpected value %#v", name, value)
		}
	}
	span := telemetry.traces.spans()[0]
	assertNoAttribute(t, span.Attributes, clientkitotel.AttributeOperationAttempts)
}

func TestHealthAddsEventOnlyToExistingRecordingSpan(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	checkedAt := time.Now().UTC()
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	telemetry.observer.ObserveHealth(ctx, clientkit.HealthEvent{
		Client:       "payments",
		Protocol:     "tcp",
		State:        clientkit.HealthUnhealthy,
		FailureClass: clientkit.FailureConnectionRefused,
		CheckedAt:    checkedAt,
		Attributes:   []opskit.Attribute{{Key: "client.security", Value: "tls"}},
	})
	operation.End(ctx, clientkit.OperationEndEvent{Succeeded: true})

	spans := telemetry.traces.spans()
	if len(spans) != 1 || len(spans[0].Events) != 1 {
		t.Fatalf("spans = %#v, want one health event on existing span", spans)
	}
	event := spans[0].Events[0]
	if event.Name != "clientkit.health" || !event.Time.Equal(checkedAt) {
		t.Fatalf("health event = %#v, want supplied timestamp", event)
	}
	if attributeValue(t, event.Attributes, clientkitotel.AttributeHealthState).AsString() != string(clientkit.HealthUnhealthy) ||
		attributeValue(t, event.Attributes, clientkitotel.AttributeFailureClass).AsString() != string(clientkit.FailureConnectionRefused) ||
		attributeValue(t, event.Attributes, "client.security").AsString() != "tls" {
		t.Fatalf("health event attributes = %#v, want complete health state", event.Attributes)
	}

	withoutSpan := newTelemetryFixture(t)
	withoutSpan.observer.ObserveHealth(context.Background(), clientkit.HealthEvent{State: clientkit.HealthHealthy})
	if len(withoutSpan.traces.spans()) != 0 || len(withoutSpan.metrics.records()) != 2 {
		t.Fatalf("standalone health telemetry = (%d spans, %d metrics), want (0, 2)", len(withoutSpan.traces.spans()), len(withoutSpan.metrics.records()))
	}
}
