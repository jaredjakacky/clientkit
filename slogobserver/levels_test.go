package slogobserver_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/slogobserver"
)

func TestDefaultLevelConfig(t *testing.T) {
	levels := slogobserver.DefaultLevelConfig()
	if levels.OperationSuccess != slog.LevelDebug {
		t.Fatalf("OperationSuccess = %v, want debug", levels.OperationSuccess)
	}
	if levels.OperationFailure != slog.LevelError {
		t.Fatalf("OperationFailure = %v, want error", levels.OperationFailure)
	}
	if levels.Attempt != slog.LevelDebug {
		t.Fatalf("Attempt = %v, want debug", levels.Attempt)
	}
	if levels.Retry != slog.LevelWarn {
		t.Fatalf("Retry = %v, want warn", levels.Retry)
	}
	if levels.HealthHealthy != slog.LevelDebug {
		t.Fatalf("HealthHealthy = %v, want debug", levels.HealthHealthy)
	}
	if levels.HealthUnhealthy != slog.LevelWarn {
		t.Fatalf("HealthUnhealthy = %v, want warn", levels.HealthUnhealthy)
	}
}

func TestWithLevelsControlsEveryRecordType(t *testing.T) {
	levels := slogobserver.LevelConfig{
		OperationSuccess: slog.Level(-8),
		OperationFailure: slog.Level(-4),
		Attempt:          slog.LevelInfo,
		Retry:            slog.LevelWarn,
		HealthHealthy:    slog.LevelError,
		HealthUnhealthy:  slog.Level(12),
	}
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.Level(-100)), slogobserver.WithLevels(levels))
	now := time.Now().UTC()

	_, success := observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	success.End(context.Background(), clientkit.OperationEndEvent{EndedAt: now, Succeeded: true})
	_, failure := observer.StartOperation(context.Background(), clientkit.OperationStartEvent{})
	failure.End(context.Background(), clientkit.OperationEndEvent{EndedAt: now})
	observer.ObserveAttempt(context.Background(), clientkit.AttemptEvent{EndedAt: now})
	observer.ObserveRetry(context.Background(), clientkit.RetryEvent{At: now})
	observer.ObserveHealth(context.Background(), clientkit.HealthEvent{CheckedAt: now, State: clientkit.HealthHealthy})
	observer.ObserveHealth(context.Background(), clientkit.HealthEvent{CheckedAt: now, State: clientkit.HealthDegraded})

	records, _ := store.snapshot()
	want := []slog.Level{
		levels.OperationSuccess,
		levels.OperationFailure,
		levels.Attempt,
		levels.Retry,
		levels.HealthHealthy,
		levels.HealthUnhealthy,
	}
	if len(records) != len(want) {
		t.Fatalf("log records = %d, want %d", len(records), len(want))
	}
	for index, level := range want {
		if got := records[index].level; got != level {
			t.Errorf("record %d level = %v, want %v", index, got, level)
		}
	}
}

func TestWithLevelsUsesZeroFieldsAsInfo(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(
		testLogger(store, slog.LevelDebug),
		slogobserver.WithLevels(slogobserver.LevelConfig{}),
	)
	observer.ObserveAttempt(context.Background(), clientkit.AttemptEvent{})

	if got := onlyRecord(t, store).level; got != slog.LevelInfo {
		t.Fatalf("level = %v, want INFO from zero LevelConfig field", got)
	}
}
