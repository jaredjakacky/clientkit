package clientkit

import (
	"context"
	"time"

	"github.com/jaredjakacky/opskit"
)

// Observer receives backend-neutral client lifecycle events. Implementations
// must be safe for concurrent use. Callbacks are synchronous and should return
// quickly. Event attributes must remain bounded and safe for production
// telemetry.
type Observer interface {
	// StartOperation observes an operation start and may return a derived context
	// used by the operation and its later observer callbacks.
	StartOperation(context.Context, OperationStartEvent) (context.Context, OperationObservation)
	// ObserveAttempt observes one completed execution attempt.
	ObserveAttempt(context.Context, AttemptEvent)
	// ObserveRetry observes one retry that has been scheduled.
	ObserveRetry(context.Context, RetryEvent)
	// ObserveHealth observes one completed health check.
	ObserveHealth(context.Context, HealthEvent)
}

// OperationObservation completes an observation started by Observer.
type OperationObservation interface {
	// End observes the final operation outcome exactly once.
	End(context.Context, OperationEndEvent)
}

// OperationKind describes whether an observed operation coordinates logical
// client policy or directly represents one remote interaction.
type OperationKind uint8

const (
	// OperationKindLogical identifies an operation that may coordinate retries,
	// backoff, classification, or multiple remote interactions.
	OperationKindLogical OperationKind = iota
	// OperationKindRemote identifies an operation that directly represents one
	// remote interaction.
	OperationKindRemote
)

// OperationStartEvent describes the beginning of a client operation. The zero
// Kind is OperationKindLogical for compatibility with ordinary event literals.
type OperationStartEvent struct {
	// Kind identifies the operation boundary for tracing adapters.
	Kind OperationKind
	// Client is the stable configured client name.
	Client string
	// Protocol identifies the client protocol.
	Protocol string
	// Operation identifies the bounded operation type.
	Operation string
	// StartedAt is the UTC operation start time.
	StartedAt time.Time
	// Attributes contains bounded, production-safe operation details.
	Attributes []opskit.Attribute
}

// OperationEndEvent describes the completion of a client operation. Err may be
// recorded by error-aware telemetry but must never be used directly as a metric
// label.
type OperationEndEvent struct {
	// Client is the stable configured client name.
	Client string
	// Protocol identifies the client protocol.
	Protocol string
	// Operation identifies the bounded operation type.
	Operation string
	// StartedAt is the UTC operation start time.
	StartedAt time.Time
	// EndedAt is the UTC operation completion time.
	EndedAt time.Time
	// Duration is the complete operation duration.
	Duration time.Duration
	// Attempts is the number of actual execution attempts.
	Attempts int
	// Outcome is the protocol implementation's bounded final outcome.
	Outcome string
	// Succeeded is the protocol implementation's authoritative decision that the
	// operation met its configured acceptance criteria. A true value requires a
	// nil Err and FailureNone; adapters treat contradictory events as failures.
	Succeeded bool
	// FailureClass is the protocol implementation's stable classified failure.
	// It supplements Outcome and Err and is empty on success.
	FailureClass FailureClass
	// Err is the original terminal error and must not be used as a metric label.
	Err error
	// Attributes contains bounded, production-safe operation details.
	Attributes []opskit.Attribute
}

// AttemptEvent describes one actual protocol execution attempt. Err may be
// recorded by error-aware telemetry but must never be used directly as a metric
// label.
type AttemptEvent struct {
	// Client is the stable configured client name.
	Client string
	// Protocol identifies the client protocol.
	Protocol string
	// Operation identifies the bounded operation type.
	Operation string
	// Number is the one-based attempt number.
	Number int
	// StartedAt is the UTC attempt start time.
	StartedAt time.Time
	// EndedAt is the UTC attempt completion time.
	EndedAt time.Time
	// Duration is the attempt duration.
	Duration time.Duration
	// Outcome is the protocol implementation's bounded attempt outcome.
	Outcome string
	// Succeeded is the protocol implementation's authoritative decision that the
	// attempt met its configured acceptance criteria. A true value requires a nil
	// Err and FailureNone; adapters treat contradictory events as failures.
	Succeeded bool
	// FailureClass is the protocol implementation's stable classified failure.
	// It supplements Outcome and Err and is empty on success.
	FailureClass FailureClass
	// Err is the original attempt error and must not be used as a metric label.
	Err error
	// Attributes contains bounded, production-safe attempt details.
	Attributes []opskit.Attribute
}

// RetryEvent describes one retry that has been scheduled.
type RetryEvent struct {
	// Client is the stable configured client name.
	Client string
	// Protocol identifies the client protocol.
	Protocol string
	// Operation identifies the bounded operation type.
	Operation string
	// AfterAttempt is the completed attempt that caused the retry.
	AfterAttempt int
	// At is the UTC time at which the retry was scheduled.
	At time.Time
	// Delay is the exact selected retry delay.
	Delay time.Duration
	// Cause is the bounded outcome that caused the retry.
	Cause string
	// FailureClass is the stable classified failure that caused the retry.
	FailureClass FailureClass
	// Attributes contains bounded, production-safe retry details.
	Attributes []opskit.Attribute
}

// HealthEvent describes a completed client health check.
type HealthEvent struct {
	// Client is the stable configured client name.
	Client string
	// Protocol identifies the client protocol.
	Protocol string
	// State is the final health state.
	State HealthState
	// FailureClass is the stable classified health-check failure and is empty
	// when no execution failure caused the health state.
	FailureClass FailureClass
	// CheckedAt is the UTC health-check completion time.
	CheckedAt time.Time
	// Duration is the complete health-check duration.
	Duration time.Duration
	// Message is the bounded health result message.
	Message string
	// Attributes contains bounded, production-safe health details.
	Attributes []opskit.Attribute
}

// NopObserver is an Observer that performs no work. Supplying it in a protocol
// client configuration explicitly disables the protocol's default observer.
type NopObserver struct{}

// StartOperation returns the incoming context and a no-op observation.
func (NopObserver) StartOperation(ctx context.Context, _ OperationStartEvent) (context.Context, OperationObservation) {
	return ctx, NopOperationObservation{}
}

// ObserveAttempt performs no work.
func (NopObserver) ObserveAttempt(context.Context, AttemptEvent) {}

// ObserveRetry performs no work.
func (NopObserver) ObserveRetry(context.Context, RetryEvent) {}

// ObserveHealth performs no work.
func (NopObserver) ObserveHealth(context.Context, HealthEvent) {}

// NopOperationObservation is an OperationObservation that performs no work.
type NopOperationObservation struct{}

// End performs no work.
func (NopOperationObservation) End(context.Context, OperationEndEvent) {}

// OperationObservationFunc adapts a function into an OperationObservation.
type OperationObservationFunc func(context.Context, OperationEndEvent)

// End invokes fn when it is non-nil.
func (fn OperationObservationFunc) End(ctx context.Context, event OperationEndEvent) {
	if fn != nil {
		fn(ctx, event)
	}
}

// SafeObserver wraps an observer so telemetry panics cannot affect client
// execution. Event attribute slices are cloned before callbacks, a panic from
// StartOperation preserves the incoming context, and a nil observer becomes
// NopObserver.
func SafeObserver(observer Observer) Observer {
	if observer == nil {
		return NopObserver{}
	}
	switch observer.(type) {
	case NopObserver, safeObserver, multiObserver:
		return observer
	}
	return safeObserver{observer: observer}
}

type safeObserver struct {
	observer Observer
}

func (o safeObserver) StartOperation(ctx context.Context, event OperationStartEvent) (context.Context, OperationObservation) {
	next, observation, ok := startOperationSafely(o.observer, ctx, event)
	if !ok {
		return ctx, NopOperationObservation{}
	}
	if observation == nil {
		observation = NopOperationObservation{}
	}
	return next, safeOperationObservation{observation: observation}
}

func (o safeObserver) ObserveAttempt(ctx context.Context, event AttemptEvent) {
	observeAttemptSafely(o.observer, ctx, event)
}

func (o safeObserver) ObserveRetry(ctx context.Context, event RetryEvent) {
	observeRetrySafely(o.observer, ctx, event)
}

func (o safeObserver) ObserveHealth(ctx context.Context, event HealthEvent) {
	observeHealthSafely(o.observer, ctx, event)
}

type safeOperationObservation struct {
	observation OperationObservation
}

func (o safeOperationObservation) End(ctx context.Context, event OperationEndEvent) {
	endOperationSafely(o.observation, ctx, event)
}

// MultiObserver explicitly composes non-nil observers in registration order.
// Derived operation contexts are chained in the same order, callback panics are
// contained independently, and operation observations end in reverse order to
// support stacked spans and cleanup.
func MultiObserver(observers ...Observer) Observer {
	usable := make([]Observer, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			usable = append(usable, observer)
		}
	}
	if len(usable) == 0 {
		return NopObserver{}
	}
	if len(usable) == 1 {
		return SafeObserver(usable[0])
	}
	return multiObserver{observers: usable}
}

type multiObserver struct {
	observers []Observer
}

func (o multiObserver) StartOperation(ctx context.Context, event OperationStartEvent) (context.Context, OperationObservation) {
	current := ctx
	observations := make([]OperationObservation, 0, len(o.observers))
	for _, observer := range o.observers {
		next, observation, ok := startOperationSafely(observer, current, event)
		if !ok {
			continue
		}
		current = next
		if observation != nil {
			observations = append(observations, observation)
		}
	}
	if len(observations) == 0 {
		return current, NopOperationObservation{}
	}
	return current, multiOperationObservation{observations: observations}
}

func (o multiObserver) ObserveAttempt(ctx context.Context, event AttemptEvent) {
	for _, observer := range o.observers {
		observeAttemptSafely(observer, ctx, event)
	}
}

func (o multiObserver) ObserveRetry(ctx context.Context, event RetryEvent) {
	for _, observer := range o.observers {
		observeRetrySafely(observer, ctx, event)
	}
}

func (o multiObserver) ObserveHealth(ctx context.Context, event HealthEvent) {
	for _, observer := range o.observers {
		observeHealthSafely(observer, ctx, event)
	}
}

type multiOperationObservation struct {
	observations []OperationObservation
}

func (o multiOperationObservation) End(ctx context.Context, event OperationEndEvent) {
	for index := len(o.observations) - 1; index >= 0; index-- {
		endOperationSafely(o.observations[index], ctx, event)
	}
}

func startOperationSafely(observer Observer, ctx context.Context, event OperationStartEvent) (next context.Context, observation OperationObservation, ok bool) {
	next = ctx
	defer func() {
		if recover() != nil {
			next = ctx
			observation = nil
			ok = false
		}
	}()

	returnedContext, observation := observer.StartOperation(ctx, cloneOperationStartEvent(event))
	if returnedContext != nil {
		next = returnedContext
	}
	return next, observation, true
}

func endOperationSafely(observation OperationObservation, ctx context.Context, event OperationEndEvent) {
	defer func() {
		_ = recover()
	}()
	observation.End(ctx, cloneOperationEndEvent(event))
}

func observeAttemptSafely(observer Observer, ctx context.Context, event AttemptEvent) {
	defer func() {
		_ = recover()
	}()
	observer.ObserveAttempt(ctx, cloneAttemptEvent(event))
}

func observeRetrySafely(observer Observer, ctx context.Context, event RetryEvent) {
	defer func() {
		_ = recover()
	}()
	observer.ObserveRetry(ctx, cloneRetryEvent(event))
}

func observeHealthSafely(observer Observer, ctx context.Context, event HealthEvent) {
	defer func() {
		_ = recover()
	}()
	observer.ObserveHealth(ctx, cloneHealthEvent(event))
}

func cloneOperationStartEvent(event OperationStartEvent) OperationStartEvent {
	event.Attributes = cloneObserverAttributes(event.Attributes)
	return event
}

func cloneOperationEndEvent(event OperationEndEvent) OperationEndEvent {
	event.Attributes = cloneObserverAttributes(event.Attributes)
	return event
}

func cloneAttemptEvent(event AttemptEvent) AttemptEvent {
	event.Attributes = cloneObserverAttributes(event.Attributes)
	return event
}

func cloneRetryEvent(event RetryEvent) RetryEvent {
	event.Attributes = cloneObserverAttributes(event.Attributes)
	return event
}

func cloneHealthEvent(event HealthEvent) HealthEvent {
	event.Attributes = cloneObserverAttributes(event.Attributes)
	return event
}

func cloneObserverAttributes(attributes []opskit.Attribute) []opskit.Attribute {
	if attributes == nil {
		return nil
	}
	cloned := make([]opskit.Attribute, len(attributes))
	copy(cloned, attributes)
	return cloned
}
