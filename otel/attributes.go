package otel

import (
	"strings"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/opskit"
	"go.opentelemetry.io/otel/attribute"
)

// Clientkit-owned OpenTelemetry attribute names.
const (
	// AttributeClientName identifies the configured logical client.
	AttributeClientName = "clientkit.client.name"
	// AttributeProtocol identifies the bounded protocol implementation.
	AttributeProtocol = "clientkit.protocol"
	// AttributeOperation identifies the bounded logical operation.
	AttributeOperation = "clientkit.operation"
	// AttributeOutcome records the protocol-defined bounded outcome.
	AttributeOutcome = "clientkit.outcome"
	// AttributeSucceeded records the adapter-normalized success decision.
	AttributeSucceeded = "clientkit.succeeded"
	// AttributeOperationAttempts records Clientkit execution attempts in an operation.
	AttributeOperationAttempts = "clientkit.operation.attempts"
	// AttributeAttemptNumber records a one-based attempt number.
	AttributeAttemptNumber = "clientkit.attempt.number"
	// AttributeRetryAfterAttempt identifies the attempt that scheduled a retry.
	AttributeRetryAfterAttempt = "clientkit.retry.after_attempt"
	// AttributeRetryCause records the bounded retry cause.
	AttributeRetryCause = "clientkit.retry.cause"
	// AttributeRetryDelay records the selected retry delay in seconds.
	AttributeRetryDelay = "clientkit.retry.delay"
	// AttributeHealthState records the bounded dependency-health state.
	AttributeHealthState = "clientkit.health.state"
	// AttributeFailureClass identifies the stable Clientkit failure class.
	AttributeFailureClass = "clientkit.failure.class"
)

func convertAttributes(attributes []opskit.Attribute) []attribute.KeyValue {
	converted := make([]attribute.KeyValue, 0, len(attributes))
	for _, value := range attributes {
		// Failure classification is adapter-owned so common or protocol
		// attributes cannot override the event's authoritative value.
		if strings.TrimSpace(value.Key) == "" ||
			value.Key == "client.failure_class" ||
			value.Key == AttributeFailureClass {
			continue
		}
		converted = append(converted, attribute.String(value.Key, value.Value))
	}
	return converted
}

func withoutFailureClass(attributes []attribute.KeyValue) []attribute.KeyValue {
	filtered := make([]attribute.KeyValue, 0, len(attributes))
	for _, value := range attributes {
		if value.Key != attribute.Key(AttributeFailureClass) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func failureClassAttributes(failureClass clientkit.FailureClass) []attribute.KeyValue {
	if failureClass == clientkit.FailureNone {
		return nil
	}
	return []attribute.KeyValue{attribute.String(AttributeFailureClass, string(failureClass))}
}

func mergeAttributes(groups ...[]attribute.KeyValue) []attribute.KeyValue {
	count := 0
	for _, group := range groups {
		count += len(group)
	}

	// Later groups replace earlier values without changing key order. This lets
	// Clientkit-owned identity and outcome fields override common attributes.
	merged := make([]attribute.KeyValue, 0, count)
	positions := make(map[attribute.Key]int, count)
	for _, group := range groups {
		for _, value := range group {
			if strings.TrimSpace(string(value.Key)) == "" {
				continue
			}
			if index, exists := positions[value.Key]; exists {
				merged[index] = value
				continue
			}
			positions[value.Key] = len(merged)
			merged = append(merged, value)
		}
	}
	return merged
}

func identityAttributes(client, protocol, operation string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(AttributeClientName, client),
		attribute.String(AttributeProtocol, protocol),
		attribute.String(AttributeOperation, operation),
	}
}
