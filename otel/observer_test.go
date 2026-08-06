package otel_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
)

func TestObserverPreservesLifecycleEventOrder(t *testing.T) {
	telemetry := newTelemetryFixture(t)
	startedAt := time.Unix(1_700_000_000, 0).UTC()

	ctx, observation := telemetry.observer.StartOperation(context.Background(), clientkit.OperationStartEvent{
		Client:    "payments",
		Protocol:  "http",
		Operation: "request",
		StartedAt: startedAt,
	})
	firstErr := errors.New("first attempt failed")
	telemetry.observer.ObserveAttempt(ctx, clientkit.AttemptEvent{
		Client:       "payments",
		Protocol:     "http",
		Operation:    "request",
		Number:       1,
		StartedAt:    startedAt,
		EndedAt:      startedAt.Add(time.Second),
		Duration:     time.Second,
		Outcome:      "transport_error",
		FailureClass: clientkit.FailureTransport,
		Err:          firstErr,
	})
	telemetry.observer.ObserveRetry(ctx, clientkit.RetryEvent{
		Client:       "payments",
		Protocol:     "http",
		Operation:    "request",
		AfterAttempt: 1,
		At:           startedAt.Add(2 * time.Second),
		Delay:        time.Second,
		Cause:        "transport_error",
		FailureClass: clientkit.FailureTransport,
	})
	terminalErr := errors.New("second attempt failed")
	telemetry.observer.ObserveAttempt(ctx, clientkit.AttemptEvent{
		Client:       "payments",
		Protocol:     "http",
		Operation:    "request",
		Number:       2,
		StartedAt:    startedAt.Add(2 * time.Second),
		EndedAt:      startedAt.Add(3 * time.Second),
		Duration:     time.Second,
		Outcome:      "transport_error",
		FailureClass: clientkit.FailureTransport,
		Err:          terminalErr,
	})
	endEvent := clientkit.OperationEndEvent{
		Client:       "payments",
		Protocol:     "http",
		Operation:    "request",
		StartedAt:    startedAt,
		EndedAt:      startedAt.Add(4 * time.Second),
		Duration:     4 * time.Second,
		Attempts:     2,
		Outcome:      "transport_error",
		FailureClass: clientkit.FailureTransport,
		Err:          terminalErr,
	}
	observation.End(ctx, endEvent)
	observation.End(ctx, endEvent)

	spans := telemetry.traces.spans()
	if len(spans) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(spans))
	}
	if spans[0].EndCount != 1 {
		t.Fatalf("span end count = %d, want 1", spans[0].EndCount)
	}

	events := spans[0].Events
	gotNames := make([]string, 0, len(events))
	for _, event := range events {
		gotNames = append(gotNames, event.Name)
	}
	// Attempt and retry events contain bounded classifications; raw errors are
	// intentionally absent unless the caller explicitly opts into them.
	wantNames := []string{"clientkit.attempt", "clientkit.retry", "clientkit.attempt"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("span events = %v, want %v", gotNames, wantNames)
	}

	wantTimes := []time.Time{
		startedAt.Add(time.Second),
		startedAt.Add(2 * time.Second),
		startedAt.Add(3 * time.Second),
	}
	for index, event := range events {
		if !event.Time.Equal(wantTimes[index]) {
			t.Errorf("event %q time = %s, want %s", event.Name, event.Time, wantTimes[index])
		}
	}
}
