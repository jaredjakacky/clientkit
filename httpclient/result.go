package httpclient

import (
	"net/http"
	"time"

	"github.com/jaredjakacky/clientkit"
)

// Outcome classifies one HTTP operation using bounded values.
type Outcome string

const (
	// OutcomeSuccess indicates an accepted response.
	OutcomeSuccess Outcome = "success"
	// OutcomeResponseRejected indicates that a completed response was rejected
	// by the configured response classifier.
	OutcomeResponseRejected Outcome = "response_rejected"
	// OutcomeTimeout indicates that request execution timed out.
	OutcomeTimeout Outcome = "timeout"
	// OutcomeCanceled indicates that request execution was canceled.
	OutcomeCanceled Outcome = "canceled"
	// OutcomeExecutionError indicates another request-execution failure.
	// FailureClass identifies whether configuration, request, policy, or
	// transport behavior caused the failure.
	OutcomeExecutionError Outcome = "execution_error"
)

// Attempt describes one Clientkit HTTP execution attempt. Redirects may cause
// more than one transport RoundTrip within one execution attempt.
type Attempt struct {
	// Number is the one-based execution-attempt number.
	Number int
	// Outcome is the bounded result of this attempt.
	Outcome Outcome
	// FailureClass is the stable classified attempt failure and supplements Err
	// and Outcome without replacing either.
	FailureClass clientkit.FailureClass
	// StatusCode is the received HTTP status, or zero when no response arrived.
	StatusCode int
	// StartedAt is the time at which transport execution began.
	StartedAt time.Time
	// Duration covers transport execution through response headers or error.
	Duration time.Duration
	// Err is the original request or transport execution error.
	Err error
}

// Result describes one completed HTTP operation. A final Response and its body
// remain caller-owned. Err reports setup, request, and transport execution
// failures; response rejection and classifier policy failures use Outcome and
// FailureClass without synthesizing an error.
type Result struct {
	// Outcome is the bounded final operation result.
	Outcome Outcome
	// FailureClass is the stable classified operation failure and supplements
	// Err and Outcome without replacing either.
	FailureClass clientkit.FailureClass
	// StatusCode is the final response status, or zero when no response exists.
	StatusCode int
	// Response is the final caller-owned response, when one exists. The caller
	// must close any open body. When Err reports redirect-policy rejection,
	// net/http returns the redirect response with its body already closed.
	Response *http.Response
	// StartedAt is the operation start time.
	StartedAt time.Time
	// Duration covers execution through final response headers or terminal error.
	Duration time.Duration
	// Attempts contains one entry for each Clientkit execution attempt.
	Attempts []Attempt
	// Err is the original setup, request, or transport execution error. Response
	// rejection and classifier policy failures do not synthesize errors.
	Err error
}
