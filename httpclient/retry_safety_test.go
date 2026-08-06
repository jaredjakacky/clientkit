package httpclient_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPRetrySafetyAndReplayability(t *testing.T) {
	newRetryingClient := func(t *testing.T, method string) (*httpclient.Client, *int) {
		t.Helper()
		attempts := 0
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			attempts++
			status := http.StatusServiceUnavailable
			if attempts == 2 {
				status = http.StatusNoContent
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
		}, httpclient.Config{Retry: httpclient.RetryConfig{
			MaxAttempts: 2,
			StatusCodes: []int{http.StatusServiceUnavailable},
			Methods:     []string{method},
		}})
		return client, &attempts
	}

	for _, method := range []string{
		http.MethodGet,
		http.MethodHead,
		http.MethodPut,
		http.MethodDelete,
		http.MethodOptions,
		http.MethodTrace,
	} {
		t.Run("idempotent "+method, func(t *testing.T) {
			client, attempts := newRetryingClient(t, method)
			request, _ := http.NewRequest(method, "https://example.test/resource", nil)
			result := client.Execute(request)
			if *attempts != 2 || len(result.Attempts) != 2 || result.Outcome != httpclient.OutcomeSuccess {
				t.Fatalf("Execute() = %#v with %d calls, want retry success", result, *attempts)
			}
		})
	}

	t.Run("unsafe POST is not retried by default", func(t *testing.T) {
		client, attempts := newRetryingClient(t, http.MethodPost)
		request, _ := http.NewRequest(http.MethodPost, "https://example.test/resource", nil)
		result := client.Execute(request)
		if *attempts != 1 || len(result.Attempts) != 1 || result.Outcome != httpclient.OutcomeHTTPError {
			t.Fatalf("Execute() = %#v with %d calls, want one rejected attempt", result, *attempts)
		}
	})

	t.Run("explicitly idempotent POST retries", func(t *testing.T) {
		client, attempts := newRetryingClient(t, http.MethodPost)
		request, _ := http.NewRequest(http.MethodPost, "https://example.test/resource", nil)
		result := client.ExecuteWithOptions(request, httpclient.DoOptions{RetrySafety: httpclient.RetrySafetyIdempotent})
		if *attempts != 2 || len(result.Attempts) != 2 || result.Outcome != httpclient.OutcomeSuccess {
			t.Fatalf("ExecuteWithOptions() = %#v with %d calls, want retry success", result, *attempts)
		}
	})

	t.Run("never disables idempotent retry", func(t *testing.T) {
		client, attempts := newRetryingClient(t, http.MethodPut)
		request, _ := http.NewRequest(http.MethodPut, "https://example.test/resource", nil)
		result := client.ExecuteWithOptions(request, httpclient.DoOptions{RetrySafety: httpclient.RetrySafetyNever})
		if *attempts != 1 || len(result.Attempts) != 1 {
			t.Fatalf("ExecuteWithOptions() = %#v with %d calls, want one attempt", result, *attempts)
		}
	})

	t.Run("non-replayable body prevents retry", func(t *testing.T) {
		client, attempts := newRetryingClient(t, http.MethodPut)
		request, _ := http.NewRequest(http.MethodPut, "https://example.test/resource", io.NopCloser(strings.NewReader("payload")))
		if request.GetBody != nil {
			t.Fatal("test request unexpectedly has GetBody")
		}
		result := client.Execute(request)
		if *attempts != 1 || len(result.Attempts) != 1 {
			t.Fatalf("Execute() = %#v with %d calls, want no replay retry", result, *attempts)
		}
	})
}
