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
	// OutcomeHTTPError indicates a rejected response or response-policy failure.
	OutcomeHTTPError Outcome = "http_error"
	// OutcomeTimeout indicates that request execution timed out.
	OutcomeTimeout Outcome = "timeout"
	// OutcomeCanceled indicates that request execution was canceled.
	OutcomeCanceled Outcome = "canceled"
	// OutcomeTransportError indicates another request-execution failure.
	OutcomeTransportError Outcome = "transport_error"
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
// remain caller-owned. Err is reserved for request and transport execution
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
	// must close any open body.
	Response *http.Response
	// StartedAt is the operation start time.
	StartedAt time.Time
	// Duration covers execution through final response headers or terminal error.
	Duration time.Duration
	// Attempts contains one entry for each Clientkit execution attempt.
	Attempts []Attempt
	// Err is the original request or transport execution error.
	Err error
}
