package httpclient

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryConfig defines a complete retry policy. Containers define its zero-value
// behavior: Config.Retry selects DefaultRetryConfig, while CheckConfig.Retry
// performs one attempt with no automatic retries. Any non-zero value is a
// complete replacement rather than a merge with defaults. MaxAttempts includes
// the initial request. Requests with non-replayable bodies are never retried.
// RetrySafety is an independent semantic gate: configured methods retry only
// when the operation is intrinsically idempotent or explicitly asserted
// idempotent.
type RetryConfig struct {
	// MaxAttempts is the total attempt limit, including the initial request.
	MaxAttempts int
	// Backoff is the base delay before the first retry.
	Backoff time.Duration
	// BackoffMultiplier controls exponential delay growth between retries.
	BackoffMultiplier float64
	// MaxBackoff caps the policy delay after backoff and jitter are applied.
	MaxBackoff time.Duration
	// Jitter bounds the random positive or negative delay adjustment.
	Jitter time.Duration
	// StatusCodes lists response statuses eligible for retry.
	StatusCodes []int
	// Methods lists exact request methods eligible for retry. RetrySafety and
	// body replayability remain independent gates.
	Methods []string
	// RetryTransportErrors permits retries for non-timeout transport failures.
	RetryTransportErrors bool
	// RetryTimeouts permits retries for attempt-level timeouts while the total
	// operation context remains active.
	RetryTimeouts bool
	// RespectRetryAfter honors delta-seconds and HTTP-date Retry-After values
	// only after the response has already qualified for a retry. Setting it to
	// false disables server-directed retry timing and requires MaxRetryAfter to
	// be zero.
	RespectRetryAfter bool
	// MaxRetryAfter bounds the server-requested delay. Retry-After cannot
	// shorten the configured policy delay or extend the total operation context.
	// It must be positive when RespectRetryAfter is true.
	MaxRetryAfter time.Duration
}

// DefaultRetryConfig returns the production retry policy. Default retries are
// limited to GET, HEAD, OPTIONS, PUT, and DELETE. Retry-After delta-seconds and
// HTTP-date values are honored only for otherwise retryable responses, bounded
// by DefaultMaxRetryAfter, and cannot shorten the policy delay or extend the
// total operation context. Custom configurations are complete replacements;
// callers changing selected defaults should modify this returned value.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:       DefaultRetryMaxAttempts,
		Backoff:           DefaultRetryBackoff,
		BackoffMultiplier: DefaultRetryBackoffMultiplier,
		MaxBackoff:        DefaultRetryMaxBackoff,
		Jitter:            DefaultRetryJitter,
		RespectRetryAfter: DefaultRespectRetryAfter,
		MaxRetryAfter:     DefaultMaxRetryAfter,
		StatusCodes: []int{
			http.StatusRequestTimeout,
			http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout,
		},
		Methods: []string{
			http.MethodGet,
			http.MethodHead,
			http.MethodOptions,
			http.MethodPut,
			http.MethodDelete,
		},
		RetryTransportErrors: true,
		RetryTimeouts:        true,
	}
}

// NoRetryConfig disables retries by allowing one total attempt. It retains
// valid bounded Retry-After defaults, although they cannot be consulted.
func NoRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:       1,
		RespectRetryAfter: DefaultRespectRetryAfter,
		MaxRetryAfter:     DefaultMaxRetryAfter,
	}
}

func normalizeRetryConfig(cfg RetryConfig) RetryConfig {
	if retryConfigIsZero(cfg) {
		cfg = DefaultRetryConfig()
	}
	return cloneRetryConfig(cfg)
}

func cloneRetryConfig(cfg RetryConfig) RetryConfig {
	normalized := cfg
	if cfg.StatusCodes != nil {
		normalized.StatusCodes = append([]int(nil), cfg.StatusCodes...)
	}
	if cfg.Methods != nil {
		normalized.Methods = append([]string(nil), cfg.Methods...)
	}
	return normalized
}

func validateRetryConfig(cfg RetryConfig) error {
	if cfg.MaxAttempts < 1 {
		return errors.New("clientkit: retry max attempts must be at least 1")
	}
	if cfg.Backoff < 0 {
		return errors.New("clientkit: retry backoff must not be negative")
	}
	if math.IsNaN(cfg.BackoffMultiplier) || math.IsInf(cfg.BackoffMultiplier, 0) || cfg.BackoffMultiplier < 0 {
		return errors.New("clientkit: retry backoff multiplier must be finite and not negative")
	}
	if cfg.MaxBackoff < 0 {
		return errors.New("clientkit: retry max backoff must not be negative")
	}
	if cfg.Jitter < 0 {
		return errors.New("clientkit: retry jitter must not be negative")
	}
	if cfg.MaxRetryAfter < 0 {
		return errors.New("clientkit: retry max Retry-After must not be negative")
	}
	if cfg.RespectRetryAfter && cfg.MaxRetryAfter <= 0 {
		return errors.New("clientkit: retry max Retry-After must be positive when Retry-After is enabled")
	}
	if !cfg.RespectRetryAfter && cfg.MaxRetryAfter > 0 {
		return errors.New("clientkit: retry max Retry-After must be zero when Retry-After is disabled")
	}

	statusCodes := make(map[int]struct{}, len(cfg.StatusCodes))
	for _, statusCode := range cfg.StatusCodes {
		if statusCode < 100 || statusCode > 599 {
			return fmt.Errorf("clientkit: retry status code %d must be between 100 and 599", statusCode)
		}
		if _, exists := statusCodes[statusCode]; exists {
			return fmt.Errorf("clientkit: duplicate retry status code %d", statusCode)
		}
		statusCodes[statusCode] = struct{}{}
	}

	methods := make(map[string]struct{}, len(cfg.Methods))
	for _, method := range cfg.Methods {
		if strings.TrimSpace(method) == "" {
			return errors.New("clientkit: retry method must not be empty")
		}
		if _, err := http.NewRequest(method, "http://clientkit.invalid", nil); err != nil {
			return fmt.Errorf("clientkit: invalid retry method %q: %w", method, err)
		}
		if _, exists := methods[method]; exists {
			return fmt.Errorf("clientkit: duplicate retry method %q", method)
		}
		methods[method] = struct{}{}
	}

	return nil
}

func retryConfigIsZero(cfg RetryConfig) bool {
	return cfg.MaxAttempts == 0 &&
		cfg.Backoff == 0 &&
		cfg.BackoffMultiplier == 0 &&
		cfg.MaxBackoff == 0 &&
		cfg.Jitter == 0 &&
		cfg.StatusCodes == nil &&
		cfg.Methods == nil &&
		!cfg.RetryTransportErrors &&
		!cfg.RetryTimeouts &&
		!cfg.RespectRetryAfter &&
		cfg.MaxRetryAfter == 0
}

func (cfg RetryConfig) shouldRetry(method string, outcome Outcome, statusCode int) bool {
	if !cfg.allowsMethod(method) {
		return false
	}

	switch outcome {
	case OutcomeResponseRejected:
		return cfg.allowsStatusCode(statusCode)
	case OutcomeTimeout:
		return cfg.RetryTimeouts
	case OutcomeExecutionError:
		return cfg.RetryTransportErrors
	default:
		return false
	}
}

func (cfg RetryConfig) allowsMethod(method string) bool {
	for _, allowedMethod := range cfg.Methods {
		if method == allowedMethod {
			return true
		}
	}

	return false
}

func (cfg RetryConfig) allowsStatusCode(statusCode int) bool {
	for _, allowedStatusCode := range cfg.StatusCodes {
		if statusCode == allowedStatusCode {
			return true
		}
	}

	return false
}

func (cfg RetryConfig) baseRetryDelay(retryNumber int) time.Duration {
	if retryNumber <= 0 || cfg.Backoff <= 0 {
		return 0
	}

	delay := float64(cfg.Backoff) * math.Pow(cfg.BackoffMultiplier, float64(retryNumber-1))
	if math.IsNaN(delay) || delay <= 0 {
		return 0
	}

	if cfg.MaxBackoff > 0 && delay >= float64(cfg.MaxBackoff) {
		return cfg.MaxBackoff
	}

	const maxDuration = time.Duration(1<<63 - 1)
	if math.IsInf(delay, 1) || delay >= float64(maxDuration) {
		return maxDuration
	}

	return time.Duration(delay)
}

func (cfg RetryConfig) retryDelay(retryNumber int) time.Duration {
	base := cfg.baseRetryDelay(retryNumber)
	if base <= 0 {
		return 0
	}
	if cfg.Jitter <= 0 {
		return base
	}

	offset := (rand.Float64()*2 - 1) * float64(cfg.Jitter)
	delay := float64(base) + offset
	if math.IsNaN(delay) || delay <= 0 {
		return 0
	}

	if cfg.MaxBackoff > 0 && delay >= float64(cfg.MaxBackoff) {
		return cfg.MaxBackoff
	}

	const maxDuration = time.Duration(1<<63 - 1)
	if math.IsInf(delay, 1) || delay >= float64(maxDuration) {
		return maxDuration
	}

	return time.Duration(delay)
}

func retryAfterDelay(response *http.Response, receivedAt time.Time) (time.Duration, bool) {
	if response == nil {
		return 0, false
	}

	for _, value := range response.Header.Values("Retry-After") {
		if delay, ok := parseRetryAfter(value, receivedAt); ok {
			return delay, true
		}
	}

	return 0, false
}

func parseRetryAfter(value string, receivedAt time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	deltaSeconds := true
	for _, character := range value {
		if character < '0' || character > '9' {
			deltaSeconds = false
			break
		}
	}
	if deltaSeconds {
		seconds, err := strconv.ParseUint(value, 10, 64)
		const maxDuration = time.Duration(1<<63 - 1)
		if err != nil || seconds > uint64(maxDuration/time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}

	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	if !retryAt.After(receivedAt) {
		return 0, true
	}

	const maxDuration = time.Duration(1<<63 - 1)
	if retryAt.After(receivedAt.Add(maxDuration)) {
		return 0, false
	}
	return retryAt.Sub(receivedAt), true
}

func waitForRetryDelay(ctx context.Context, delay time.Duration) error {
	if ctx == nil {
		return errors.New("clientkit: context is required")
	}

	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
