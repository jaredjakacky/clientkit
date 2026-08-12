package httpclient_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"reflect"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPRetryDefaultsAreIndependent(t *testing.T) {
	first := httpclient.DefaultRetryConfig()
	second := httpclient.DefaultRetryConfig()
	if first.MaxAttempts != httpclient.DefaultRetryMaxAttempts ||
		first.Backoff != httpclient.DefaultRetryBackoff ||
		first.BackoffMultiplier != httpclient.DefaultRetryBackoffMultiplier ||
		first.MaxBackoff != httpclient.DefaultRetryMaxBackoff ||
		first.Jitter != httpclient.DefaultRetryJitter ||
		first.RespectRetryAfter != httpclient.DefaultRespectRetryAfter ||
		first.MaxRetryAfter != httpclient.DefaultMaxRetryAfter {
		t.Fatalf("DefaultRetryConfig() = %#v, want documented defaults", first)
	}
	first.Methods[0] = http.MethodPost
	first.StatusCodes[0] = http.StatusTeapot
	if reflect.DeepEqual(first.Methods, second.Methods) || reflect.DeepEqual(first.StatusCodes, second.StatusCodes) {
		t.Fatal("DefaultRetryConfig() returned shared policy slices")
	}

	noRetry := httpclient.NoRetryConfig()
	if noRetry.MaxAttempts != 1 || !noRetry.RespectRetryAfter || noRetry.MaxRetryAfter != httpclient.DefaultMaxRetryAfter {
		t.Fatalf("NoRetryConfig() = %#v, want one valid attempt", noRetry)
	}
}

func TestHTTPRetryConfigValidation(t *testing.T) {
	base := httpclient.Config{
		Config:     clientkit.Config{Name: "retry-validation", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test",
		Propagator: httpclient.NopHeaderPropagator{},
	}
	tests := []struct {
		name  string
		retry httpclient.RetryConfig
	}{
		{name: "zero attempts in explicit policy", retry: httpclient.RetryConfig{Backoff: time.Millisecond}},
		{name: "negative backoff", retry: httpclient.RetryConfig{MaxAttempts: 1, Backoff: -time.Nanosecond}},
		{name: "negative multiplier", retry: httpclient.RetryConfig{MaxAttempts: 1, BackoffMultiplier: -1}},
		{name: "NaN multiplier", retry: httpclient.RetryConfig{MaxAttempts: 1, BackoffMultiplier: math.NaN()}},
		{name: "infinite multiplier", retry: httpclient.RetryConfig{MaxAttempts: 1, BackoffMultiplier: math.Inf(1)}},
		{name: "negative maximum backoff", retry: httpclient.RetryConfig{MaxAttempts: 1, MaxBackoff: -time.Nanosecond}},
		{name: "negative jitter", retry: httpclient.RetryConfig{MaxAttempts: 1, Jitter: -time.Nanosecond}},
		{name: "negative maximum Retry-After", retry: httpclient.RetryConfig{MaxAttempts: 1, MaxRetryAfter: -time.Nanosecond}},
		{name: "Retry-After missing maximum", retry: httpclient.RetryConfig{MaxAttempts: 1, RespectRetryAfter: true}},
		{name: "Retry-After maximum while disabled", retry: httpclient.RetryConfig{MaxAttempts: 1, MaxRetryAfter: time.Second}},
		{name: "status below range", retry: httpclient.RetryConfig{MaxAttempts: 1, StatusCodes: []int{99}}},
		{name: "status above range", retry: httpclient.RetryConfig{MaxAttempts: 1, StatusCodes: []int{600}}},
		{name: "duplicate status", retry: httpclient.RetryConfig{MaxAttempts: 1, StatusCodes: []int{500, 500}}},
		{name: "blank method", retry: httpclient.RetryConfig{MaxAttempts: 1, Methods: []string{" "}}},
		{name: "invalid method", retry: httpclient.RetryConfig{MaxAttempts: 1, Methods: []string{"GET\n"}}},
		{name: "duplicate method", retry: httpclient.RetryConfig{MaxAttempts: 1, Methods: []string{http.MethodGet, http.MethodGet}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			cfg.Retry = test.retry
			if client, err := httpclient.New(cfg); err == nil || client != nil {
				t.Fatalf("New() = (%v, %v), want retry validation failure", client, err)
			}
		})
	}
}

func TestHTTPRetriesConfiguredTransportFailures(t *testing.T) {
	tests := []struct {
		name                 string
		firstErr             error
		retryTransportErrors bool
		retryTimeouts        bool
		wantAttempts         int
		wantOutcome          httpclient.Outcome
	}{
		{name: "transport retry", firstErr: errors.New("transport unavailable"), retryTransportErrors: true, wantAttempts: 2, wantOutcome: httpclient.OutcomeSuccess},
		{name: "transport disabled", firstErr: errors.New("transport unavailable"), wantAttempts: 1, wantOutcome: httpclient.OutcomeExecutionError},
		{name: "timeout retry", firstErr: retryTimeoutError{}, retryTimeouts: true, wantAttempts: 2, wantOutcome: httpclient.OutcomeSuccess},
		{name: "timeout disabled", firstErr: retryTimeoutError{}, wantAttempts: 1, wantOutcome: httpclient.OutcomeTimeout},
		{name: "cancellation is never retried", firstErr: context.Canceled, retryTransportErrors: true, retryTimeouts: true, wantAttempts: 1, wantOutcome: httpclient.OutcomeCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return nil, test.firstErr
				}
				return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
			}, httpclient.Config{Retry: httpclient.RetryConfig{
				MaxAttempts:          2,
				Methods:              []string{http.MethodGet},
				RetryTransportErrors: test.retryTransportErrors,
				RetryTimeouts:        test.retryTimeouts,
			}})
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
			result := client.Execute(request)
			if calls != test.wantAttempts || len(result.Attempts) != test.wantAttempts || result.Outcome != test.wantOutcome {
				t.Fatalf("Execute() = %#v with %d calls, want %d attempts and %q", result, calls, test.wantAttempts, test.wantOutcome)
			}
		})
	}
}

func TestHTTPResponseRetryRequiresConfiguredMethodAndStatus(t *testing.T) {
	tests := []struct {
		name        string
		methods     []string
		statusCodes []int
	}{
		{name: "method not configured", methods: []string{http.MethodPost}, statusCodes: []int{http.StatusServiceUnavailable}},
		{name: "status not configured", methods: []string{http.MethodGet}, statusCodes: []int{http.StatusBadGateway}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
			}, httpclient.Config{Retry: httpclient.RetryConfig{
				MaxAttempts: 2,
				Methods:     test.methods,
				StatusCodes: test.statusCodes,
			}})
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
			result := client.Execute(request)
			if result.Outcome != httpclient.OutcomeResponseRejected || calls != 1 || len(result.Attempts) != 1 {
				t.Fatalf("Execute() = %#v with %d calls, want non-eligible response without retry", result, calls)
			}
		})
	}
}

func TestHTTPRetryAfterSelection(t *testing.T) {
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	tests := []struct {
		name       string
		values     []string
		wantDelay  time.Duration
		wantSource string
	}{
		{name: "delta seconds is capped", values: []string{"30"}, wantDelay: 5 * time.Millisecond, wantSource: "retry_after"},
		{name: "HTTP date is capped", values: []string{future}, wantDelay: 5 * time.Millisecond, wantSource: "retry_after"},
		{name: "past date cannot shorten policy", values: []string{time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)}, wantDelay: 2 * time.Millisecond, wantSource: "policy"},
		{name: "invalid value falls back to policy", values: []string{"not-a-delay"}, wantDelay: 2 * time.Millisecond, wantSource: "policy"},
		{name: "empty value falls back to policy", values: []string{" "}, wantDelay: 2 * time.Millisecond, wantSource: "policy"},
		{name: "overflowing delta falls back to policy", values: []string{"18446744073709551616"}, wantDelay: 2 * time.Millisecond, wantSource: "policy"},
		{name: "later valid value is honored", values: []string{"invalid", "30"}, wantDelay: 5 * time.Millisecond, wantSource: "retry_after"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := &retryEventObserver{}
			calls := 0
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				calls++
				statusCode := http.StatusServiceUnavailable
				if calls == 2 {
					statusCode = http.StatusNoContent
				}
				headers := make(http.Header)
				for _, value := range test.values {
					headers.Add("Retry-After", value)
				}
				return &http.Response{StatusCode: statusCode, Header: headers, Body: http.NoBody, Request: request}, nil
			}, httpclient.Config{
				Config: clientkit.Config{Name: "retry-after", Observer: observer},
				Retry: httpclient.RetryConfig{
					MaxAttempts:       2,
					Backoff:           2 * time.Millisecond,
					BackoffMultiplier: 1,
					MaxBackoff:        2 * time.Millisecond,
					StatusCodes:       []int{http.StatusServiceUnavailable},
					Methods:           []string{http.MethodGet},
					RespectRetryAfter: true,
					MaxRetryAfter:     5 * time.Millisecond,
				},
			})
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
			result := client.Execute(request)
			if result.Outcome != httpclient.OutcomeSuccess || calls != 2 {
				t.Fatalf("Execute() = %#v with %d calls, want retry success", result, calls)
			}
			retries := observer.snapshot()
			if len(retries) != 1 || retries[0].Delay != test.wantDelay || httpEventAttribute(retries[0].Attributes, "http.retry_delay_source") != test.wantSource {
				t.Fatalf("retry events = %#v, want delay %v from %q", retries, test.wantDelay, test.wantSource)
			}
		})
	}
}

func TestHTTPRetryBackoffIsCapped(t *testing.T) {
	observer := &retryEventObserver{}
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	}, httpclient.Config{
		Config: clientkit.Config{Name: "retry-backoff", Observer: observer},
		Retry: httpclient.RetryConfig{
			MaxAttempts:       3,
			Backoff:           2 * time.Millisecond,
			BackoffMultiplier: 2,
			MaxBackoff:        3 * time.Millisecond,
			StatusCodes:       []int{http.StatusServiceUnavailable},
			Methods:           []string{http.MethodGet},
		},
	})
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeResponseRejected || len(result.Attempts) != 3 {
		t.Fatalf("Execute() = %#v, want three rejected attempts", result)
	}
	retries := observer.snapshot()
	if len(retries) != 2 || retries[0].Delay != 2*time.Millisecond || retries[1].Delay != 3*time.Millisecond {
		t.Fatalf("retry delays = %#v, want 2ms then capped 3ms", retries)
	}
}

func TestHTTPRetryJitterRemainsWithinConfiguredBounds(t *testing.T) {
	observer := &retryEventObserver{}
	calls := 0
	client := newStatusRetryClient(t, &calls, httpclient.RetryConfig{
		MaxAttempts:       2,
		Backoff:           5 * time.Millisecond,
		BackoffMultiplier: 1,
		Jitter:            2 * time.Millisecond,
		StatusCodes:       []int{http.StatusServiceUnavailable},
		Methods:           []string{http.MethodGet},
	}, observer)
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeSuccess {
		t.Fatalf("Execute() = %#v, want retry success", result)
	}
	retries := observer.snapshot()
	if len(retries) != 1 || retries[0].Delay < 3*time.Millisecond || retries[0].Delay > 7*time.Millisecond {
		t.Fatalf("retry events = %#v, want delay within [3ms, 7ms]", retries)
	}
}

type retryEventObserver struct {
	clientkit.NopObserver
	retries []clientkit.RetryEvent
}

func (observer *retryEventObserver) ObserveRetry(_ context.Context, event clientkit.RetryEvent) {
	observer.retries = append(observer.retries, event)
}

func (observer *retryEventObserver) snapshot() []clientkit.RetryEvent {
	return append([]clientkit.RetryEvent(nil), observer.retries...)
}

type retryTimeoutError struct{}

func (retryTimeoutError) Error() string   { return "transport timed out" }
func (retryTimeoutError) Timeout() bool   { return true }
func (retryTimeoutError) Temporary() bool { return true }
