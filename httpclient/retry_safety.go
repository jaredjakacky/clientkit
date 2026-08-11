package httpclient

import (
	"fmt"
	"net/http"
)

// RetrySafety defines whether repeating one logical HTTP operation is
// semantically authorized. RetryConfig.Methods remains an independent gate,
// and request bodies must still be mechanically replayable. Clientkit neither
// generates nor inspects idempotency keys, and Retry-After cannot authorize an
// otherwise unsafe retry. Repeating a request after a timeout may duplicate
// side effects unless the remote operation is genuinely idempotent or
// application-level deduplication is in place.
type RetrySafety string

const (
	// RetrySafetyDefault uses built-in HTTP method semantics. GET, HEAD,
	// OPTIONS, TRACE, PUT, and DELETE may pass the safety gate; POST, PATCH,
	// CONNECT, and custom methods do not.
	RetrySafetyDefault RetrySafety = ""
	// RetrySafetyNever disables automatic retries for one operation while still
	// allowing its initial Clientkit execution attempt.
	RetrySafetyNever RetrySafety = "never"
	// RetrySafetyIdempotent asserts that repeating the complete operation is
	// semantically safe. This is an application assertion, not a Clientkit
	// guarantee, and body replayability and all other retry gates still apply.
	RetrySafetyIdempotent RetrySafety = "idempotent"
)

type retryAuthorization string

const (
	retryAuthorizationMethod   retryAuthorization = "method"
	retryAuthorizationExplicit retryAuthorization = "explicit"
	retryAuthorizationDenied   retryAuthorization = "denied"
	retryAuthorizationDisabled retryAuthorization = "disabled"
)

func retryAuthorizationFor(method string, safety RetrySafety) (retryAuthorization, error) {
	switch safety {
	case RetrySafetyDefault:
		if methodIsIdempotent(method) {
			return retryAuthorizationMethod, nil
		}
		return retryAuthorizationDenied, nil
	case RetrySafetyNever:
		return retryAuthorizationDisabled, nil
	case RetrySafetyIdempotent:
		return retryAuthorizationExplicit, nil
	default:
		return retryAuthorizationDenied, invalidRetrySafetyError(safety)
	}
}

func validateRetrySafety(safety RetrySafety) error {
	switch safety {
	case RetrySafetyDefault, RetrySafetyNever, RetrySafetyIdempotent:
		return nil
	default:
		return invalidRetrySafetyError(safety)
	}
}

func invalidRetrySafetyError(safety RetrySafety) error {
	return fmt.Errorf("clientkit: invalid HTTP retry safety %q", safety)
}

func (authorization retryAuthorization) allowsRetry() bool {
	return authorization == retryAuthorizationMethod || authorization == retryAuthorizationExplicit
}

func methodIsIdempotent(method string) bool {
	switch normalizedHTTPMethod(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace,
		http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}
