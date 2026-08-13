package httpclient

import (
	"fmt"
	"net/http"
)

// RetrySafety defines whether Clientkit may automatically repeat one logical
// HTTP operation with equivalent method semantics. It applies both to
// Clientkit-scheduled retry attempts and to following method-preserving 307 and
// 308 redirects. RetryConfig.Methods remains an independent gate for scheduled
// retries, and request bodies must still be mechanically replayable. Clientkit
// neither generates nor inspects idempotency keys, and Retry-After cannot
// authorize an otherwise unsafe retry. Repeating a request after a timeout or
// redirect may duplicate side effects unless the remote operation is genuinely
// idempotent or application-level deduplication is in place. RetrySafety does
// not control retries internal to a RoundTripper, intermediaries, or the remote
// system and cannot guarantee exactly-once delivery.
type RetrySafety string

const (
	// RetrySafetyDefault uses built-in HTTP method semantics. GET, HEAD,
	// OPTIONS, TRACE, PUT, and DELETE may pass the retry and 307/308 redirect
	// safety gate; POST, PATCH, CONNECT, and custom methods do not.
	RetrySafetyDefault RetrySafety = ""
	// RetrySafetyNever disables automatic retries and rejects 307/308 redirects
	// for one operation while still allowing its initial Clientkit execution
	// attempt. It does not disable ordinary 301, 302, or 303 redirect handling.
	RetrySafetyNever RetrySafety = "never"
	// RetrySafetyIdempotent asserts that repeating the complete operation through
	// a retry or 307/308 redirect is semantically safe. This is an application
	// assertion, not a Clientkit guarantee, and body replayability and all other
	// applicable policy gates still apply.
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
