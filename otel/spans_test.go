package otel_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	clientkitotel "github.com/jaredjakacky/clientkit/otel"
	"github.com/jaredjakacky/opskit"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func TestOperationSpanLifecycleAndAttributePrecedence(t *testing.T) {
	traces := newRecordingTracerProvider()
	meters, _ := newRecordingMeterProvider()
	common := []attribute.KeyValue{
		attribute.String("component", "original"),
		attribute.String("custom", "common"),
		attribute.String(clientkitotel.AttributeClientName, "common-client"),
		attribute.String(clientkitotel.AttributeFailureClass, "common-failure"),
		attribute.String(" ", "blank"),
	}
	option := clientkitotel.WithAttributes(common...)
	common[0] = attribute.String("component", "mutated")
	observer, err := clientkitotel.New(
		clientkitotel.WithTracerProvider(traces),
		clientkitotel.WithMeterProvider(meters),
		option,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	startedAt := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	startAttributes := []opskit.Attribute{
		{Key: "captured", Value: "before-start"},
		{Key: "custom", Value: "event-first"},
		{Key: "custom", Value: "event-last"},
		{Key: clientkitotel.AttributeClientName, Value: "event-client"},
		{Key: clientkitotel.AttributeFailureClass, Value: "event-failure"},
		{Key: "client.failure_class", Value: "legacy-failure"},
		{Key: " ", Value: "blank"},
	}
	ctx, operation := observer.StartOperation(context.Background(), clientkit.OperationStartEvent{
		Client:     "payments",
		Protocol:   "http",
		Operation:  "request",
		StartedAt:  startedAt,
		Attributes: startAttributes,
	})
	startAttributes[0].Value = "mutated-after-start"
	if !trace.SpanFromContext(ctx).IsRecording() {
		t.Fatal("StartOperation() context does not contain the recording span")
	}

	spans := traces.spans()
	if len(spans) != 1 {
		t.Fatalf("started spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name != "clientkit.http.request" || span.Kind != trace.SpanKindClient || !span.StartedAt.Equal(startedAt) {
		t.Fatalf("started span = %#v, want named client span with supplied timestamp", span)
	}
	if attributeValue(t, span.Attributes, "component").AsString() != "original" ||
		attributeValue(t, span.Attributes, "captured").AsString() != "before-start" ||
		attributeValue(t, span.Attributes, "custom").AsString() != "event-last" {
		t.Fatalf("start attributes = %#v, want cloned common and last event values", span.Attributes)
	}
	if attributeValue(t, span.Attributes, clientkitotel.AttributeClientName).AsString() != "payments" ||
		attributeValue(t, span.Attributes, clientkitotel.AttributeProtocol).AsString() != "http" ||
		attributeValue(t, span.Attributes, clientkitotel.AttributeOperation).AsString() != "request" {
		t.Fatalf("start identity attributes = %#v, want authoritative event identity", span.Attributes)
	}
	assertNoAttribute(t, span.Attributes, clientkitotel.AttributeFailureClass)
	assertNoAttribute(t, span.Attributes, "client.failure_class")
	assertNoAttribute(t, span.Attributes, " ")

	endedAt := startedAt.Add(2 * time.Second)
	operation.End(ctx, clientkit.OperationEndEvent{
		Client:     "payments",
		Protocol:   "http",
		Operation:  "request",
		EndedAt:    endedAt,
		Duration:   2 * time.Second,
		Attempts:   2,
		Outcome:    "success",
		Succeeded:  true,
		Attributes: []opskit.Attribute{{Key: "custom", Value: "end"}},
	})
	operation.End(ctx, clientkit.OperationEndEvent{Outcome: "failure"})

	span = traces.spans()[0]
	if span.EndCount != 1 || !span.EndedAt.Equal(endedAt) || span.StatusCode != codes.Ok || span.StatusDescription != "" {
		t.Fatalf("completed span = %#v, want one successful completion", span)
	}
	if attributeValue(t, span.Attributes, "custom").AsString() != "end" ||
		attributeValue(t, span.Attributes, clientkitotel.AttributeOutcome).AsString() != "success" ||
		!attributeValue(t, span.Attributes, clientkitotel.AttributeSucceeded).AsBool() ||
		attributeValue(t, span.Attributes, clientkitotel.AttributeOperationAttempts).AsInt64() != 2 {
		t.Fatalf("end attributes = %#v, want final operation result", span.Attributes)
	}
}

func TestOperationSpanNameFallback(t *testing.T) {
	for _, test := range []struct {
		name      string
		protocol  string
		operation string
		want      string
	}{
		{name: "complete", protocol: "tcp", operation: "dial", want: "clientkit.tcp.dial"},
		{name: "missing protocol", operation: "dial", want: "clientkit.operation"},
		{name: "blank protocol", protocol: " ", operation: "dial", want: "clientkit.operation"},
		{name: "missing operation", protocol: "tcp", want: "clientkit.operation"},
		{name: "blank operation", protocol: "tcp", operation: " ", want: "clientkit.operation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			telemetry := newTelemetryFixture(t)
			ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{
				Protocol: test.protocol, Operation: test.operation,
			})
			operation.End(ctx, clientkit.OperationEndEvent{Succeeded: true})
			if got := telemetry.traces.spans()[0].Name; got != test.want {
				t.Fatalf("span name = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOperationSpanStatusAndTerminalError(t *testing.T) {
	wantErr := errors.New("terminal failure")
	for _, test := range []struct {
		name        string
		event       clientkit.OperationEndEvent
		wantCode    codes.Code
		wantMessage string
		wantError   bool
	}{
		{name: "success", event: clientkit.OperationEndEvent{Outcome: "success", Succeeded: true}, wantCode: codes.Ok},
		{name: "outcome failure", event: clientkit.OperationEndEvent{Outcome: "rejected"}, wantCode: codes.Error, wantMessage: "rejected"},
		{name: "fallback failure", event: clientkit.OperationEndEvent{}, wantCode: codes.Error, wantMessage: "operation_failed"},
		{name: "failure class contradicts success", event: clientkit.OperationEndEvent{Outcome: "success", Succeeded: true, FailureClass: clientkit.FailurePolicy}, wantCode: codes.Error, wantMessage: "success"},
		{name: "error contradicts success", event: clientkit.OperationEndEvent{Outcome: "success", Succeeded: true, Err: wantErr}, wantCode: codes.Error, wantMessage: "success", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := []clientkitotel.Option(nil)
			if test.wantError {
				options = append(options, clientkitotel.WithErrorDetails())
			}
			telemetry := newTelemetryFixture(t, options...)
			endedAt := time.Now().UTC()
			test.event.EndedAt = endedAt
			ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
			operation.End(ctx, test.event)
			span := telemetry.traces.spans()[0]
			if span.StatusCode != test.wantCode || span.StatusDescription != test.wantMessage {
				t.Fatalf("span status = (%v, %q), want (%v, %q)", span.StatusCode, span.StatusDescription, test.wantCode, test.wantMessage)
			}
			if got := attributeValue(t, span.Attributes, clientkitotel.AttributeSucceeded).AsBool(); got != (test.wantCode == codes.Ok) {
				t.Fatalf("succeeded attribute = %t, want %t", got, test.wantCode == codes.Ok)
			}
			hasError := len(span.Errors) != 0
			if hasError != test.wantError {
				t.Fatalf("recorded errors = %#v, wantError=%t", span.Errors, test.wantError)
			}
			if test.wantError && (span.Errors[0].Err != wantErr || !span.Errors[0].Time.Equal(endedAt)) {
				t.Fatalf("recorded error = %#v, want original error at completion", span.Errors[0])
			}
		})
	}
}

func TestTerminalErrorWithoutTimestampUsesCurrentTime(t *testing.T) {
	telemetry := newTelemetryFixture(t, clientkitotel.WithErrorDetails())
	wantErr := errors.New("terminal failure")
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	before := time.Now()
	operation.End(ctx, clientkit.OperationEndEvent{Err: wantErr})
	after := time.Now()

	span := telemetry.traces.spans()[0]
	if len(span.Errors) != 1 || span.Errors[0].Err != wantErr {
		t.Fatalf("recorded errors = %#v, want original terminal error", span.Errors)
	}
	if span.Errors[0].Time.Before(before) || span.Errors[0].Time.After(after) {
		t.Fatalf("error time = %v, want between %v and %v", span.Errors[0].Time, before, after)
	}
	if !span.EndedAt.IsZero() {
		t.Fatalf("explicit end timestamp = %v, want none when EndedAt is zero", span.EndedAt)
	}
}

func TestOperationErrorsAreRedactedByDefault(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	operation.End(ctx, clientkit.OperationEndEvent{
		Outcome:      "transport_error",
		FailureClass: clientkit.FailureTransport,
		Err:          errors.New("https://user:secret@example.test/private?token=secret"),
	})

	span := telemetry.traces.spans()[0]
	if len(span.Errors) != 0 {
		t.Fatalf("recorded errors = %#v, want raw error details omitted", span.Errors)
	}
	if span.StatusCode != codes.Error || span.StatusDescription != "transport_error" {
		t.Fatalf("span status = (%v, %q), want classified failure", span.StatusCode, span.StatusDescription)
	}
}

func TestAttemptAndRetrySpanEvents(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	startedAt := time.Now().UTC()
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	telemetry.observer.ObserveAttempt(ctx, clientkit.AttemptEvent{
		Client:       "payments",
		Protocol:     "http",
		Operation:    "request",
		Number:       1,
		EndedAt:      startedAt.Add(time.Second),
		Outcome:      "transport_error",
		Succeeded:    true,
		FailureClass: clientkit.FailureTransport,
		Err:          errors.New("attempt detail must not be an exception"),
		Attributes:   []opskit.Attribute{{Key: "http.method", Value: "GET"}},
	})
	telemetry.observer.ObserveRetry(ctx, clientkit.RetryEvent{
		Client:       "payments",
		Protocol:     "http",
		Operation:    "request",
		AfterAttempt: 1,
		At:           startedAt.Add(2 * time.Second),
		Delay:        250 * time.Millisecond,
		Cause:        "transport_error",
		FailureClass: clientkit.FailureTransport,
	})
	telemetry.observer.ObserveAttempt(ctx, clientkit.AttemptEvent{
		Client:    "payments",
		Protocol:  "http",
		Operation: "request",
		Number:    2,
		EndedAt:   startedAt.Add(3 * time.Second),
		Outcome:   "success",
		Succeeded: true,
	})
	operation.End(ctx, clientkit.OperationEndEvent{Succeeded: true})

	events := telemetry.traces.spans()[0].Events
	if len(events) != 3 || events[0].Name != "clientkit.attempt" || events[1].Name != "clientkit.retry" || events[2].Name != "clientkit.attempt" {
		t.Fatalf("span events = %#v, want attempt, retry, successful attempt", events)
	}
	if !events[0].Time.Equal(startedAt.Add(time.Second)) || !events[1].Time.Equal(startedAt.Add(2*time.Second)) || !events[2].Time.Equal(startedAt.Add(3*time.Second)) {
		t.Fatalf("event times = (%v, %v, %v), want supplied completion times", events[0].Time, events[1].Time, events[2].Time)
	}
	attempt := events[0].Attributes
	if attributeValue(t, attempt, clientkitotel.AttributeAttemptNumber).AsInt64() != 1 ||
		attributeValue(t, attempt, clientkitotel.AttributeOutcome).AsString() != "transport_error" ||
		attributeValue(t, attempt, clientkitotel.AttributeSucceeded).AsBool() ||
		attributeValue(t, attempt, clientkitotel.AttributeFailureClass).AsString() != string(clientkit.FailureTransport) ||
		attributeValue(t, attempt, "http.method").AsString() != "GET" {
		t.Fatalf("attempt attributes = %#v, want normalized failure event", attempt)
	}
	retry := events[1].Attributes
	if attributeValue(t, retry, clientkitotel.AttributeRetryAfterAttempt).AsInt64() != 1 ||
		attributeValue(t, retry, clientkitotel.AttributeRetryCause).AsString() != "transport_error" ||
		attributeValue(t, retry, clientkitotel.AttributeRetryDelay).AsFloat64() != 0.25 {
		t.Fatalf("retry attributes = %#v, want complete retry event", retry)
	}
	success := events[2].Attributes
	if !attributeValue(t, success, clientkitotel.AttributeSucceeded).AsBool() ||
		attributeValue(t, success, clientkitotel.AttributeOutcome).AsString() != "success" {
		t.Fatalf("successful attempt attributes = %#v, want accepted attempt", success)
	}
	assertNoAttribute(t, success, clientkitotel.AttributeFailureClass)
}

func TestSpanEventsWithoutTimestampsUseObservationTime(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	before := time.Now()
	telemetry.observer.ObserveAttempt(ctx, clientkit.AttemptEvent{})
	telemetry.observer.ObserveRetry(ctx, clientkit.RetryEvent{})
	telemetry.observer.ObserveHealth(ctx, clientkit.HealthEvent{})
	after := time.Now()
	operation.End(ctx, clientkit.OperationEndEvent{Succeeded: true})

	events := telemetry.traces.spans()[0].Events
	if len(events) != 3 {
		t.Fatalf("span events = %d, want attempt, retry, and health", len(events))
	}
	for _, event := range events {
		if event.Time.Before(before) || event.Time.After(after) {
			t.Errorf("event %q time = %v, want between %v and %v", event.Name, event.Time, before, after)
		}
	}
}

func TestObserverSupportsConcurrentAttempts(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	const workers = 32
	var wait sync.WaitGroup
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			telemetry.observer.ObserveAttempt(ctx, clientkit.AttemptEvent{Number: worker + 1})
		}()
	}
	wait.Wait()
	operation.End(ctx, clientkit.OperationEndEvent{Succeeded: true})

	span := telemetry.traces.spans()[0]
	if len(span.Events) != workers || span.EndCount != 1 {
		t.Fatalf("span = %d events and %d ends, want %d and 1", len(span.Events), span.EndCount, workers)
	}
	if got := len(metricRecordsNamed(telemetry.metrics.records(), "clientkit.attempts")); got != workers {
		t.Fatalf("attempt metric records = %d, want %d", got, workers)
	}
}

func TestOperationObservationConcurrentEndRecordsOnce(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	const workers = 32
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			operation.End(ctx, clientkit.OperationEndEvent{Succeeded: true})
		}()
	}
	wait.Wait()

	if got := telemetry.traces.spans()[0].EndCount; got != 1 {
		t.Fatalf("span end count = %d, want 1", got)
	}
	if got := len(metricRecordsNamed(telemetry.metrics.records(), "clientkit.operations")); got != 1 {
		t.Fatalf("operation metric records = %d, want 1", got)
	}
}

func TestObserverSupportsIndependentConcurrentOperations(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	const workers = 32
	var wait sync.WaitGroup
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
			telemetry.observer.ObserveAttempt(ctx, clientkit.AttemptEvent{Number: worker + 1})
			operation.End(ctx, clientkit.OperationEndEvent{Succeeded: true})
		}()
	}
	wait.Wait()

	spans := telemetry.traces.spans()
	if len(spans) != workers {
		t.Fatalf("spans = %d, want %d", len(spans), workers)
	}
	for index, span := range spans {
		if span.EndCount != 1 || len(span.Events) != 1 {
			t.Errorf("span %d = %d events and %d ends, want 1 and 1", index, len(span.Events), span.EndCount)
		}
	}
	if got := len(metricRecordsNamed(telemetry.metrics.records(), "clientkit.operations")); got != workers {
		t.Fatalf("operation metric records = %d, want %d", got, workers)
	}
}

func TestEventAttributesCannotForgeFailureClass(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	forged := []opskit.Attribute{
		{Key: clientkitotel.AttributeFailureClass, Value: "forged"},
		{Key: "client.failure_class", Value: "legacy-forged"},
	}
	ctx, operation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{
		Attributes: forged,
	})
	operation.End(ctx, clientkit.OperationEndEvent{
		FailureClass: clientkit.FailureTLS,
		Attributes:   forged,
	})
	span := telemetry.traces.spans()[0]
	if got := attributeValue(t, span.Attributes, clientkitotel.AttributeFailureClass).AsString(); got != string(clientkit.FailureTLS) {
		t.Fatalf("failure class = %q, want authoritative TLS class", got)
	}
	for _, value := range span.Attributes {
		if value.Value.Type() == attribute.STRING && strings.Contains(value.Value.AsString(), "forged") {
			t.Fatalf("span exposed forged failure class through %q", value.Key)
		}
	}
	metric := onlyMetricRecord(t, telemetry.metrics.records(), "clientkit.operations")
	if got := metricAttributeValue(t, metric, clientkitotel.AttributeFailureClass).AsString(); got != string(clientkit.FailureTLS) {
		t.Fatalf("metric failure class = %q, want authoritative TLS class", got)
	}
	assertNoMetricAttribute(t, metric, "client.failure_class")
	for _, value := range metric.attributes.ToSlice() {
		if value.Value.Type() == attribute.STRING && strings.Contains(value.Value.AsString(), "forged") {
			t.Fatalf("metric exposed forged failure class through %q", value.Key)
		}
	}
}
