package httpclient_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPExecutionRetryOverrides(t *testing.T) {
	t.Run("disable overrides client policy", func(t *testing.T) {
		calls := 0
		client := newStatusRetryClient(t, &calls, httpclient.RetryConfig{
			MaxAttempts: 2,
			StatusCodes: []int{http.StatusServiceUnavailable},
			Methods:     []string{http.MethodGet},
		}, nil)
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
		result := client.ExecuteWithOptions(request, httpclient.DoOptions{Retry: httpclient.ExecutionRetry{Disable: true}})
		if result.Outcome != httpclient.OutcomeHTTPError || calls != 1 || len(result.Attempts) != 1 {
			t.Fatalf("ExecuteWithOptions() = %#v with %d calls, want one rejected attempt", result, calls)
		}
	})

	t.Run("custom policy overrides no retry client", func(t *testing.T) {
		observer := &retryEventObserver{}
		calls := 0
		client := newStatusRetryClient(t, &calls, httpclient.NoRetryConfig(), observer)
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
		result := client.ExecuteWithOptions(request, httpclient.DoOptions{Retry: httpclient.ExecutionRetry{Config: httpclient.RetryConfig{
			MaxAttempts: 2,
			StatusCodes: []int{http.StatusServiceUnavailable},
			Methods:     []string{http.MethodGet},
		}}})
		if result.Outcome != httpclient.OutcomeSuccess || calls != 2 || len(result.Attempts) != 2 {
			t.Fatalf("ExecuteWithOptions() = %#v with %d calls, want override retry success", result, calls)
		}
		retries := observer.snapshot()
		if len(retries) != 1 || httpEventAttribute(retries[0].Attributes, "http.retry_policy_source") != "operation" {
			t.Fatalf("retry events = %#v, want operation policy source", retries)
		}
	})

	t.Run("zero override inherits client policy", func(t *testing.T) {
		observer := &retryEventObserver{}
		calls := 0
		client := newStatusRetryClient(t, &calls, httpclient.RetryConfig{
			MaxAttempts: 2,
			StatusCodes: []int{http.StatusServiceUnavailable},
			Methods:     []string{http.MethodGet},
		}, observer)
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
		result := client.ExecuteWithOptions(request, httpclient.DoOptions{})
		if result.Outcome != httpclient.OutcomeSuccess || calls != 2 {
			t.Fatalf("ExecuteWithOptions() = %#v with %d calls, want inherited retry success", result, calls)
		}
		retries := observer.snapshot()
		if len(retries) != 1 || httpEventAttribute(retries[0].Attributes, "http.retry_policy_source") != "client" {
			t.Fatalf("retry events = %#v, want client policy source", retries)
		}
	})
}

func TestHTTPExecutionTimeoutOverrides(t *testing.T) {
	t.Run("total timeout", func(t *testing.T) {
		client := newHTTPTestClient(t, waitForRequestCancellation, httpclient.Config{
			DisableTimeout:        true,
			DisableAttemptTimeout: true,
			Retry:                 httpclient.NoRetryConfig(),
		})
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
		result := client.ExecuteWithOptions(request, httpclient.DoOptions{Timeouts: httpclient.ExecutionTimeouts{Timeout: 10 * time.Millisecond}})
		if result.Outcome != httpclient.OutcomeTimeout || result.FailureClass != clientkit.FailureTimeout {
			t.Fatalf("ExecuteWithOptions() = %#v, want total timeout", result)
		}
	})

	t.Run("attempt timeout", func(t *testing.T) {
		client := newHTTPTestClient(t, waitForRequestCancellation, httpclient.Config{
			DisableTimeout:        true,
			DisableAttemptTimeout: true,
			Retry:                 httpclient.NoRetryConfig(),
		})
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
		result := client.ExecuteWithOptions(request, httpclient.DoOptions{Timeouts: httpclient.ExecutionTimeouts{AttemptTimeout: 10 * time.Millisecond}})
		if result.Outcome != httpclient.OutcomeTimeout || result.FailureClass != clientkit.FailureTimeout {
			t.Fatalf("ExecuteWithOptions() = %#v, want attempt timeout", result)
		}
	})

	t.Run("disable inherited attempt timeout", func(t *testing.T) {
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			select {
			case <-request.Context().Done():
				return nil, request.Context().Err()
			case <-time.After(15 * time.Millisecond):
				return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
			}
		}, httpclient.Config{
			DisableTimeout: true,
			AttemptTimeout: 5 * time.Millisecond,
			Retry:          httpclient.NoRetryConfig(),
		})
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
		result := client.ExecuteWithOptions(request, httpclient.DoOptions{Timeouts: httpclient.ExecutionTimeouts{DisableAttemptTimeout: true}})
		if result.Outcome != httpclient.OutcomeSuccess || result.Err != nil {
			t.Fatalf("ExecuteWithOptions() = %#v, want success without inherited attempt timeout", result)
		}
	})
}

func TestHTTPClientClonesRetryPolicies(t *testing.T) {
	t.Run("ordinary execution", func(t *testing.T) {
		retry := httpclient.RetryConfig{
			MaxAttempts: 2,
			StatusCodes: []int{http.StatusServiceUnavailable},
			Methods:     []string{http.MethodGet},
		}
		calls := 0
		client := newStatusRetryClient(t, &calls, retry, nil)
		retry.StatusCodes[0] = http.StatusTeapot
		retry.Methods[0] = http.MethodPost

		request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
		result := client.Execute(request)
		if result.Outcome != httpclient.OutcomeSuccess || calls != 2 {
			t.Fatalf("Execute() = %#v with %d calls, want immutable configured retry policy", result, calls)
		}
	})

	t.Run("health check", func(t *testing.T) {
		retry := httpclient.RetryConfig{
			MaxAttempts: 2,
			StatusCodes: []int{http.StatusServiceUnavailable},
			Methods:     []string{http.MethodGet},
		}
		calls := 0
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			status := http.StatusServiceUnavailable
			if calls == 2 {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
		}, httpclient.Config{Check: httpclient.CheckConfig{Enabled: true, Path: "/healthz", Retry: retry}})
		retry.StatusCodes[0] = http.StatusTeapot
		retry.Methods[0] = http.MethodPost

		health := client.Check(context.Background())
		if health.State != clientkit.HealthHealthy || calls != 2 {
			t.Fatalf("Check() = %#v with %d calls, want immutable health retry policy", health, calls)
		}
	})
}

func newStatusRetryClient(t *testing.T, calls *int, retry httpclient.RetryConfig, observer clientkit.Observer) *httpclient.Client {
	t.Helper()
	return newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		*calls = *calls + 1
		statusCode := http.StatusServiceUnavailable
		if *calls == 2 {
			statusCode = http.StatusNoContent
		}
		return &http.Response{StatusCode: statusCode, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	}, httpclient.Config{
		Config: clientkit.Config{Name: "retry-options", Observer: observer},
		Retry:  retry,
	})
}

func waitForRequestCancellation(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
}
