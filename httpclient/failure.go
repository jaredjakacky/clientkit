package httpclient

import (
	"net/http"

	"github.com/jaredjakacky/clientkit"
	internalfailure "github.com/jaredjakacky/clientkit/internal/failure"
)

type failureStage uint8

const (
	failureStageTransport failureStage = iota
	failureStageConfiguration
	failureStagePolicy
	failureStageRequest
)

func classifyFailure(stage failureStage, response *http.Response, err error) clientkit.FailureClass {
	if stage == failureStageConfiguration {
		return clientkit.FailureConfiguration
	}
	if stage == failureStagePolicy {
		return clientkit.FailurePolicy
	}
	if stage == failureStageRequest {
		return clientkit.FailureRequest
	}
	if isRedirectPolicyFailure(response, err) {
		return clientkit.FailurePolicy
	}
	if isRequestPolicyError(err) {
		return clientkit.FailurePolicy
	}
	if err != nil {
		return internalfailure.Network(err, false)
	}
	if response == nil {
		return clientkit.FailureTransport
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return clientkit.FailureRemoteResponse
	}
	return clientkit.FailureNone
}

// A non-nil response with a non-nil error is net/http's documented signal
// that CheckRedirect rejected a redirect. The response body is already closed.
func isRedirectPolicyFailure(response *http.Response, err error) bool {
	return response != nil && err != nil
}
