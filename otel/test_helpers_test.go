package otel_test

import (
	"context"
	"sync"
	"testing"
	"time"

	clientkitotel "github.com/jaredjakacky/clientkit/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

type telemetryFixture struct {
	observer *clientkitotel.Observer
	traces   *recordingTracerProvider
	metrics  *metricRecorder
}

func newTelemetryFixture(t *testing.T, options ...clientkitotel.Option) telemetryFixture {
	t.Helper()

	tracerProvider := newRecordingTracerProvider()
	meterProvider, metrics := newRecordingMeterProvider()
	options = append([]clientkitotel.Option{
		clientkitotel.WithTracerProvider(tracerProvider),
		clientkitotel.WithMeterProvider(meterProvider),
	}, options...)
	observer, err := clientkitotel.New(options...)
	if err != nil {
		t.Fatalf("construct OTel observer: %v", err)
	}

	return telemetryFixture{
		observer: observer,
		traces:   tracerProvider,
		metrics:  metrics,
	}
}

type recordedEvent struct {
	Name       string
	Time       time.Time
	Attributes []attribute.KeyValue
}

type recordedSpan struct {
	Name              string
	Kind              trace.SpanKind
	StartedAt         time.Time
	EndedAt           time.Time
	Attributes        []attribute.KeyValue
	Events            []recordedEvent
	Errors            []recordedError
	StatusCode        codes.Code
	StatusDescription string
	EndCount          int
}

type recordedError struct {
	Err  error
	Time time.Time
}

type scopeRecord struct {
	Name    string
	Version string
}

type recordingTracerProvider struct {
	trace.TracerProvider
	mu      sync.Mutex
	started []*recordingSpan
	scopes  []scopeRecord
}

func newRecordingTracerProvider() *recordingTracerProvider {
	return &recordingTracerProvider{TracerProvider: tracenoop.NewTracerProvider()}
}

func (provider *recordingTracerProvider) Tracer(name string, options ...trace.TracerOption) trace.Tracer {
	config := trace.NewTracerConfig(options...)
	provider.mu.Lock()
	provider.scopes = append(provider.scopes, scopeRecord{Name: name, Version: config.InstrumentationVersion()})
	provider.mu.Unlock()
	return &recordingTracer{
		Tracer:   provider.TracerProvider.Tracer(name, options...),
		provider: provider,
	}
}

func (provider *recordingTracerProvider) scopeRecords() []scopeRecord {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]scopeRecord(nil), provider.scopes...)
}

func (provider *recordingTracerProvider) spans() []recordedSpan {
	provider.mu.Lock()
	started := append([]*recordingSpan(nil), provider.started...)
	provider.mu.Unlock()

	spans := make([]recordedSpan, 0, len(started))
	for _, span := range started {
		spans = append(spans, span.snapshot())
	}
	return spans
}

type recordingTracer struct {
	trace.Tracer
	provider *recordingTracerProvider
}

func (tracer *recordingTracer) Start(ctx context.Context, name string, options ...trace.SpanStartOption) (context.Context, trace.Span) {
	next, base := tracer.Tracer.Start(ctx, name, options...)
	config := trace.NewSpanStartConfig(options...)
	span := &recordingSpan{
		Span:       base,
		provider:   tracer.provider,
		name:       name,
		kind:       config.SpanKind(),
		startedAt:  config.Timestamp(),
		attributes: cloneAttributes(config.Attributes()),
	}
	tracer.provider.mu.Lock()
	tracer.provider.started = append(tracer.provider.started, span)
	tracer.provider.mu.Unlock()
	return trace.ContextWithSpan(next, span), span
}

type recordingSpan struct {
	trace.Span
	provider   *recordingTracerProvider
	mu         sync.Mutex
	name       string
	attributes []attribute.KeyValue
	events     []recordedEvent
	errors     []recordedError
	kind       trace.SpanKind
	startedAt  time.Time
	endedAt    time.Time
	statusCode codes.Code
	statusText string
	endCount   int
}

func (span *recordingSpan) End(options ...trace.SpanEndOption) {
	config := trace.NewSpanEndConfig(options...)
	span.mu.Lock()
	span.endCount++
	span.endedAt = config.Timestamp()
	span.mu.Unlock()
}

func (span *recordingSpan) AddEvent(name string, options ...trace.EventOption) {
	config := trace.NewEventConfig(options...)
	span.mu.Lock()
	span.events = append(span.events, recordedEvent{
		Name:       name,
		Time:       config.Timestamp(),
		Attributes: cloneAttributes(config.Attributes()),
	})
	span.mu.Unlock()
}

func (span *recordingSpan) IsRecording() bool {
	return true
}

func (span *recordingSpan) RecordError(err error, options ...trace.EventOption) {
	config := trace.NewEventConfig(options...)
	span.mu.Lock()
	span.errors = append(span.errors, recordedError{Err: err, Time: config.Timestamp()})
	span.mu.Unlock()
	span.AddEvent("exception", options...)
}

func (span *recordingSpan) SetStatus(code codes.Code, description string) {
	span.mu.Lock()
	span.statusCode = code
	span.statusText = description
	span.mu.Unlock()
}

func (span *recordingSpan) SetName(name string) {
	span.mu.Lock()
	span.name = name
	span.mu.Unlock()
}

func (span *recordingSpan) SetAttributes(attributes ...attribute.KeyValue) {
	span.mu.Lock()
	span.attributes = mergeRecordedAttributes(span.attributes, attributes)
	span.mu.Unlock()
}

func (span *recordingSpan) TracerProvider() trace.TracerProvider {
	return span.provider
}

func (span *recordingSpan) snapshot() recordedSpan {
	span.mu.Lock()
	defer span.mu.Unlock()

	events := make([]recordedEvent, len(span.events))
	for index, event := range span.events {
		events[index] = recordedEvent{
			Name:       event.Name,
			Time:       event.Time,
			Attributes: cloneAttributes(event.Attributes),
		}
	}
	return recordedSpan{
		Name:              span.name,
		Kind:              span.kind,
		StartedAt:         span.startedAt,
		EndedAt:           span.endedAt,
		Attributes:        cloneAttributes(span.attributes),
		Events:            events,
		Errors:            append([]recordedError(nil), span.errors...),
		StatusCode:        span.statusCode,
		StatusDescription: span.statusText,
		EndCount:          span.endCount,
	}
}

func cloneAttributes(values []attribute.KeyValue) []attribute.KeyValue {
	return append([]attribute.KeyValue(nil), values...)
}

func mergeRecordedAttributes(existing []attribute.KeyValue, additions []attribute.KeyValue) []attribute.KeyValue {
	merged := cloneAttributes(existing)
	positions := make(map[attribute.Key]int, len(merged))
	for index, value := range merged {
		positions[value.Key] = index
	}
	for _, value := range additions {
		if index, ok := positions[value.Key]; ok {
			merged[index] = value
			continue
		}
		positions[value.Key] = len(merged)
		merged = append(merged, value)
	}
	return merged
}

type metricRecord struct {
	name       string
	attributes attribute.Set
	value      any
	context    context.Context
}

type instrumentRecord struct {
	name        string
	kind        string
	unit        string
	description string
}

type metricRecorder struct {
	mu sync.Mutex

	values          []metricRecord
	instruments     []instrumentRecord
	scopes          []scopeRecord
	failInstrument  string
	instrumentError error
}

func (recorder *metricRecorder) record(ctx context.Context, name string, value any, attributes attribute.Set) {
	recorder.mu.Lock()
	recorder.values = append(recorder.values, metricRecord{name: name, attributes: attributes, value: value, context: ctx})
	recorder.mu.Unlock()
}

func (recorder *metricRecorder) records() []metricRecord {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]metricRecord(nil), recorder.values...)
}

func (recorder *metricRecorder) instrumentRecords() []instrumentRecord {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]instrumentRecord(nil), recorder.instruments...)
}

func (recorder *metricRecorder) scopeRecords() []scopeRecord {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]scopeRecord(nil), recorder.scopes...)
}

type recordingMeterProvider struct {
	metric.MeterProvider
	recorder *metricRecorder
}

func newRecordingMeterProvider() (*recordingMeterProvider, *metricRecorder) {
	recorder := &metricRecorder{}
	return &recordingMeterProvider{
		MeterProvider: metricnoop.NewMeterProvider(),
		recorder:      recorder,
	}, recorder
}

func (provider *recordingMeterProvider) Meter(name string, options ...metric.MeterOption) metric.Meter {
	config := metric.NewMeterConfig(options...)
	provider.recorder.mu.Lock()
	provider.recorder.scopes = append(provider.recorder.scopes, scopeRecord{Name: name, Version: config.InstrumentationVersion()})
	provider.recorder.mu.Unlock()
	return &recordingMeter{
		Meter:    provider.MeterProvider.Meter(name, options...),
		recorder: provider.recorder,
	}
}

type recordingMeter struct {
	metric.Meter
	recorder *metricRecorder
}

func (meter *recordingMeter) Int64Counter(name string, options ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	config := metric.NewInt64CounterConfig(options...)
	meter.recorder.mu.Lock()
	meter.recorder.instruments = append(meter.recorder.instruments, instrumentRecord{name: name, kind: "int64_counter", unit: config.Unit(), description: config.Description()})
	fail := meter.recorder.failInstrument == name
	err := meter.recorder.instrumentError
	meter.recorder.mu.Unlock()
	if fail {
		return nil, err
	}
	instrument, err := meter.Meter.Int64Counter(name, options...)
	return &recordingInt64Counter{Int64Counter: instrument, name: name, recorder: meter.recorder}, err
}

func (meter *recordingMeter) Int64Histogram(name string, options ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	config := metric.NewInt64HistogramConfig(options...)
	meter.recorder.mu.Lock()
	meter.recorder.instruments = append(meter.recorder.instruments, instrumentRecord{name: name, kind: "int64_histogram", unit: config.Unit(), description: config.Description()})
	fail := meter.recorder.failInstrument == name
	err := meter.recorder.instrumentError
	meter.recorder.mu.Unlock()
	if fail {
		return nil, err
	}
	instrument, err := meter.Meter.Int64Histogram(name, options...)
	return &recordingInt64Histogram{Int64Histogram: instrument, name: name, recorder: meter.recorder}, err
}

func (meter *recordingMeter) Float64Histogram(name string, options ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	config := metric.NewFloat64HistogramConfig(options...)
	meter.recorder.mu.Lock()
	meter.recorder.instruments = append(meter.recorder.instruments, instrumentRecord{name: name, kind: "float64_histogram", unit: config.Unit(), description: config.Description()})
	fail := meter.recorder.failInstrument == name
	err := meter.recorder.instrumentError
	meter.recorder.mu.Unlock()
	if fail {
		return nil, err
	}
	instrument, err := meter.Meter.Float64Histogram(name, options...)
	return &recordingFloat64Histogram{Float64Histogram: instrument, name: name, recorder: meter.recorder}, err
}

type recordingInt64Counter struct {
	metric.Int64Counter
	name     string
	recorder *metricRecorder
}

func (counter *recordingInt64Counter) Add(ctx context.Context, value int64, options ...metric.AddOption) {
	counter.recorder.record(ctx, counter.name, value, metric.NewAddConfig(options).Attributes())
}

func (counter *recordingInt64Counter) Enabled(context.Context) bool {
	return true
}

type recordingInt64Histogram struct {
	metric.Int64Histogram
	name     string
	recorder *metricRecorder
}

func (histogram *recordingInt64Histogram) Record(ctx context.Context, value int64, options ...metric.RecordOption) {
	histogram.recorder.record(ctx, histogram.name, value, metric.NewRecordConfig(options).Attributes())
}

func (histogram *recordingInt64Histogram) Enabled(context.Context) bool {
	return true
}

type recordingFloat64Histogram struct {
	metric.Float64Histogram
	name     string
	recorder *metricRecorder
}

func (histogram *recordingFloat64Histogram) Record(ctx context.Context, value float64, options ...metric.RecordOption) {
	histogram.recorder.record(ctx, histogram.name, value, metric.NewRecordConfig(options).Attributes())
}

func (histogram *recordingFloat64Histogram) Enabled(context.Context) bool {
	return true
}
