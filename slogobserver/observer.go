package slogobserver

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jaredjakacky/clientkit"
)

var (
	_ clientkit.Observer             = (*Observer)(nil)
	_ clientkit.OperationObservation = (*operationObservation)(nil)
)

type config struct {
	levels       LevelConfig
	attributes   []slog.Attr
	errorDetails bool
}

// Option configures an Observer during construction.
type Option func(*config)

// WithLevels completely replaces the default record-level configuration. Zero
// fields are used as slog.LevelInfo rather than inheriting individual defaults.
func WithLevels(levels LevelConfig) Option {
	return func(cfg *config) {
		cfg.levels = levels
	}
}

// WithAttributes appends application-controlled attributes to every record.
// Values should be stable and low-cardinality and must not contain secrets.
// Service identity may instead be configured on the logger with Logger.With.
// The supplied slice is cloned and LogValuer values are not resolved during
// construction.
func WithAttributes(attributes ...slog.Attr) Option {
	cloned := append([]slog.Attr(nil), attributes...)
	return func(cfg *config) {
		cfg.attributes = append(cfg.attributes, cloned...)
	}
}

// WithErrorDetails opts into adding raw Go errors to operation and attempt
// records when the event carries an error. Errors may contain URLs, hosts,
// ports, certificate details, transport text, or other infrastructure and
// application data. Applications remain responsible for logger redaction and
// access policy.
func WithErrorDetails() Option {
	return func(cfg *config) {
		cfg.errorDetails = true
	}
}

// Observer adapts protocol-neutral Clientkit lifecycle events to synchronous
// structured log/slog records. It is immutable and safe for concurrent use.
type Observer struct {
	logger       *slog.Logger
	levels       LevelConfig
	attributes   []slog.Attr
	errorDetails bool
}

// New constructs an Observer without logging. A nil logger uses slog.Default.
// Nil options are ignored, and configured common attributes are cloned.
func New(logger *slog.Logger, options ...Option) *Observer {
	cfg := config{levels: DefaultLevelConfig()}
	for _, option := range options {
		if option != nil {
			option(&cfg)
		}
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Observer{
		logger:       logger,
		levels:       cfg.levels,
		attributes:   append([]slog.Attr(nil), cfg.attributes...),
		errorDetails: cfg.errorDetails,
	}
}

// StartOperation captures immutable start metadata without emitting a record.
// Completion is logged when the returned observation is ended.
func (o *Observer) StartOperation(ctx context.Context, event clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	ctx = contextOrBackground(ctx)
	if o == nil || o.logger == nil {
		return ctx, clientkit.NopOperationObservation{}
	}

	return ctx, &operationObservation{
		observer:   o,
		client:     event.Client,
		protocol:   event.Protocol,
		operation:  event.Operation,
		attributes: eventAttributes(event.Attributes),
	}
}

// ObserveAttempt emits one completed-attempt record at the configured Attempt
// level. Raw errors are included only when WithErrorDetails was selected.
func (o *Observer) ObserveAttempt(ctx context.Context, event clientkit.AttemptEvent) {
	if o == nil || o.logger == nil {
		return
	}

	attributes := make([]slog.Attr, 0, 10)
	attributes = append(attributes, slog.String("event", "attempt_completed"))
	addString(&attributes, "client", event.Client)
	addString(&attributes, "protocol", event.Protocol)
	addString(&attributes, "operation", event.Operation)
	addString(&attributes, "outcome", event.Outcome)
	attributes = append(attributes, slog.Bool("succeeded", attemptSucceeded(event)))
	addFailureClass(&attributes, event.FailureClass)
	if event.Number > 0 {
		attributes = append(attributes, slog.Int("attempt", event.Number))
	}
	addDuration(&attributes, "duration", event.Duration)
	if o.errorDetails && event.Err != nil {
		attributes = append(attributes, slog.Any("error", event.Err))
	}
	attributes = addEventAttributeGroup(attributes, eventAttributes(event.Attributes))

	o.log(ctx, event.EndedAt, o.levels.Attempt, "clientkit attempt completed", attributes)
}

// ObserveRetry emits one retry-scheduling record at the configured Retry level.
func (o *Observer) ObserveRetry(ctx context.Context, event clientkit.RetryEvent) {
	if o == nil || o.logger == nil {
		return
	}

	attributes := make([]slog.Attr, 0, 10)
	attributes = append(attributes, slog.String("event", "retry_scheduled"))
	addString(&attributes, "client", event.Client)
	addString(&attributes, "protocol", event.Protocol)
	addString(&attributes, "operation", event.Operation)
	addFailureClass(&attributes, event.FailureClass)
	if event.AfterAttempt > 0 {
		attributes = append(attributes, slog.Int("after_attempt", event.AfterAttempt))
	}
	addString(&attributes, "cause", event.Cause)
	addDuration(&attributes, "delay", event.Delay)
	attributes = addEventAttributeGroup(attributes, eventAttributes(event.Attributes))

	o.log(ctx, event.At, o.levels.Retry, "clientkit retry scheduled", attributes)
}

// ObserveHealth emits one completed health-check record. Healthy results use
// HealthHealthy; degraded, unhealthy, and unknown results use HealthUnhealthy.
func (o *Observer) ObserveHealth(ctx context.Context, event clientkit.HealthEvent) {
	if o == nil || o.logger == nil {
		return
	}

	attributes := make([]slog.Attr, 0, 9)
	attributes = append(attributes, slog.String("event", "health_check_completed"))
	addString(&attributes, "client", event.Client)
	addString(&attributes, "protocol", event.Protocol)
	addString(&attributes, "health_state", string(event.State))
	addFailureClass(&attributes, event.FailureClass)
	addDuration(&attributes, "duration", event.Duration)
	addString(&attributes, "message", event.Message)
	attributes = addEventAttributeGroup(attributes, eventAttributes(event.Attributes))

	level := o.levels.HealthUnhealthy
	if event.State == clientkit.HealthHealthy {
		level = o.levels.HealthHealthy
	}
	o.log(ctx, event.CheckedAt, level, "clientkit health check completed", attributes)
}

type operationObservation struct {
	once       sync.Once
	observer   *Observer
	client     string
	protocol   string
	operation  string
	attributes []slog.Attr
}

func (o *operationObservation) End(ctx context.Context, event clientkit.OperationEndEvent) {
	ctx = contextOrBackground(ctx)
	if o == nil || o.observer == nil {
		return
	}
	o.once.Do(func() {
		client := event.Client
		if client == "" {
			client = o.client
		}
		protocol := event.Protocol
		if protocol == "" {
			protocol = o.protocol
		}
		operation := event.Operation
		if operation == "" {
			operation = o.operation
		}
		convertedAttributes := eventAttributes(event.Attributes)
		if len(convertedAttributes) == 0 {
			convertedAttributes = o.attributes
		}

		attributes := make([]slog.Attr, 0, 11)
		attributes = append(attributes, slog.String("event", "operation_completed"))
		addString(&attributes, "client", client)
		addString(&attributes, "protocol", protocol)
		addString(&attributes, "operation", operation)
		addString(&attributes, "outcome", event.Outcome)
		succeeded := operationSucceeded(event)
		attributes = append(attributes, slog.Bool("succeeded", succeeded))
		addFailureClass(&attributes, event.FailureClass)
		addDuration(&attributes, "duration", event.Duration)
		if event.Attempts > 0 {
			attributes = append(attributes, slog.Int("attempts", event.Attempts))
		}
		if o.observer.errorDetails && event.Err != nil {
			attributes = append(attributes, slog.Any("error", event.Err))
		}
		attributes = addEventAttributeGroup(attributes, convertedAttributes)

		level := o.observer.levels.OperationFailure
		if succeeded {
			level = o.observer.levels.OperationSuccess
		}
		o.observer.log(ctx, event.EndedAt, level, "clientkit operation completed", attributes)
	})
}

func operationSucceeded(event clientkit.OperationEndEvent) bool {
	return event.Succeeded && event.FailureClass == clientkit.FailureNone && event.Err == nil
}

func attemptSucceeded(event clientkit.AttemptEvent) bool {
	return event.Succeeded && event.FailureClass == clientkit.FailureNone && event.Err == nil
}

func (o *Observer) log(ctx context.Context, at time.Time, level slog.Level, message string, clientkitAttributes []slog.Attr) {
	if o == nil || o.logger == nil {
		return
	}
	handler := o.logger.Handler()
	if handler == nil {
		return
	}
	ctx = contextOrBackground(ctx)
	if !handler.Enabled(ctx, level) {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}

	record := slog.NewRecord(at, level, message, 0)
	record.AddAttrs(o.attributes...)
	record.AddAttrs(slog.Attr{Key: "clientkit", Value: slog.GroupValue(clientkitAttributes...)})
	_ = handler.Handle(ctx, record)
}

func addFailureClass(attributes *[]slog.Attr, failureClass clientkit.FailureClass) {
	if failureClass != clientkit.FailureNone {
		*attributes = append(*attributes, slog.String("failure_class", string(failureClass)))
	}
}

func addEventAttributeGroup(attributes []slog.Attr, eventAttributes []slog.Attr) []slog.Attr {
	if len(eventAttributes) == 0 {
		return attributes
	}
	return append(attributes, slog.Attr{Key: "attributes", Value: slog.GroupValue(eventAttributes...)})
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
