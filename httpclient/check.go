package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/internal/configvalue"
	"github.com/jaredjakacky/clientkit/internal/healthrecord"
)

const (
	// DefaultCheckMethod is the default method for enabled HTTP health checks.
	DefaultCheckMethod = http.MethodGet
	// DefaultCheckTimeout is the default outer timeout for an HTTP health check.
	DefaultCheckTimeout = 5 * time.Second
	// DefaultCheckStaleAfter is the default age after which cached HTTP health is
	// stale.
	DefaultCheckStaleAfter = 90 * time.Second
	// DefaultCheckStatus is the default exact HTTP health-check status.
	DefaultCheckStatus = http.StatusOK
)

// CheckConfig configures an explicit HTTP health check. Its zero value disables
// checking. Set Enabled or use DefaultCheckConfig to enable it.
type CheckConfig struct {
	// Enabled allows direct and registry-driven health checking.
	Enabled bool

	// Method is the health-check request method.
	Method string
	// Path is the required relative health-check URL reference. It uses the same
	// RFC 3986 BaseURL resolution semantics as NewRequest.
	Path string

	// Timeout bounds the complete health-check execution.
	Timeout time.Duration
	// DisableTimeout disables the health-check outer timeout.
	DisableTimeout bool

	// StaleAfter controls when cached check health is projected as unknown. It
	// should exceed the maximum expected completion-to-completion refresh gap,
	// including scheduler wait, check-group execution and queueing, positive
	// jitter, and scheduler delay.
	StaleAfter time.Duration
	// DisableStaleAfter disables cached-health staleness projection.
	DisableStaleAfter bool

	// ResponseClassifier defines a healthy response. Nil accepts exactly HTTP
	// 200. The same classifier abstraction and panic containment used by ordinary
	// operations applies to health checks.
	ResponseClassifier ResponseClassifier

	// Retry supplies the independent health-check retry policy. Its zero value
	// performs one attempt with no automatic retries. Assign DefaultRetryConfig or
	// another complete non-zero policy to enable retries explicitly. Retries
	// consume the health-check timeout and may delay unhealthy results.
	Retry RetryConfig
	// RetrySafety controls semantic retry authorization independently from
	// Retry.Methods and body replayability. POST, PATCH, CONNECT, and custom
	// methods require RetrySafetyIdempotent, which is a caller assertion rather
	// than a Clientkit guarantee. RetrySafetyNever disables check retries.
	RetrySafety RetrySafety
}

type normalizedCheckConfig struct {
	enabled     bool
	method      string
	path        string
	timeout     time.Duration
	staleAfter  time.Duration
	classifier  safeResponseClassifier
	retry       RetryConfig
	retrySafety RetrySafety
}

// DefaultCheckConfig returns an enabled, independently mutable health-check
// configuration with production-safe defaults and no automatic retries.
func DefaultCheckConfig(path string) CheckConfig {
	return CheckConfig{
		Enabled: true,
		Path:    path,
	}
}

func normalizeCheckConfig(cfg CheckConfig) (normalizedCheckConfig, error) {
	if !cfg.Enabled {
		if checkConfigHasValues(cfg) {
			return normalizedCheckConfig{}, errors.New("clientkit: HTTP health check configuration requires Check.Enabled")
		}
		return normalizedCheckConfig{}, nil
	}

	method, err := normalizeCheckMethod(cfg.Method)
	if err != nil {
		return normalizedCheckConfig{}, err
	}
	if strings.TrimSpace(cfg.Path) == "" {
		return normalizedCheckConfig{}, errors.New("clientkit: HTTP health check path is required")
	}
	reference, err := url.Parse(cfg.Path)
	if err != nil {
		return normalizedCheckConfig{}, errors.New("clientkit: HTTP health check path is invalid")
	}
	if reference.IsAbs() || reference.Host != "" || reference.Opaque != "" {
		return normalizedCheckConfig{}, errors.New("clientkit: HTTP health check path must be relative")
	}
	if reference.Fragment != "" || reference.RawFragment != "" {
		return normalizedCheckConfig{}, errors.New("clientkit: HTTP health check path must not include a fragment")
	}

	timeout, err := configvalue.Duration("HTTP health check timeout", cfg.Timeout, cfg.DisableTimeout, DefaultCheckTimeout, 0)
	if err != nil {
		return normalizedCheckConfig{}, err
	}

	staleAfter, err := configvalue.Duration("HTTP health check stale-after duration", cfg.StaleAfter, cfg.DisableStaleAfter, DefaultCheckStaleAfter, 0)
	if err != nil {
		return normalizedCheckConfig{}, err
	}

	classifier := cfg.ResponseClassifier
	if classifier == nil {
		classifier = exactStatusResponseClassifier{statusCode: DefaultCheckStatus}
	}

	retry := normalizeCheckRetryConfig(cfg.Retry)
	if err := validateRetryConfig(retry); err != nil {
		return normalizedCheckConfig{}, err
	}
	if err := validateRetrySafety(cfg.RetrySafety); err != nil {
		return normalizedCheckConfig{}, err
	}

	return normalizedCheckConfig{
		enabled:     true,
		method:      method,
		path:        cfg.Path,
		timeout:     timeout,
		staleAfter:  staleAfter,
		classifier:  normalizeResponseClassifier(classifier),
		retry:       retry,
		retrySafety: cfg.RetrySafety,
	}, nil
}

func normalizeCheckRetryConfig(cfg RetryConfig) RetryConfig {
	if retryConfigIsZero(cfg) {
		return cloneRetryConfig(NoRetryConfig())
	}
	return cloneRetryConfig(cfg)
}

func checkConfigHasValues(cfg CheckConfig) bool {
	return cfg.Method != "" ||
		cfg.Path != "" ||
		cfg.Timeout != 0 ||
		cfg.DisableTimeout ||
		cfg.StaleAfter != 0 ||
		cfg.DisableStaleAfter ||
		cfg.ResponseClassifier != nil ||
		!retryConfigIsZero(cfg.Retry) ||
		cfg.RetrySafety != RetrySafetyDefault
}

func normalizeCheckMethod(method string) (string, error) {
	if method == "" {
		return DefaultCheckMethod, nil
	}
	trimmed := strings.TrimSpace(method)
	if trimmed == "" {
		return "", errors.New("clientkit: HTTP health check method must not be empty")
	}
	upper := strings.ToUpper(trimmed)
	switch upper {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect,
		http.MethodOptions, http.MethodTrace:
		trimmed = upper
	}
	if _, err := http.NewRequest(trimmed, "http://clientkit.invalid", nil); err != nil {
		return "", fmt.Errorf("clientkit: invalid HTTP health check method %q: %w", trimmed, err)
	}
	return trimmed, nil
}

// HealthCheckEnabled reports whether active HTTP health checking is enabled.
func (c *Client) HealthCheckEnabled() bool {
	return c != nil && c.core != nil && c.check.enabled
}

// Health returns cached health with read-time staleness projection for enabled
// checks. It never performs network I/O or mutates cached health.
func (c *Client) Health() clientkit.Health {
	if c == nil || c.core == nil {
		return clientkit.Health{State: clientkit.HealthUnknown, Message: "HTTP client health is unavailable"}
	}

	health := c.core.Health()
	if !c.check.enabled {
		return health
	}
	return healthrecord.ProjectStaleness(health, c.check.staleAfter, "HTTP health check")
}

// Check executes one enabled HTTP health check using its independent timeout
// and retry policy. Disabled checks return unknown without network I/O, cache
// mutation, or health telemetry.
func (c *Client) Check(ctx context.Context) clientkit.Health {
	startedAt := time.Now()
	if c == nil || c.core == nil || c.httpClient == nil || c.baseURL == nil {
		return c.completeCheckHealth(ctx, clientkit.HealthUnhealthy, clientkit.FailureConfiguration, "HTTP client is not configured", startedAt, 0)
	}
	if !c.check.enabled {
		return clientkit.Health{State: clientkit.HealthUnknown, Message: "HTTP health check is disabled"}
	}
	if ctx == nil {
		return c.completeCheckHealth(ctx, clientkit.HealthUnhealthy, clientkit.FailureRequest, "HTTP health check context is required", startedAt, 0)
	}

	checkContext := ctx
	cancel := func() {}
	if c.check.timeout > 0 {
		checkContext, cancel = context.WithTimeout(checkContext, c.check.timeout)
	}
	defer cancel()

	request, err := c.NewRequest(checkContext, c.check.method, c.check.path, nil)
	if err != nil {
		return c.completeCheckHealth(checkContext, clientkit.HealthUnhealthy, clientkit.FailureRequest, "HTTP health check request failed", startedAt, 0)
	}

	result := c.do(request, executionPolicy{
		operation:          OperationHTTPRequest,
		attemptTimeout:     c.attemptTimeout,
		retry:              c.check.retry,
		retrySafety:        c.check.retrySafety,
		responseClassifier: c.check.classifier,
		attributes:         httpHealthOperationAttributes(),
	})

	state := clientkit.HealthUnhealthy
	failureClass := result.FailureClass
	message := "HTTP health check request failed"
	switch {
	case result.Err != nil:
		switch result.Outcome {
		case OutcomeTimeout:
			message = "HTTP health check timed out"
		case OutcomeCanceled:
			message = "HTTP health check canceled"
		}
	case result.Response == nil:
		failureClass = clientkit.FailureTransport
		message = "HTTP health check returned no response"
	case result.Outcome == OutcomeSuccess:
		state = clientkit.HealthHealthy
		failureClass = clientkit.FailureNone
		message = "HTTP health check succeeded"
	case result.FailureClass == clientkit.FailurePolicy:
		message = "HTTP health response classification failed"
	default:
		failureClass = clientkit.FailureRemoteResponse
		message = "HTTP health check response was rejected"
	}

	closeResponse(result.Response)
	return c.completeCheckHealth(checkContext, state, failureClass, message, startedAt, result.StatusCode)
}

func (c *Client) completeCheckHealth(ctx context.Context, state clientkit.HealthState, failureClass clientkit.FailureClass, message string, startedAt time.Time, statusCode int) clientkit.Health {
	var client *clientkit.Client
	if c != nil {
		client = c.core
	}
	return healthrecord.Record(client, ctx, ProtocolHTTP, clientkit.HealthAssessment{
		State:        state,
		FailureClass: failureClass,
		Message:      message,
	}, startedAt, httpHealthAttributes(c.checkMethod(), statusCode))
}

func (c *Client) checkMethod() string {
	if c == nil || c.check.method == "" {
		return DefaultCheckMethod
	}
	return c.check.method
}
