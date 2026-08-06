package otel_test

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func attributeValue(t *testing.T, attributes []attribute.KeyValue, key string) attribute.Value {
	t.Helper()
	var values []attribute.Value
	for _, value := range attributes {
		if string(value.Key) == key {
			values = append(values, value.Value)
		}
	}
	if len(values) != 1 {
		t.Fatalf("attribute %q occurrences = %d, want 1 in %#v", key, len(values), attributes)
	}
	return values[0]
}

func assertNoAttribute(t *testing.T, attributes []attribute.KeyValue, key string) {
	t.Helper()
	for _, value := range attributes {
		if string(value.Key) == key {
			t.Fatalf("attribute %q unexpectedly present with value %v", key, value.Value)
		}
	}
}

func metricRecordsNamed(records []metricRecord, name string) []metricRecord {
	var matches []metricRecord
	for _, record := range records {
		if record.name == name {
			matches = append(matches, record)
		}
	}
	return matches
}

func onlyMetricRecord(t *testing.T, records []metricRecord, name string) metricRecord {
	t.Helper()
	matches := metricRecordsNamed(records, name)
	if len(matches) != 1 {
		t.Fatalf("metric %q records = %d, want 1: %#v", name, len(matches), matches)
	}
	return matches[0]
}

func metricAttributeValue(t *testing.T, record metricRecord, key string) attribute.Value {
	t.Helper()
	value, ok := record.attributes.Value(attribute.Key(key))
	if !ok {
		t.Fatalf("metric %q attribute %q was not recorded", record.name, key)
	}
	return value
}

func assertNoMetricAttribute(t *testing.T, record metricRecord, key string) {
	t.Helper()
	if value, ok := record.attributes.Value(attribute.Key(key)); ok {
		t.Fatalf("metric %q unexpectedly contains %s=%v", record.name, key, value)
	}
}

func assertSameMetricAttributes(t *testing.T, records []metricRecord, names ...string) {
	t.Helper()
	if len(names) < 2 {
		t.Fatal("assertSameMetricAttributes requires at least two metric names")
	}

	reference := onlyMetricRecord(t, records, names[0])
	want := reference.attributes.Equivalent()
	for _, name := range names[1:] {
		record := onlyMetricRecord(t, records, name)
		if got := record.attributes.Equivalent(); got != want {
			t.Errorf("metric %q attributes = %#v, want same set as %q: %#v", name, record.attributes.ToSlice(), names[0], reference.attributes.ToSlice())
		}
	}
}
