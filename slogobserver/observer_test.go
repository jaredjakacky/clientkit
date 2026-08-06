package slogobserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/slogobserver"
	"github.com/jaredjakacky/opskit"
)

func TestNewDoesNotLogAndIgnoresNilOptions(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug), nil)
	if observer == nil {
		t.Fatal("New() returned nil")
	}
	if records, _ := store.snapshot(); len(records) != 0 {
		t.Fatalf("New() emitted %d records, want 0", len(records))
	}
}

func TestNewUsesDefaultLoggerWhenLoggerIsNil(t *testing.T) {
	// This test changes process-global slog state and must not run in parallel.
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	store := &recordStore{}
	slog.SetDefault(testLogger(store, slog.LevelDebug))

	observer := slogobserver.New(nil)
	observer.ObserveRetry(context.Background(), clientkit.RetryEvent{})
	if got := attributeValue(t, clientkitAttributes(t, onlyRecord(t, store)), "event").String(); got != "retry_scheduled" {
		t.Fatalf("event = %v, want retry_scheduled through default logger", got)
	}
}

func TestOperationObservationUsesStartFallbackAndEndsOnce(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug))
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("key"), "value")
	endedAt := time.Date(2026, time.August, 6, 12, 0, 0, 123, time.UTC)
	startAttributes := []opskit.Attribute{{Key: "method", Value: "GET"}}
	next, operation := observer.StartOperation(ctx, clientkit.OperationStartEvent{
		Client:     "payments",
		Protocol:   "http",
		Operation:  "request",
		Attributes: startAttributes,
	})
	startAttributes[0].Value = "MUTATED"
	if next.Value(contextKey("key")) != "value" {
		t.Fatal("StartOperation() did not preserve context")
	}
	if records, _ := store.snapshot(); len(records) != 0 {
		t.Fatalf("StartOperation() emitted %d records, want 0", len(records))
	}

	operation.End(ctx, clientkit.OperationEndEvent{
		EndedAt:   endedAt,
		Duration:  25 * time.Millisecond,
		Attempts:  2,
		Outcome:   "success",
		Succeeded: true,
	})
	// A second completion must not emit another terminal record.
	operation.End(ctx, clientkit.OperationEndEvent{EndedAt: endedAt, Outcome: "failure"})

	record := onlyRecord(t, store)
	if !record.time.Equal(endedAt) || record.level != slog.LevelDebug || record.message != "clientkit operation completed" {
		t.Fatalf("record envelope = %#v, want supplied time and successful operation level", record)
	}
	group := clientkitAttributes(t, record)
	if attributeValue(t, group, "event").String() != "operation_completed" ||
		attributeValue(t, group, "client").String() != "payments" ||
		attributeValue(t, group, "protocol").String() != "http" ||
		attributeValue(t, group, "operation").String() != "request" {
		t.Fatalf("operation identity = %#v, want start-event fallback", group)
	}
	if attributeValue(t, group, "outcome").String() != "success" ||
		!attributeValue(t, group, "succeeded").Bool() ||
		attributeValue(t, group, "attempts").Int64() != 2 ||
		attributeValue(t, group, "duration").Duration() != 25*time.Millisecond {
		t.Fatalf("operation result = %#v, want completed success", group)
	}
	if attributes := nestedAttributes(t, group, "attributes"); attributeValue(t, attributes, "method").String() != "GET" {
		t.Fatalf("operation attributes = %#v, want start attributes", attributes)
	}
}

func TestOperationEndMetadataOverridesStartMetadata(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug))
	_, operation := observer.StartOperation(context.Background(), clientkit.OperationStartEvent{
		Client:     "start-client",
		Protocol:   "start-protocol",
		Operation:  "start-operation",
		Attributes: []opskit.Attribute{{Key: "phase", Value: "start"}},
	})
	operation.End(context.Background(), clientkit.OperationEndEvent{
		Client:     "end-client",
		Protocol:   "end-protocol",
		Operation:  "end-operation",
		Succeeded:  true,
		Attributes: []opskit.Attribute{{Key: "phase", Value: "end"}},
	})

	group := clientkitAttributes(t, onlyRecord(t, store))
	if attributeValue(t, group, "client").String() != "end-client" ||
		attributeValue(t, group, "protocol").String() != "end-protocol" ||
		attributeValue(t, group, "operation").String() != "end-operation" {
		t.Fatalf("operation identity = %#v, want end-event values", group)
	}
	attributes := nestedAttributes(t, group, "attributes")
	if len(attributes) != 1 || attributeValue(t, attributes, "phase").String() != "end" {
		t.Fatalf("operation attributes = %#v, want replacement end attributes", attributes)
	}
}

func TestContradictoryOperationSuccessIsLoggedAsFailure(t *testing.T) {
	tests := []struct {
		name  string
		event clientkit.OperationEndEvent
	}{
		{name: "not declared successful", event: clientkit.OperationEndEvent{}},
		{name: "failure class", event: clientkit.OperationEndEvent{Succeeded: true, FailureClass: clientkit.FailurePolicy}},
		{name: "terminal error", event: clientkit.OperationEndEvent{Succeeded: true, Err: errors.New("failed")}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordStore{}
			observer := slogobserver.New(testLogger(store, slog.LevelDebug))
			_, operation := observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
			operation.End(context.Background(), test.event)
			record := onlyRecord(t, store)
			if record.level != slog.LevelError || attributeValue(t, clientkitAttributes(t, record), "succeeded").Bool() {
				t.Fatalf("record = %#v, want normalized operation failure", record)
			}
		})
	}
}

func TestAttemptRecordAndErrorDetailPolicy(t *testing.T) {
	wantErr := errors.New("dial payments.internal: secret detail")
	event := clientkit.AttemptEvent{
		Client:       "payments",
		Protocol:     "tcp",
		Operation:    "dial",
		Number:       3,
		EndedAt:      time.Now().UTC(),
		Duration:     15 * time.Millisecond,
		Outcome:      "dial_error",
		Succeeded:    true,
		FailureClass: clientkit.FailureTransport,
		Err:          wantErr,
		Attributes:   []opskit.Attribute{{Key: "network", Value: "tcp"}},
	}

	for _, test := range []struct {
		name      string
		option    slogobserver.Option
		wantError bool
	}{
		{name: "safe by default"},
		{name: "details explicitly enabled", option: slogobserver.WithErrorDetails(), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &recordStore{}
			observer := slogobserver.New(testLogger(store, slog.LevelDebug), test.option)
			observer.ObserveAttempt(context.Background(), event)
			record := onlyRecord(t, store)
			group := clientkitAttributes(t, record)
			if !record.time.Equal(event.EndedAt) {
				t.Fatalf("attempt record time = %v, want %v", record.time, event.EndedAt)
			}
			if attributeValue(t, group, "event").String() != "attempt_completed" ||
				attributeValue(t, group, "client").String() != "payments" ||
				attributeValue(t, group, "attempt").Int64() != 3 {
				t.Fatalf("attempt identity = %#v, want completed third attempt", group)
			}
			if attributeValue(t, group, "succeeded").Bool() ||
				attributeValue(t, group, "failure_class").String() != string(clientkit.FailureTransport) ||
				attributeValue(t, group, "duration").Duration() != 15*time.Millisecond {
				t.Fatalf("attempt result = %#v, want normalized transport failure", group)
			}
			errorValue, hasError := optionalAttribute(group, "error")
			if hasError != test.wantError {
				t.Fatalf("error attribute present = %t, want %t: %#v", hasError, test.wantError, group)
			}
			if test.wantError && errorValue.Any() != wantErr {
				t.Fatalf("error attribute = %#v, want original error", errorValue.Any())
			}
		})
	}
}

func TestSuccessfulAttemptRecord(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug))
	observer.ObserveAttempt(context.Background(), clientkit.AttemptEvent{
		Number:    1,
		Outcome:   "success",
		Succeeded: true,
	})

	group := clientkitAttributes(t, onlyRecord(t, store))
	if !attributeValue(t, group, "succeeded").Bool() ||
		attributeValue(t, group, "outcome").String() != "success" ||
		attributeValue(t, group, "attempt").Int64() != 1 {
		t.Fatalf("attempt result = %#v, want successful first attempt", group)
	}
	if _, exists := optionalAttribute(group, "failure_class"); exists {
		t.Fatalf("successful attempt contains failure class: %#v", group)
	}
}

func TestOperationErrorDetailsAreOptIn(t *testing.T) {
	wantErr := errors.New("certificate for internal.example")
	for _, test := range []struct {
		name      string
		option    slogobserver.Option
		wantError bool
	}{
		{name: "omitted"},
		{name: "included", option: slogobserver.WithErrorDetails(), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &recordStore{}
			observer := slogobserver.New(testLogger(store, slog.LevelDebug), test.option)
			_, operation := observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
			operation.End(context.Background(), clientkit.OperationEndEvent{Err: wantErr})
			group := clientkitAttributes(t, onlyRecord(t, store))
			errorValue, hasError := optionalAttribute(group, "error")
			if hasError != test.wantError {
				t.Fatalf("error attribute present = %t, want %t", hasError, test.wantError)
			}
			if test.wantError && errorValue.Any() != wantErr {
				t.Fatalf("error attribute = %#v, want original error", errorValue.Any())
			}
		})
	}
}

func TestRetryRecord(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug))
	at := time.Date(2026, time.August, 6, 13, 0, 0, 0, time.UTC)
	observer.ObserveRetry(context.Background(), clientkit.RetryEvent{
		Client:       "payments",
		Protocol:     "http",
		Operation:    "request",
		AfterAttempt: 2,
		At:           at,
		Delay:        750 * time.Millisecond,
		Cause:        "server_error",
		FailureClass: clientkit.FailureRemoteResponse,
	})

	record := onlyRecord(t, store)
	group := clientkitAttributes(t, record)
	if !record.time.Equal(at) || record.level != slog.LevelWarn || record.message != "clientkit retry scheduled" {
		t.Fatalf("retry envelope = %#v, want supplied time and warning level", record)
	}
	if attributeValue(t, group, "event").String() != "retry_scheduled" ||
		attributeValue(t, group, "client").String() != "payments" ||
		attributeValue(t, group, "protocol").String() != "http" ||
		attributeValue(t, group, "operation").String() != "request" {
		t.Fatalf("retry identity = %#v, want complete operation identity", group)
	}
	if attributeValue(t, group, "after_attempt").Int64() != 2 ||
		attributeValue(t, group, "cause").String() != "server_error" ||
		attributeValue(t, group, "failure_class").String() != string(clientkit.FailureRemoteResponse) ||
		attributeValue(t, group, "delay").Duration() != 750*time.Millisecond {
		t.Fatalf("retry result = %#v, want complete retry metadata", group)
	}
}

func TestHealthRecordLevelsAndFields(t *testing.T) {
	tests := []struct {
		state        clientkit.HealthState
		level        slog.Level
		failureClass clientkit.FailureClass
	}{
		{state: clientkit.HealthHealthy, level: slog.LevelDebug},
		{state: clientkit.HealthDegraded, level: slog.LevelWarn, failureClass: clientkit.FailureRemoteResponse},
		{state: clientkit.HealthUnhealthy, level: slog.LevelWarn, failureClass: clientkit.FailureRemoteResponse},
		{state: clientkit.HealthUnknown, level: slog.LevelWarn, failureClass: clientkit.FailureRemoteResponse},
	}

	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			store := &recordStore{}
			observer := slogobserver.New(testLogger(store, slog.LevelDebug))
			checkedAt := time.Now().UTC()
			observer.ObserveHealth(context.Background(), clientkit.HealthEvent{
				Client:       "payments",
				Protocol:     "http",
				State:        test.state,
				FailureClass: test.failureClass,
				CheckedAt:    checkedAt,
				Duration:     5 * time.Millisecond,
				Message:      "dependency status",
			})
			record := onlyRecord(t, store)
			group := clientkitAttributes(t, record)
			if !record.time.Equal(checkedAt) || record.level != test.level || record.message != "clientkit health check completed" {
				t.Fatalf("health envelope = %#v, want %s", record, test.level)
			}
			if attributeValue(t, group, "event").String() != "health_check_completed" ||
				attributeValue(t, group, "client").String() != "payments" ||
				attributeValue(t, group, "protocol").String() != "http" ||
				attributeValue(t, group, "health_state").String() != string(test.state) {
				t.Fatalf("health identity = %#v, want complete health identity", group)
			}
			if attributeValue(t, group, "message").String() != "dependency status" ||
				attributeValue(t, group, "duration").Duration() != 5*time.Millisecond {
				t.Fatalf("health fields = %#v, want complete health result", group)
			}
			failure, exists := optionalAttribute(group, "failure_class")
			if test.failureClass == clientkit.FailureNone && exists {
				t.Fatalf("healthy record contains failure class: %#v", group)
			}
			if test.failureClass != clientkit.FailureNone && (!exists || failure.String() != string(test.failureClass)) {
				t.Fatalf("failure class = %v, want %q", failure, test.failureClass)
			}
		})
	}
}

func TestOptionalFieldsAreOmitted(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug))
	observer.ObserveAttempt(context.Background(), clientkit.AttemptEvent{Duration: -time.Second})

	group := clientkitAttributes(t, onlyRecord(t, store))
	for _, key := range []string{"client", "protocol", "operation", "outcome", "failure_class", "attempt", "duration", "error", "attributes"} {
		if _, exists := optionalAttribute(group, key); exists {
			t.Errorf("optional attribute %q unexpectedly present in %#v", key, group)
		}
	}
}

func TestHandlerFiltering(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelError))
	observer.ObserveAttempt(context.Background(), clientkit.AttemptEvent{})
	observer.ObserveRetry(context.Background(), clientkit.RetryEvent{})
	observer.ObserveHealth(context.Background(), clientkit.HealthEvent{State: clientkit.HealthHealthy})
	_, operation := observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	operation.End(context.Background(), clientkit.OperationEndEvent{})

	record := onlyRecord(t, store)
	if got := attributeValue(t, clientkitAttributes(t, record), "event").String(); got != "operation_completed" {
		t.Fatalf("event = %q, want only error-level operation record", got)
	}
}

func TestZeroTimestampUsesCurrentTime(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug))
	before := time.Now().UTC()
	observer.ObserveRetry(context.Background(), clientkit.RetryEvent{})
	after := time.Now().UTC()

	loggedAt := onlyRecord(t, store).time
	if loggedAt.Before(before) || loggedAt.After(after) {
		t.Fatalf("fallback record time = %v, want between %v and %v", loggedAt, before, after)
	}
}

func TestObserverSupportsConcurrentUse(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug))

	const workers = 32
	var wait sync.WaitGroup
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			observer.ObserveAttempt(context.Background(), clientkit.AttemptEvent{Number: worker + 1})
		}()
	}
	wait.Wait()

	records, _ := store.snapshot()
	if len(records) != workers {
		t.Fatalf("log records = %d, want %d attempts", len(records), workers)
	}
	for _, record := range records {
		if got := attributeValue(t, clientkitAttributes(t, record), "event").String(); got != "attempt_completed" {
			t.Fatalf("event = %q, want attempt_completed", got)
		}
	}
}

func TestOperationObservationConcurrentEndLogsOnce(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug))
	_, operation := observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})

	const workers = 32
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			operation.End(context.Background(), clientkit.OperationEndEvent{Succeeded: true})
		}()
	}
	wait.Wait()

	if got := attributeValue(t, clientkitAttributes(t, onlyRecord(t, store)), "event").String(); got != "operation_completed" {
		t.Fatalf("event = %q, want operation_completed", got)
	}
}

func TestObserverForwardsContextAndIgnoresHandlerErrors(t *testing.T) {
	store := &recordStore{handleError: errors.New("handler failed")}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug))
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("request"), "request-42")

	observer.ObserveRetry(ctx, clientkit.RetryEvent{})
	records, enabledContexts := store.snapshot()
	if len(records) != 1 || records[0].context.Value(contextKey("request")) != "request-42" {
		t.Fatalf("handled contexts = %#v, want supplied context", records)
	}
	if len(enabledContexts) == 0 || enabledContexts[len(enabledContexts)-1].Value(contextKey("request")) != "request-42" {
		t.Fatalf("enabled contexts = %#v, want supplied context", enabledContexts)
	}
}

func TestObserverPreservesLoggerAttributes(t *testing.T) {
	store := &recordStore{}
	logger := testLogger(store, slog.LevelDebug).With("service", "checkout")
	observer := slogobserver.New(logger)
	observer.ObserveRetry(context.Background(), clientkit.RetryEvent{})

	if got := attributeValue(t, onlyRecord(t, store).attributes, "service").String(); got != "checkout" {
		t.Fatalf("service = %q, want logger-bound attribute", got)
	}
}

func TestObserverProducesJSONHandlerCompatibleRecords(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	observer := slogobserver.New(logger)
	observer.ObserveRetry(context.Background(), clientkit.RetryEvent{Client: "payments"})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode JSON handler record: %v", err)
	}
	group, ok := record["clientkit"].(map[string]any)
	if !ok || group["event"] != "retry_scheduled" || group["client"] != "payments" {
		t.Fatalf("JSON record = %#v, want nested Clientkit retry", record)
	}
}

var _ clientkit.Observer = (*slogobserver.Observer)(nil)
