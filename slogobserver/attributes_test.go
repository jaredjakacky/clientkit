package slogobserver_test

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/slogobserver"
	"github.com/jaredjakacky/opskit"
)

type countingValue struct {
	calls *atomic.Int32
}

func (v countingValue) LogValue() slog.Value {
	v.calls.Add(1)
	return slog.StringValue("resolved")
}

func TestWithAttributesClonesAndDefersValues(t *testing.T) {
	store := &recordStore{}
	var resolutions atomic.Int32
	attributes := []slog.Attr{
		slog.String("component", "outbound"),
		slog.Any("dynamic", countingValue{calls: &resolutions}),
	}
	observer := slogobserver.New(
		testLogger(store, slog.LevelDebug),
		slogobserver.WithAttributes(attributes...),
		slogobserver.WithAttributes(slog.String("environment", "test")),
	)
	attributes[0] = slog.String("component", "mutated")
	if got := resolutions.Load(); got != 0 {
		t.Fatalf("LogValuer resolutions during New = %d, want 0", got)
	}

	observer.ObserveRetry(context.Background(), clientkit.RetryEvent{})
	record := onlyRecord(t, store)
	if attributeValue(t, record.attributes, "component").String() != "outbound" ||
		attributeValue(t, record.attributes, "environment").String() != "test" ||
		attributeValue(t, record.attributes, "dynamic").String() != "resolved" {
		t.Fatalf("common attributes = %#v, want cloned and resolved values", record.attributes)
	}
	if got := resolutions.Load(); got != 1 {
		t.Fatalf("LogValuer resolutions after logging = %d, want 1", got)
	}
}

func TestWithAttributesClonesWhenOptionIsCreated(t *testing.T) {
	attributes := []slog.Attr{slog.String("component", "original")}
	option := slogobserver.WithAttributes(attributes...)
	attributes[0] = slog.String("component", "mutated")
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug), option)

	observer.ObserveRetry(context.Background(), clientkit.RetryEvent{})
	if got := attributeValue(t, onlyRecord(t, store).attributes, "component").String(); got != "original" {
		t.Fatalf("component = %q, want value captured when option was created", got)
	}
}

func TestEventAttributesAreNestedFilteredAndDeduplicated(t *testing.T) {
	store := &recordStore{}
	observer := slogobserver.New(testLogger(store, slog.LevelDebug))
	observer.ObserveRetry(context.Background(), clientkit.RetryEvent{
		Attributes: []opskit.Attribute{
			{Key: "region", Value: "first"},
			{Key: " ", Value: "omitted"},
			{Key: "zone", Value: "a"},
			{Key: "region", Value: "last"},
		},
	})

	attributes := nestedAttributes(t, clientkitAttributes(t, onlyRecord(t, store)), "attributes")
	if len(attributes) != 2 || len(attributeValues(attributes, "region")) != 1 ||
		attributeValue(t, attributes, "region").String() != "last" ||
		attributeValue(t, attributes, "zone").String() != "a" {
		t.Fatalf("event attributes = %#v, want filtered last-value-wins mapping", attributes)
	}
}
