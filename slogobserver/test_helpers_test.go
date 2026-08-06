package slogobserver_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type capturedRecord struct {
	time       time.Time
	level      slog.Level
	message    string
	attributes []slog.Attr
	context    context.Context
}

type recordStore struct {
	mu sync.Mutex

	records         []capturedRecord
	enabledContexts []context.Context
	handleError     error
}

func (s *recordStore) snapshot() ([]capturedRecord, []context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]capturedRecord, len(s.records))
	for index, record := range s.records {
		record.attributes = append([]slog.Attr(nil), record.attributes...)
		records[index] = record
	}
	return records, append([]context.Context(nil), s.enabledContexts...)
}

type recordingHandler struct {
	store      *recordStore
	minimum    slog.Level
	attributes []slog.Attr
	groups     []string
}

func (h recordingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	h.store.mu.Lock()
	h.store.enabledContexts = append(h.store.enabledContexts, ctx)
	h.store.mu.Unlock()
	return level >= h.minimum
}

func (h recordingHandler) Handle(ctx context.Context, record slog.Record) error {
	attributes := make([]slog.Attr, len(h.attributes))
	for index, attribute := range h.attributes {
		attributes[index] = resolveAttribute(attribute)
	}
	var recordAttributes []slog.Attr
	record.Attrs(func(attribute slog.Attr) bool {
		recordAttributes = append(recordAttributes, resolveAttribute(attribute))
		return true
	})
	attributes = append(attributes, nestAttributes(h.groups, recordAttributes)...)

	h.store.mu.Lock()
	h.store.records = append(h.store.records, capturedRecord{
		time:       record.Time,
		level:      record.Level,
		message:    record.Message,
		attributes: attributes,
		context:    ctx,
	})
	err := h.store.handleError
	h.store.mu.Unlock()
	return err
}

func (h recordingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clone := h
	clone.attributes = append(append([]slog.Attr(nil), h.attributes...), nestAttributes(h.groups, attributes)...)
	return clone
}

func (h recordingHandler) WithGroup(name string) slog.Handler {
	clone := h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return clone
}

func resolveAttribute(attribute slog.Attr) slog.Attr {
	attribute.Value = attribute.Value.Resolve()
	if attribute.Value.Kind() != slog.KindGroup {
		return attribute
	}
	group := attribute.Value.Group()
	for index := range group {
		group[index] = resolveAttribute(group[index])
	}
	attribute.Value = slog.GroupValue(group...)
	return attribute
}

func nestAttributes(groups []string, attributes []slog.Attr) []slog.Attr {
	nested := append([]slog.Attr(nil), attributes...)
	for index := len(groups) - 1; index >= 0; index-- {
		nested = []slog.Attr{{Key: groups[index], Value: slog.GroupValue(nested...)}}
	}
	return nested
}

func testLogger(store *recordStore, minimum slog.Level) *slog.Logger {
	return slog.New(recordingHandler{store: store, minimum: minimum})
}

func onlyRecord(t *testing.T, store *recordStore) capturedRecord {
	t.Helper()
	records, _ := store.snapshot()
	if len(records) != 1 {
		t.Fatalf("log records = %d, want 1: %#v", len(records), records)
	}
	return records[0]
}

func attributeValues(attributes []slog.Attr, key string) []slog.Value {
	var values []slog.Value
	for _, attribute := range attributes {
		if attribute.Key == key {
			values = append(values, attribute.Value)
		}
	}
	return values
}

func attributeValue(t *testing.T, attributes []slog.Attr, key string) slog.Value {
	t.Helper()
	values := attributeValues(attributes, key)
	if len(values) != 1 {
		t.Fatalf("attribute %q occurrences = %d, want 1 in %#v", key, len(values), attributes)
	}
	return values[0]
}

func optionalAttribute(attributes []slog.Attr, key string) (slog.Value, bool) {
	values := attributeValues(attributes, key)
	if len(values) == 0 {
		return slog.Value{}, false
	}
	return values[len(values)-1], true
}

func clientkitAttributes(t *testing.T, record capturedRecord) []slog.Attr {
	t.Helper()
	value := attributeValue(t, record.attributes, "clientkit")
	if value.Kind() != slog.KindGroup {
		t.Fatalf("clientkit attribute kind = %v, want group", value.Kind())
	}
	return value.Group()
}

func nestedAttributes(t *testing.T, attributes []slog.Attr, key string) []slog.Attr {
	t.Helper()
	value := attributeValue(t, attributes, key)
	if value.Kind() != slog.KindGroup {
		t.Fatalf("%s attribute kind = %v, want group", key, value.Kind())
	}
	return value.Group()
}

var _ slog.Handler = recordingHandler{}
