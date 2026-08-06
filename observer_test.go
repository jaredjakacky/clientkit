package clientkit_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/opskit"
)

type observerCallbacks struct {
	start   func(context.Context, clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation)
	attempt func(context.Context, clientkit.AttemptEvent)
	retry   func(context.Context, clientkit.RetryEvent)
	health  func(context.Context, clientkit.HealthEvent)
}

func (o observerCallbacks) StartOperation(ctx context.Context, event clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	if o.start == nil {
		return ctx, nil
	}
	return o.start(ctx, event)
}

func (o observerCallbacks) ObserveAttempt(ctx context.Context, event clientkit.AttemptEvent) {
	if o.attempt != nil {
		o.attempt(ctx, event)
	}
}

func (o observerCallbacks) ObserveRetry(ctx context.Context, event clientkit.RetryEvent) {
	if o.retry != nil {
		o.retry(ctx, event)
	}
}

func (o observerCallbacks) ObserveHealth(ctx context.Context, event clientkit.HealthEvent) {
	if o.health != nil {
		o.health(ctx, event)
	}
}

func TestNopObserverAndOperationObservationFunc(t *testing.T) {
	ctx := context.Background()
	nop := clientkit.NopObserver{}
	next, observation := nop.StartOperation(ctx, clientkit.OperationStartEvent{})
	if next != ctx {
		t.Fatal("NopObserver changed the operation context")
	}
	observation.End(ctx, clientkit.OperationEndEvent{})
	nop.ObserveAttempt(ctx, clientkit.AttemptEvent{})
	nop.ObserveRetry(ctx, clientkit.RetryEvent{})
	nop.ObserveHealth(ctx, clientkit.HealthEvent{})

	called := false
	clientkit.OperationObservationFunc(func(got context.Context, event clientkit.OperationEndEvent) {
		called = got == ctx && event.Operation == "request"
	}).End(ctx, clientkit.OperationEndEvent{Operation: "request"})
	if !called {
		t.Fatal("OperationObservationFunc did not receive its arguments")
	}
	clientkit.OperationObservationFunc(nil).End(ctx, clientkit.OperationEndEvent{})
}

func TestSafeObserverContainsPanicsAndClonesAttributes(t *testing.T) {
	ctx := context.Background()
	startAttributes := []opskit.Attribute{opskit.Attr("key", "start")}
	endAttributes := []opskit.Attribute{opskit.Attr("key", "end")}
	attemptAttributes := []opskit.Attribute{opskit.Attr("key", "attempt")}
	retryAttributes := []opskit.Attribute{opskit.Attr("key", "retry")}
	healthAttributes := []opskit.Attribute{opskit.Attr("key", "health")}

	observer := clientkit.SafeObserver(observerCallbacks{
		start: func(_ context.Context, event clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
			event.Attributes[0].Value = "changed"
			return nil, clientkit.OperationObservationFunc(func(_ context.Context, event clientkit.OperationEndEvent) {
				event.Attributes[0].Value = "changed"
				panic("end")
			})
		},
		attempt: func(_ context.Context, event clientkit.AttemptEvent) {
			event.Attributes[0].Value = "changed"
			panic("attempt")
		},
		retry: func(_ context.Context, event clientkit.RetryEvent) {
			event.Attributes[0].Value = "changed"
			panic("retry")
		},
		health: func(_ context.Context, event clientkit.HealthEvent) {
			event.Attributes[0].Value = "changed"
			panic("health")
		},
	})

	next, observation := observer.StartOperation(ctx, clientkit.OperationStartEvent{Attributes: startAttributes})
	if next != ctx {
		t.Fatal("nil observer context did not preserve the incoming context")
	}
	observation.End(ctx, clientkit.OperationEndEvent{Attributes: endAttributes})
	observer.ObserveAttempt(ctx, clientkit.AttemptEvent{Attributes: attemptAttributes})
	observer.ObserveRetry(ctx, clientkit.RetryEvent{Attributes: retryAttributes})
	observer.ObserveHealth(ctx, clientkit.HealthEvent{Attributes: healthAttributes})

	for name, attributes := range map[string][]opskit.Attribute{
		"start":   startAttributes,
		"end":     endAttributes,
		"attempt": attemptAttributes,
		"retry":   retryAttributes,
		"health":  healthAttributes,
	} {
		if attributes[0].Value != name {
			t.Errorf("%s attributes were mutated: %#v", name, attributes)
		}
	}
}

func TestSafeObserverContainsStartPanic(t *testing.T) {
	ctx := context.Background()
	observer := clientkit.SafeObserver(observerCallbacks{
		start: func(context.Context, clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
			panic("start")
		},
	})
	next, observation := observer.StartOperation(ctx, clientkit.OperationStartEvent{})
	if next != ctx {
		t.Fatal("StartOperation panic did not preserve the incoming context")
	}
	observation.End(ctx, clientkit.OperationEndEvent{})
}

func TestSafeObserverNilUsesNoOp(t *testing.T) {
	ctx := context.Background()
	observer := clientkit.SafeObserver(nil)
	next, observation := observer.StartOperation(ctx, clientkit.OperationStartEvent{})
	if next != ctx {
		t.Fatal("SafeObserver(nil) changed context")
	}
	observation.End(ctx, clientkit.OperationEndEvent{})
	observer.ObserveAttempt(ctx, clientkit.AttemptEvent{})
	observer.ObserveRetry(ctx, clientkit.RetryEvent{})
	observer.ObserveHealth(ctx, clientkit.HealthEvent{})
}

func TestSafeObserverWrapsExternalNopObserverEmbedding(t *testing.T) {
	called := false
	observer := clientkit.SafeObserver(embeddedNopObserver{called: &called})
	observer.ObserveHealth(context.Background(), clientkit.HealthEvent{})
	if !called {
		t.Fatal("embedded observer callback was not invoked")
	}
}

func TestSafeObserverForwardsCompleteEventsAndDerivedContext(t *testing.T) {
	startedAt := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(250 * time.Millisecond)
	terminalErr := errors.New("terminal")
	attributes := []opskit.Attribute{opskit.Attr("key", "value")}
	startEvent := clientkit.OperationStartEvent{
		Client: "payments", Protocol: "tcp", Operation: "dial",
		StartedAt: startedAt, Attributes: attributes,
	}
	endEvent := clientkit.OperationEndEvent{
		Client: "payments", Protocol: "tcp", Operation: "dial",
		StartedAt: startedAt, EndedAt: endedAt, Duration: 250 * time.Millisecond,
		Attempts: 2, Outcome: "success", Succeeded: true, Attributes: attributes,
	}
	attemptEvent := clientkit.AttemptEvent{
		Client: "payments", Protocol: "tcp", Operation: "dial", Number: 2,
		StartedAt: startedAt, EndedAt: endedAt, Duration: 250 * time.Millisecond,
		Outcome: "failed", FailureClass: clientkit.FailureTransport,
		Err: terminalErr, Attributes: attributes,
	}
	retryEvent := clientkit.RetryEvent{
		Client: "payments", Protocol: "tcp", Operation: "dial", AfterAttempt: 1,
		At: endedAt, Delay: time.Second, Cause: "transport_error",
		FailureClass: clientkit.FailureTransport, Attributes: attributes,
	}
	healthEvent := clientkit.HealthEvent{
		Client: "payments", Protocol: "tcp", State: clientkit.HealthUnhealthy,
		FailureClass: clientkit.FailureTransport, CheckedAt: endedAt,
		Duration: 250 * time.Millisecond, Message: "unavailable", Attributes: attributes,
	}
	assertEvent := func(name string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s event = %#v, want %#v", name, got, want)
		}
	}

	observer := clientkit.SafeObserver(observerCallbacks{
		start: func(ctx context.Context, event clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
			assertEvent("start", event, startEvent)
			return context.WithValue(ctx, observerContextKey("derived"), true), clientkit.OperationObservationFunc(func(_ context.Context, event clientkit.OperationEndEvent) {
				assertEvent("end", event, endEvent)
			})
		},
		attempt: func(_ context.Context, event clientkit.AttemptEvent) { assertEvent("attempt", event, attemptEvent) },
		retry:   func(_ context.Context, event clientkit.RetryEvent) { assertEvent("retry", event, retryEvent) },
		health:  func(_ context.Context, event clientkit.HealthEvent) { assertEvent("health", event, healthEvent) },
	})
	ctx, observation := observer.StartOperation(context.Background(), startEvent)
	if ctx.Value(observerContextKey("derived")) != true {
		t.Fatal("StartOperation() did not return the observer's derived context")
	}
	observer.ObserveAttempt(ctx, attemptEvent)
	observer.ObserveRetry(ctx, retryEvent)
	observer.ObserveHealth(ctx, healthEvent)
	observation.End(ctx, endEvent)
}

func TestMultiObserverOrdersCallbacksAndIsolatesObservers(t *testing.T) {
	events := make([]string, 0, 12)
	first := observerCallbacks{
		start: func(ctx context.Context, event clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
			events = append(events, "start-first")
			event.Attributes[0].Value = "mutated"
			return context.WithValue(ctx, observerContextKey("first"), true), clientkit.OperationObservationFunc(func(_ context.Context, event clientkit.OperationEndEvent) {
				events = append(events, "end-first")
				if event.Attributes[0].Value != "original" {
					t.Error("end attributes leaked mutation between observers")
				}
			})
		},
		attempt: func(_ context.Context, event clientkit.AttemptEvent) {
			events = append(events, "attempt-first")
			event.Attributes[0].Value = "mutated"
		},
		retry: func(_ context.Context, event clientkit.RetryEvent) {
			events = append(events, "retry-first")
			event.Attributes[0].Value = "mutated"
		},
		health: func(_ context.Context, event clientkit.HealthEvent) {
			events = append(events, "health-first")
			event.Attributes[0].Value = "mutated"
		},
	}
	panicking := observerCallbacks{
		start: func(context.Context, clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
			events = append(events, "start-panicking")
			panic("start")
		},
		attempt: func(context.Context, clientkit.AttemptEvent) {
			events = append(events, "attempt-panicking")
			panic("attempt")
		},
		retry: func(context.Context, clientkit.RetryEvent) {
			events = append(events, "retry-panicking")
			panic("retry")
		},
		health: func(context.Context, clientkit.HealthEvent) {
			events = append(events, "health-panicking")
			panic("health")
		},
	}
	last := observerCallbacks{
		start: func(ctx context.Context, event clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
			events = append(events, "start-last")
			if ctx.Value(observerContextKey("first")) != true {
				t.Error("derived context was not chained to the next observer")
			}
			if event.Attributes[0].Value != "original" {
				t.Error("start attributes leaked mutation between observers")
			}
			return ctx, clientkit.OperationObservationFunc(func(_ context.Context, event clientkit.OperationEndEvent) {
				events = append(events, "end-last")
				event.Attributes[0].Value = "mutated"
			})
		},
		attempt: func(_ context.Context, event clientkit.AttemptEvent) {
			events = append(events, "attempt-last")
			if event.Attributes[0].Value != "original" {
				t.Error("attempt attributes leaked mutation between observers")
			}
		},
		retry: func(_ context.Context, event clientkit.RetryEvent) {
			events = append(events, "retry-last")
			if event.Attributes[0].Value != "original" {
				t.Error("retry attributes leaked mutation between observers")
			}
		},
		health: func(_ context.Context, event clientkit.HealthEvent) {
			events = append(events, "health-last")
			if event.Attributes[0].Value != "original" {
				t.Error("health attributes leaked mutation between observers")
			}
		},
	}

	observer := clientkit.MultiObserver(nil, first, panicking, last)
	attributes := []opskit.Attribute{opskit.Attr("key", "original")}
	ctx, observation := observer.StartOperation(context.Background(), clientkit.OperationStartEvent{Attributes: attributes})
	observer.ObserveAttempt(ctx, clientkit.AttemptEvent{Attributes: attributes})
	observer.ObserveRetry(ctx, clientkit.RetryEvent{Attributes: attributes})
	observer.ObserveHealth(ctx, clientkit.HealthEvent{Attributes: attributes})
	observation.End(ctx, clientkit.OperationEndEvent{Attributes: attributes})

	want := []string{
		"start-first", "start-panicking", "start-last",
		"attempt-first", "attempt-panicking", "attempt-last",
		"retry-first", "retry-panicking", "retry-last",
		"health-first", "health-panicking", "health-last",
		"end-last", "end-first",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("callback order = %v, want %v", events, want)
	}
	if attributes[0].Value != "original" {
		t.Fatalf("caller attributes were mutated: %#v", attributes)
	}
}

func TestMultiObserverContainsEndPanicAndCompletesReverseTeardown(t *testing.T) {
	ends := make([]string, 0, 3)
	observerWithEnd := func(name string, panicOnEnd bool) clientkit.Observer {
		return observerCallbacks{
			start: func(ctx context.Context, _ clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
				return ctx, clientkit.OperationObservationFunc(func(context.Context, clientkit.OperationEndEvent) {
					ends = append(ends, name)
					if panicOnEnd {
						panic("end")
					}
				})
			},
		}
	}

	observer := clientkit.MultiObserver(
		observerWithEnd("first", false),
		observerWithEnd("panicking", true),
		observerWithEnd("last", false),
	)
	ctx, observation := observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})

	// Reverse teardown mirrors nested resources: later observations finish first,
	// while one broken observer must not prevent the remaining cleanup.
	observation.End(ctx, clientkit.OperationEndEvent{})

	want := []string{"last", "panicking", "first"}
	if !reflect.DeepEqual(ends, want) {
		t.Fatalf("end callback order = %v, want %v", ends, want)
	}
}

func TestMultiObserverEmptyAndSingle(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		observer clientkit.Observer
	}{
		{name: "empty", observer: clientkit.MultiObserver()},
		{name: "nil", observer: clientkit.MultiObserver(nil)},
		{name: "single", observer: clientkit.MultiObserver(observerCallbacks{})},
		{name: "multiple without observations", observer: clientkit.MultiObserver(observerCallbacks{}, observerCallbacks{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next, observation := test.observer.StartOperation(ctx, clientkit.OperationStartEvent{})
			if next != ctx {
				t.Fatal("observer changed context")
			}
			observation.End(ctx, clientkit.OperationEndEvent{})
		})
	}
}

type embeddedNopObserver struct {
	clientkit.NopObserver
	called *bool
}

func (o embeddedNopObserver) ObserveHealth(context.Context, clientkit.HealthEvent) {
	*o.called = true
	panic("observer panic must be contained")
}

type observerContextKey string

var _ clientkit.Observer = observerCallbacks{}
var _ clientkit.Observer = embeddedNopObserver{}
