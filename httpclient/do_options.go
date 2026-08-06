package httpclient

import (
	"errors"
	"time"

	"github.com/jaredjakacky/clientkit/internal/configvalue"
)

// DoOptions supplies explicit per-request response, retry, retry-safety, and
// timeout policy. A non-nil ResponseClassifier completely overrides the client
// classifier for this call; a zero ExecutionRetry inherits the client retry
// policy, RetrySafetyDefault uses built-in HTTP method semantics, and zero
// timeout fields inherit client-level values.
type DoOptions struct {
	// Operation supplies a stable, low-cardinality semantic name for this logical
	// execution. Zero uses OperationHTTPRequest. Custom names use the restricted
	// lowercase OperationName syntax and appear in spans, metrics, retry events,
	// and structured logs. They do not affect execution, are not sent remotely,
	// and must never be derived from URLs, paths, IDs, or user input. The value is
	// resolved per call without mutating Client and is safe for concurrent use.
	Operation OperationName
	// ResponseClassifier overrides Config.ResponseClassifier for this operation.
	// Nil uses the immutable client-level classifier.
	ResponseClassifier ResponseClassifier
	// Retry overrides the Client retry policy for this operation. Its zero value
	// inherits the immutable client policy. Config is a complete replacement,
	// while Disable performs one attempt without scheduling retries. RetrySafety
	// and request-body replayability remain independent requirements. Overrides
	// are resolved per call without mutating Client, and health-check retry policy
	// is unaffected.
	Retry ExecutionRetry
	// RetrySafety controls whether repeating this operation is semantically
	// authorized. RetryConfig.Methods and body replayability remain independent
	// requirements. POST, PATCH, CONNECT, and custom methods require
	// RetrySafetyIdempotent, which is a caller assertion rather than a Clientkit
	// guarantee. RetrySafetyNever disables retries for this operation.
	RetrySafety RetrySafety
	// Timeouts overrides the client's total and per-attempt timeout policy
	// field-by-field. Zero fields inherit client values. The total timeout spans
	// retries and delays; the attempt timeout restarts for every attempt and
	// continues through final response-body use. Disable flags remove the
	// corresponding Clientkit timeout without detaching caller or observer-derived
	// deadlines, and the earliest context deadline wins. Per-operation values do
	// not mutate Client; final response bodies must still be closed.
	Timeouts ExecutionTimeouts
}

// ExecutionRetry supplies a retry-policy override for one logical HTTP
// operation. Its zero value inherits the Client's normalized retry policy.
// Config is a complete replacement, while Disable performs the initial attempt
// without scheduling retries; Config and Disable cannot be combined.
//
// Per-operation policies are normalized before execution and never mutate the
// Client, making concurrent calls with different policies safe. RetrySafety
// remains an independent semantic gate, request-body replayability remains
// required, and Retry-After behavior comes from the selected policy. The total
// operation timeout remains authoritative, and health-check retry policy is
// unaffected.
type ExecutionRetry struct {
	// Config completely replaces the Client retry policy for this operation.
	// Its zero value means no custom policy was supplied. The configuration is
	// normalized and its slices are cloned before execution begins.
	Config RetryConfig
	// Disable performs one network attempt and schedules no automatic retries.
	// It cannot be combined with Config.
	Disable bool
}

type executionRetryPolicy struct {
	config RetryConfig
	source string
}

const (
	retryPolicySourceClient    = "client"
	retryPolicySourceOperation = "operation"
)

func resolveExecutionRetry(clientPolicy RetryConfig, override ExecutionRetry) (executionRetryPolicy, error) {
	hasCustomPolicy := !retryConfigIsZero(override.Config)
	if override.Disable && hasCustomPolicy {
		return executionRetryPolicy{}, errors.New("clientkit: operation retry policy cannot be set and disabled")
	}

	if override.Disable {
		return executionRetryPolicy{
			config: cloneRetryConfig(NoRetryConfig()),
			source: retryPolicySourceOperation,
		}, nil
	}

	if !hasCustomPolicy {
		return executionRetryPolicy{
			config: cloneRetryConfig(clientPolicy),
			source: retryPolicySourceClient,
		}, nil
	}

	config := normalizeRetryConfig(override.Config)
	if err := validateRetryConfig(config); err != nil {
		return executionRetryPolicy{}, err
	}

	return executionRetryPolicy{
		config: config,
		source: retryPolicySourceOperation,
	}, nil
}

// ExecutionTimeouts supplies field-by-field timeout overrides for one logical
// HTTP operation. Zero values inherit the client's normalized policy. Positive
// values replace one corresponding timeout, while disable flags remove that
// Clientkit timeout without detaching caller or observer-derived contexts. The
// earliest context deadline wins naturally. Overrides are immutable per call,
// safe for concurrent use, and do not mutate Client or http.Client. Final
// response bodies must still be closed by callers.
type ExecutionTimeouts struct {
	// Timeout overrides the total logical-operation timeout when positive. Zero
	// inherits the client value. The total timeout spans attempts, retry delays,
	// Retry-After delays, and final response-body use. Negative values are
	// invalid, and a positive value cannot be combined with DisableTimeout.
	Timeout time.Duration
	// DisableTimeout removes Clientkit's total timeout for this operation. Caller
	// and observer-derived cancellation and deadlines remain authoritative.
	DisableTimeout bool
	// AttemptTimeout overrides the timeout independently applied to every actual
	// network attempt, including final response-body use, when positive. Zero
	// inherits the client value, and a fresh attempt timeout begins for every
	// retry. Negative values are invalid, and a positive value cannot be combined
	// with DisableAttemptTimeout.
	AttemptTimeout time.Duration
	// DisableAttemptTimeout removes Clientkit's per-attempt timeout for this
	// operation without disabling its total, caller, or observer-derived context.
	DisableAttemptTimeout bool
}

type executionTimeoutPolicy struct {
	timeout        time.Duration
	attemptTimeout time.Duration
}

func resolveExecutionTimeouts(clientTimeout time.Duration, clientAttemptTimeout time.Duration, overrides ExecutionTimeouts) (executionTimeoutPolicy, error) {
	timeout, err := configvalue.Duration("operation timeout", overrides.Timeout, overrides.DisableTimeout, clientTimeout, 0)
	if err != nil {
		return executionTimeoutPolicy{}, err
	}
	attemptTimeout, err := configvalue.Duration("operation attempt timeout", overrides.AttemptTimeout, overrides.DisableAttemptTimeout, clientAttemptTimeout, 0)
	if err != nil {
		return executionTimeoutPolicy{}, err
	}

	return executionTimeoutPolicy{
		timeout:        timeout,
		attemptTimeout: attemptTimeout,
	}, nil
}
