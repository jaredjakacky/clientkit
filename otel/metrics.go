package otel

import (
	"context"
	"fmt"

	"github.com/jaredjakacky/clientkit"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type instruments struct {
	operations        metric.Int64Counter
	operationDuration metric.Float64Histogram
	operationAttempts metric.Int64Histogram
	attempts          metric.Int64Counter
	attemptDuration   metric.Float64Histogram
	retries           metric.Int64Counter
	retryDelay        metric.Float64Histogram
	healthChecks      metric.Int64Counter
	healthDuration    metric.Float64Histogram
}

func newInstruments(meter metric.Meter) (instruments, error) {
	var values instruments
	var err error

	values.operations, err = meter.Int64Counter("clientkit.operations", metric.WithUnit("{operation}"), metric.WithDescription("Completed client operations."))
	if err != nil {
		return instruments{}, fmt.Errorf("create clientkit.operations: %w", err)
	}
	values.operationDuration, err = meter.Float64Histogram("clientkit.operation.duration", metric.WithUnit("s"), metric.WithDescription("Client operation duration."))
	if err != nil {
		return instruments{}, fmt.Errorf("create clientkit.operation.duration: %w", err)
	}
	values.operationAttempts, err = meter.Int64Histogram("clientkit.operation.attempts", metric.WithUnit("{attempt}"), metric.WithDescription("Attempts per client operation."))
	if err != nil {
		return instruments{}, fmt.Errorf("create clientkit.operation.attempts: %w", err)
	}
	values.attempts, err = meter.Int64Counter("clientkit.attempts", metric.WithUnit("{attempt}"), metric.WithDescription("Completed client attempts."))
	if err != nil {
		return instruments{}, fmt.Errorf("create clientkit.attempts: %w", err)
	}
	values.attemptDuration, err = meter.Float64Histogram("clientkit.attempt.duration", metric.WithUnit("s"), metric.WithDescription("Client attempt duration."))
	if err != nil {
		return instruments{}, fmt.Errorf("create clientkit.attempt.duration: %w", err)
	}
	values.retries, err = meter.Int64Counter("clientkit.retries", metric.WithUnit("{retry}"), metric.WithDescription("Scheduled client retries."))
	if err != nil {
		return instruments{}, fmt.Errorf("create clientkit.retries: %w", err)
	}
	values.retryDelay, err = meter.Float64Histogram("clientkit.retry.delay", metric.WithUnit("s"), metric.WithDescription("Selected client retry delay."))
	if err != nil {
		return instruments{}, fmt.Errorf("create clientkit.retry.delay: %w", err)
	}
	values.healthChecks, err = meter.Int64Counter("clientkit.health.checks", metric.WithUnit("{check}"), metric.WithDescription("Completed client health checks."))
	if err != nil {
		return instruments{}, fmt.Errorf("create clientkit.health.checks: %w", err)
	}
	values.healthDuration, err = meter.Float64Histogram("clientkit.health.duration", metric.WithUnit("s"), metric.WithDescription("Client health-check duration."))
	if err != nil {
		return instruments{}, fmt.Errorf("create clientkit.health.duration: %w", err)
	}

	return values, nil
}

func (o *Observer) recordOperation(ctx context.Context, event clientkit.OperationEndEvent) {
	attributes := o.metricAttributes(event.Attributes, identityAttributes(event.Client, event.Protocol, event.Operation), []attribute.KeyValue{
		attribute.String(AttributeOutcome, event.Outcome),
		attribute.Bool(AttributeSucceeded, operationSucceeded(event)),
	}, failureClassAttributes(event.FailureClass))
	options := metric.WithAttributes(attributes...)
	o.metrics.operations.Add(ctx, 1, options)
	if event.Duration >= 0 {
		o.metrics.operationDuration.Record(ctx, event.Duration.Seconds(), options)
	}
	if event.Attempts >= 0 {
		o.metrics.operationAttempts.Record(ctx, int64(event.Attempts), options)
	}
}

func (o *Observer) recordAttempt(ctx context.Context, event clientkit.AttemptEvent) {
	attributes := o.metricAttributes(event.Attributes, identityAttributes(event.Client, event.Protocol, event.Operation), []attribute.KeyValue{
		attribute.String(AttributeOutcome, event.Outcome),
		attribute.Bool(AttributeSucceeded, attemptSucceeded(event)),
	}, failureClassAttributes(event.FailureClass))
	options := metric.WithAttributes(attributes...)
	o.metrics.attempts.Add(ctx, 1, options)
	if event.Duration >= 0 {
		o.metrics.attemptDuration.Record(ctx, event.Duration.Seconds(), options)
	}
}

func (o *Observer) recordRetry(ctx context.Context, event clientkit.RetryEvent) {
	attributes := o.metricAttributes(event.Attributes, identityAttributes(event.Client, event.Protocol, event.Operation), []attribute.KeyValue{
		attribute.String(AttributeRetryCause, event.Cause),
	}, failureClassAttributes(event.FailureClass))
	options := metric.WithAttributes(attributes...)
	o.metrics.retries.Add(ctx, 1, options)
	if event.Delay >= 0 {
		o.metrics.retryDelay.Record(ctx, event.Delay.Seconds(), options)
	}
}

func (o *Observer) recordHealth(ctx context.Context, event clientkit.HealthEvent) {
	attributes := o.metricAttributes(event.Attributes, []attribute.KeyValue{
		attribute.String(AttributeClientName, event.Client),
		attribute.String(AttributeProtocol, event.Protocol),
		attribute.String(AttributeHealthState, string(event.State)),
	}, failureClassAttributes(event.FailureClass))
	options := metric.WithAttributes(attributes...)
	o.metrics.healthChecks.Add(ctx, 1, options)
	if event.Duration >= 0 {
		o.metrics.healthDuration.Record(ctx, event.Duration.Seconds(), options)
	}
}
