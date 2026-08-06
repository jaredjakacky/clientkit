package healthrecord_test

import (
	"context"
	"sync"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/internal/healthrecord"
	"github.com/jaredjakacky/opskit"
)

func TestRecordWithoutClientReturnsCompletedHealth(t *testing.T) {
	assessment := clientkit.HealthAssessment{
		State:        clientkit.HealthUnhealthy,
		FailureClass: clientkit.FailureConnectionRefused,
		Message:      "connection refused",
	}
	startedAt := time.Now()
	earliestCompletion := time.Now()
	health := healthrecord.Record(nil, nil, "tcp", assessment, startedAt, nil)
	latestCompletion := time.Now()

	if health.State != assessment.State || health.FailureClass != assessment.FailureClass || health.Message != assessment.Message {
		t.Fatalf("Record() = %#v, want assessment fields preserved", health)
	}
	if health.CheckedAt.Location() != time.UTC || health.CheckedAt.Before(earliestCompletion.UTC()) || health.CheckedAt.After(latestCompletion.UTC()) {
		t.Fatalf("CheckedAt = %v, want UTC completion between %v and %v", health.CheckedAt, earliestCompletion.UTC(), latestCompletion.UTC())
	}
	minimumDuration := earliestCompletion.Sub(startedAt)
	maximumDuration := latestCompletion.Sub(startedAt)
	if health.Duration < minimumDuration || health.Duration > maximumDuration {
		t.Fatalf("Duration = %v, want between %v and %v", health.Duration, minimumDuration, maximumDuration)
	}
}

func TestRecordCachesSanitizedHealthAndEmitsIt(t *testing.T) {
	client, observer := newRecordingClient(t)

	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("request"), "request-42")
	attributes := []opskit.Attribute{{Key: "client.operation", Value: "health_check"}}
	health := healthrecord.Record(client, ctx, "http", clientkit.HealthAssessment{
		State:        clientkit.HealthHealthy,
		FailureClass: clientkit.FailureTransport,
		Message:      "ready\n",
	}, time.Now(), attributes)
	attributes[0].Value = "mutated"

	if health.FailureClass != clientkit.FailureNone || health.Message != "ready" {
		t.Fatalf("Record() = %#v, want default-sanitized healthy result", health)
	}
	if cached := client.Health(); cached != health {
		t.Fatalf("Client.Health() = %#v, want cached %#v", cached, health)
	}

	events := observer.snapshot()
	if len(events) != 1 {
		t.Fatalf("health events = %d, want 1", len(events))
	}
	event := events[0].event
	if events[0].ctx.Value(contextKey("request")) != "request-42" {
		t.Fatal("health event did not preserve the supplied context")
	}
	if event.Client != "payments" || event.Protocol != "http" ||
		event.State != health.State || event.FailureClass != health.FailureClass ||
		!event.CheckedAt.Equal(health.CheckedAt) || event.Duration != health.Duration || event.Message != health.Message {
		t.Fatalf("health event = %#v, want cached health and client identity", event)
	}
	if len(event.Attributes) != 1 || event.Attributes[0].Value != "health_check" {
		t.Fatalf("health attributes = %#v, want an observer-safe copy", event.Attributes)
	}
}

func TestRecordReplacesNilContextBeforeObservation(t *testing.T) {
	client, observer := newRecordingClient(t)

	healthrecord.Record(client, nil, "tcp", clientkit.HealthAssessment{State: clientkit.HealthHealthy}, time.Now(), nil)
	events := observer.snapshot()
	if len(events) != 1 || events[0].ctx == nil {
		t.Fatalf("observed contexts = %#v, want one non-nil context", events)
	}
}

func TestRecordIsSafeForConcurrentUse(t *testing.T) {
	client, observer := newRecordingClient(t)

	const workers = 32
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			healthrecord.Record(client, context.Background(), "tcp", clientkit.HealthAssessment{
				State:   clientkit.HealthHealthy,
				Message: "ready",
			}, time.Now(), nil)
		}()
	}
	group.Wait()

	if health := client.Health(); health.State != clientkit.HealthHealthy || health.Message != "ready" {
		t.Fatalf("cached health = %#v, want latest equivalent healthy result", health)
	}
	if got := len(observer.snapshot()); got != workers {
		t.Fatalf("health events = %d, want %d", got, workers)
	}
}

func TestProjectStalenessPreservesHealthWhenProjectionIsNotNeeded(t *testing.T) {
	current := clientkit.Health{
		State:        clientkit.HealthDegraded,
		FailureClass: clientkit.FailureRemoteResponse,
		CheckedAt:    time.Now().Add(-time.Minute),
		Duration:     250 * time.Millisecond,
		Message:      "fallback available",
	}
	unknown := current
	unknown.State = clientkit.HealthUnknown
	old := current
	old.CheckedAt = time.Now().Add(-24 * time.Hour)

	tests := []struct {
		name       string
		health     clientkit.Health
		staleAfter time.Duration
	}{
		{name: "current", health: current, staleAfter: time.Hour},
		{name: "disabled", health: old, staleAfter: 0},
		{name: "negative threshold", health: old, staleAfter: -time.Second},
		{name: "unknown state", health: unknown, staleAfter: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := healthrecord.ProjectStaleness(test.health, test.staleAfter, "HTTP health check"); got != test.health {
				t.Fatalf("ProjectStaleness() = %#v, want unchanged %#v", got, test.health)
			}
		})
	}
}

func TestProjectStalenessProjectsUntrustworthyHealthToUnknown(t *testing.T) {
	duration := 250 * time.Millisecond
	now := time.Now()
	tests := []struct {
		name        string
		checkedAt   time.Time
		wantMessage string
	}{
		{name: "missing timestamp", wantMessage: "TCP health check result has no timestamp"},
		{name: "future timestamp", checkedAt: now.Add(time.Hour), wantMessage: "TCP health check result timestamp is in the future"},
		{name: "stale", checkedAt: now.Add(-2 * time.Hour), wantMessage: "TCP health check result is stale"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := clientkit.Health{
				State:        clientkit.HealthUnhealthy,
				FailureClass: clientkit.FailureTransport,
				CheckedAt:    test.checkedAt,
				Duration:     duration,
				Message:      "raw failure",
			}
			got := healthrecord.ProjectStaleness(input, time.Hour, "TCP health check")
			if got.State != clientkit.HealthUnknown || got.FailureClass != clientkit.FailureNone ||
				!got.CheckedAt.Equal(test.checkedAt) || got.Duration != duration || got.Message != test.wantMessage {
				t.Fatalf("ProjectStaleness() = %#v, want projected unknown health", got)
			}
		})
	}
}

type recordedHealthEvent struct {
	ctx   context.Context
	event clientkit.HealthEvent
}

type recordingHealthObserver struct {
	clientkit.NopObserver
	mu     sync.Mutex
	events []recordedHealthEvent
}

func newRecordingClient(t *testing.T) (*clientkit.Client, *recordingHealthObserver) {
	t.Helper()
	observer := &recordingHealthObserver{}
	client, err := clientkit.New(clientkit.Config{Name: "payments", Observer: observer})
	if err != nil {
		t.Fatalf("clientkit.New() error = %v", err)
	}
	return client, observer
}

func (observer *recordingHealthObserver) ObserveHealth(ctx context.Context, event clientkit.HealthEvent) {
	observer.mu.Lock()
	observer.events = append(observer.events, recordedHealthEvent{ctx: ctx, event: event})
	observer.mu.Unlock()
}

func (observer *recordingHealthObserver) snapshot() []recordedHealthEvent {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]recordedHealthEvent(nil), observer.events...)
}
