package otel

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/opskit"
	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationScope = "github.com/jaredjakacky/clientkit/otel"

var (
	_ clientkit.Observer             = (*Observer)(nil)
	_ clientkit.OperationObservation = (*operationObservation)(nil)
)

type config struct {
	tracerProvider         trace.TracerProvider
	meterProvider          metric.MeterProvider
	instrumentationVersion string
	attributes             []attribute.KeyValue
	errorDetails           bool
}

// Option configures an Observer.
type Option func(*config)

// WithTracerProvider uses provider for tracing. A nil provider falls back to
// the global OpenTelemetry tracer provider during construction.
func WithTracerProvider(provider trace.TracerProvider) Option {
	return func(cfg *config) {
		cfg.tracerProvider = provider
	}
}

// WithMeterProvider uses provider for metrics. A nil provider falls back to
// the global OpenTelemetry meter provider during construction.
func WithMeterProvider(provider metric.MeterProvider) Option {
	return func(cfg *config) {
		cfg.meterProvider = provider
	}
}

// WithInstrumentationVersion sets the instrumentation scope version used by
// the observer's tracer and meter.
func WithInstrumentationVersion(version string) Option {
	return func(cfg *config) {
		cfg.instrumentationVersion = version
	}
}

// WithAttributes adds attributes to every emitted span and metric. Values must
// remain stable and low-cardinality. Service identity such as service.name,
// service.version, and deployment.environment should normally be configured
// through the OpenTelemetry Resource instead of repeated here. Clientkit-owned
// identity, outcome, success, and failure attributes take precedence over
// conflicting keys.
func WithAttributes(attributes ...attribute.KeyValue) Option {
	cloned := append([]attribute.KeyValue(nil), attributes...)
	return func(cfg *config) {
		cfg.attributes = append(cfg.attributes, cloned...)
	}
}

// WithErrorDetails records the final raw operation error as an OpenTelemetry
// exception event. Raw errors may contain URLs, endpoint names, certificate
// details, or application-controlled text, so production callers should enable
// this only when their telemetry pipeline provides suitable redaction.
func WithErrorDetails() Option {
	return func(cfg *config) {
		cfg.errorDetails = true
	}
}

// Observer adapts Clientkit observer events to OpenTelemetry traces and
// metrics. It is safe for concurrent use.
type Observer struct {
	tracer       trace.Tracer
	metrics      instruments
	attributes   []attribute.KeyValue
	errorDetails bool
}

// New constructs an OpenTelemetry observer and creates its instruments without
// starting or owning an SDK. Global providers are captured during construction
// when explicit providers are not supplied; applications should configure
// globals first and remain responsible for provider shutdown.
func New(options ...Option) (*Observer, error) {
	cfg := config{}
	for _, option := range options {
		if option != nil {
			option(&cfg)
		}
	}

	if cfg.tracerProvider == nil {
		cfg.tracerProvider = apiotel.GetTracerProvider()
	}
	if cfg.meterProvider == nil {
		cfg.meterProvider = apiotel.GetMeterProvider()
	}

	tracerOptions := []trace.TracerOption(nil)
	meterOptions := []metric.MeterOption(nil)
	if cfg.instrumentationVersion != "" {
		tracerOptions = append(tracerOptions, trace.WithInstrumentationVersion(cfg.instrumentationVersion))
		meterOptions = append(meterOptions, metric.WithInstrumentationVersion(cfg.instrumentationVersion))
	}

	values, err := newInstruments(cfg.meterProvider.Meter(instrumentationScope, meterOptions...))
	if err != nil {
		return nil, err
	}

	return &Observer{
		tracer:       cfg.tracerProvider.Tracer(instrumentationScope, tracerOptions...),
		metrics:      values,
		attributes:   withoutFailureClass(cfg.attributes),
		errorDetails: cfg.errorDetails,
	}, nil
}

// StartOperation starts one client span for the complete outbound operation.
func (o *Observer) StartOperation(ctx context.Context, event clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	ctx = contextOrBackground(ctx)
	if o == nil {
		return ctx, clientkit.NopOperationObservation{}
	}

	options := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(o.spanAttributes(event.Attributes, identityAttributes(event.Client, event.Protocol, event.Operation))...),
	}
	if !event.StartedAt.IsZero() {
		options = append(options, trace.WithTimestamp(event.StartedAt))
	}

	spanContext, span := o.tracer.Start(ctx, operationSpanName(event.Protocol, event.Operation), options...)
	return spanContext, &operationObservation{observer: o, span: span}
}

// ObserveAttempt records a completed attempt as a span event and metrics.
func (o *Observer) ObserveAttempt(ctx context.Context, event clientkit.AttemptEvent) {
	if o == nil {
		return
	}
	ctx = contextOrBackground(ctx)
	span := trace.SpanFromContext(ctx)
	attributes := o.spanAttributes(event.Attributes,
		identityAttributes(event.Client, event.Protocol, event.Operation),
		[]attribute.KeyValue{
			attribute.Int(AttributeAttemptNumber, event.Number),
			attribute.String(AttributeOutcome, event.Outcome),
			attribute.Bool(AttributeSucceeded, attemptSucceeded(event)),
		},
		failureClassAttributes(event.FailureClass),
	)
	options := []trace.EventOption{trace.WithAttributes(attributes...)}
	if !event.EndedAt.IsZero() {
		options = append(options, trace.WithTimestamp(event.EndedAt))
	}
	span.AddEvent("clientkit.attempt", options...)
	o.recordAttempt(ctx, event)
}

// ObserveRetry records a scheduled retry as a span event and metrics.
func (o *Observer) ObserveRetry(ctx context.Context, event clientkit.RetryEvent) {
	if o == nil {
		return
	}
	ctx = contextOrBackground(ctx)
	attributes := o.spanAttributes(event.Attributes,
		identityAttributes(event.Client, event.Protocol, event.Operation),
		[]attribute.KeyValue{
			attribute.Int(AttributeRetryAfterAttempt, event.AfterAttempt),
			attribute.String(AttributeRetryCause, event.Cause),
			attribute.Float64(AttributeRetryDelay, event.Delay.Seconds()),
		},
		failureClassAttributes(event.FailureClass),
	)
	options := []trace.EventOption{trace.WithAttributes(attributes...)}
	if !event.At.IsZero() {
		options = append(options, trace.WithTimestamp(event.At))
	}
	trace.SpanFromContext(ctx).AddEvent("clientkit.retry", options...)
	o.recordRetry(ctx, event)
}

// ObserveHealth records health-check metrics and adds an event to an existing
// recording span without creating another span.
func (o *Observer) ObserveHealth(ctx context.Context, event clientkit.HealthEvent) {
	if o == nil {
		return
	}
	ctx = contextOrBackground(ctx)
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		attributes := o.spanAttributes(event.Attributes, []attribute.KeyValue{
			attribute.String(AttributeClientName, event.Client),
			attribute.String(AttributeProtocol, event.Protocol),
			attribute.String(AttributeHealthState, string(event.State)),
		}, failureClassAttributes(event.FailureClass))
		options := []trace.EventOption{trace.WithAttributes(attributes...)}
		if !event.CheckedAt.IsZero() {
			options = append(options, trace.WithTimestamp(event.CheckedAt))
		}
		span.AddEvent("clientkit.health", options...)
	}
	o.recordHealth(ctx, event)
}

func (o *Observer) spanAttributes(eventAttributes []opskit.Attribute, groups ...[]attribute.KeyValue) []attribute.KeyValue {
	all := make([][]attribute.KeyValue, 0, len(groups)+2)
	all = append(all, o.attributes, convertAttributes(eventAttributes))
	all = append(all, groups...)
	return mergeAttributes(all...)
}

func (o *Observer) metricAttributes(eventAttributes []opskit.Attribute, groups ...[]attribute.KeyValue) []attribute.KeyValue {
	return o.spanAttributes(eventAttributes, groups...)
}

type operationObservation struct {
	once     sync.Once
	observer *Observer
	span     trace.Span
}

func (o *operationObservation) End(ctx context.Context, event clientkit.OperationEndEvent) {
	if o == nil {
		return
	}
	ctx = contextOrBackground(ctx)
	o.once.Do(func() {
		attributes := o.observer.spanAttributes(event.Attributes,
			identityAttributes(event.Client, event.Protocol, event.Operation),
			[]attribute.KeyValue{
				attribute.String(AttributeOutcome, event.Outcome),
				attribute.Bool(AttributeSucceeded, operationSucceeded(event)),
			},
			failureClassAttributes(event.FailureClass),
		)
		if event.Attempts > 0 {
			attributes = mergeAttributes(attributes, []attribute.KeyValue{
				attribute.Int(AttributeOperationAttempts, event.Attempts),
			})
		}
		o.span.SetAttributes(attributes...)
		if o.observer.errorDetails && event.Err != nil {
			recordError(o.span, event.Err, event.EndedAt)
		}
		setOperationStatus(o.span, event)
		o.observer.recordOperation(ctx, event)

		if event.EndedAt.IsZero() {
			o.span.End()
		} else {
			o.span.End(trace.WithTimestamp(event.EndedAt))
		}
	})
}

func operationSpanName(protocol, operation string) string {
	if strings.TrimSpace(protocol) == "" || strings.TrimSpace(operation) == "" {
		return "clientkit.operation"
	}
	return "clientkit." + protocol + "." + operation
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func recordError(span trace.Span, err error, at time.Time) {
	if at.IsZero() {
		span.RecordError(err)
		return
	}
	span.RecordError(err, trace.WithTimestamp(at))
}

func setOperationStatus(span trace.Span, event clientkit.OperationEndEvent) {
	switch {
	case operationSucceeded(event):
		span.SetStatus(codes.Ok, "")
	case event.Outcome != "":
		span.SetStatus(codes.Error, event.Outcome)
	default:
		span.SetStatus(codes.Error, "operation_failed")
	}
}

func operationSucceeded(event clientkit.OperationEndEvent) bool {
	return event.Succeeded && event.FailureClass == clientkit.FailureNone && event.Err == nil
}

func attemptSucceeded(event clientkit.AttemptEvent) bool {
	return event.Succeeded && event.FailureClass == clientkit.FailureNone && event.Err == nil
}
