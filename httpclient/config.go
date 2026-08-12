package httpclient

import (
	"net/http"
	"time"

	"github.com/jaredjakacky/clientkit"
)

// Config defines an HTTP client and its independent ordinary-request and
// health-check policies.
type Config struct {
	// Config supplies shared Clientkit identity, readiness, and observation.
	clientkit.Config
	// BaseURL is the required endpoint origin and optional directory path used as
	// NewRequest's RFC 3986 resolution base. Root-relative and parent references
	// may replace or escape that path; it is not a path-confinement boundary.
	BaseURL string
	// HTTPClient replaces the default net/http client when non-nil. Clientkit
	// still applies its contexts and request-origin policy and does not mutate or
	// claim ownership of the supplied client. Its transport is not automatically
	// instrumented; callers can wrap it explicitly with httpclient/otel when
	// physical HTTP spans or standard HTTP metrics are wanted.
	// Client.CloseIdleConnections uses this client directly; if its transport is
	// shared, that call may affect other users of the same idle pool.
	HTTPClient *http.Client
	// Propagator completely replaces the default OpenTelemetry trace propagator
	// when non-nil. Use NopHeaderPropagator to disable propagation or
	// MultiHeaderPropagator to compose propagators explicitly.
	Propagator HeaderPropagator
	// AllowCrossOrigin permits HTTP or HTTPS requests and redirects whose scheme,
	// host, or port differ from BaseURL. This can forward caller-supplied headers
	// to another origin or permit an HTTPS downgrade; the production default
	// rejects it. Non-HTTP URL schemes are always rejected.
	// Pair it with a restrictive CheckRedirect policy when redirects are enabled.
	AllowCrossOrigin bool
	// AllowHostOverride permits Request.Host to differ from Request.URL.Host. The
	// production default rejects host overrides.
	AllowHostOverride bool
	// ResponseClassifier defines ordinary HTTP operation success. Nil accepts
	// 2xx responses through DefaultResponseClassifier. Health checks do not use
	// this policy.
	ResponseClassifier ResponseClassifier
	// Timeout bounds request execution, including retries and retry delays, and
	// remains active for final response-body use. Logical observation and Result
	// duration stop at final response headers or a terminal error. Zero selects
	// DefaultTimeout.
	Timeout time.Duration
	// DisableTimeout intentionally disables Clientkit's total execution timeout.
	DisableTimeout bool
	// AttemptTimeout bounds each Clientkit execution attempt and remains active
	// for final response-body use. One execution attempt may contain multiple
	// RoundTrips because of redirects. Zero selects DefaultAttemptTimeout.
	AttemptTimeout time.Duration
	// DisableAttemptTimeout intentionally disables Clientkit's per-attempt timeout.
	DisableAttemptTimeout bool
	// Check configures explicit health checking. Its zero value is disabled.
	Check CheckConfig
	// Retry configures ordinary requests. Its zero value selects
	// DefaultRetryConfig and does not affect health-check retries.
	Retry RetryConfig
}
