package httpclient_test

import (
	"net/http"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPRedirectOriginPolicy(t *testing.T) {
	t.Run("same origin is followed", func(t *testing.T) {
		calls := 0
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return redirectTo(request, "/final"), nil
			}
			return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
		}, httpclient.Config{Retry: httpclient.NoRetryConfig()})
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
		result := client.Execute(request)
		if result.Outcome != httpclient.OutcomeSuccess || result.Err != nil || calls != 2 {
			t.Fatalf("Execute() = %#v with %d calls, want followed same-origin redirect", result, calls)
		}
	})

	t.Run("cross origin is rejected", func(t *testing.T) {
		calls := 0
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			return redirectTo(request, "https://other.test/final"), nil
		}, httpclient.Config{Retry: httpclient.RetryConfig{
			MaxAttempts:          2,
			Methods:              []string{http.MethodGet},
			RetryTransportErrors: true,
		}})
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
		result := client.Execute(request)
		if result.FailureClass != clientkit.FailurePolicy || result.Err == nil || len(result.Attempts) != 1 || calls != 1 {
			t.Fatalf("Execute() = %#v with %d calls, want non-retried redirect origin failure", result, calls)
		}
	})

	t.Run("configured policy composes with origin validation", func(t *testing.T) {
		redirectChecks := 0
		calls := 0
		configuredHTTPClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			redirectChecks++
			return nil
		}}
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return redirectTo(request, "/final"), nil
			}
			return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
		}, httpclient.Config{
			HTTPClient: configuredHTTPClient,
			Retry:      httpclient.NoRetryConfig(),
		})
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
		result := client.Execute(request)
		if result.Outcome != httpclient.OutcomeSuccess || result.Err != nil || calls != 2 || redirectChecks != 1 {
			t.Fatalf("Execute() = %#v with %d calls and %d checks, want composed redirect success", result, calls, redirectChecks)
		}
		// Clientkit composes policy on a per-execution copy and must not replace
		// the callback on the caller-owned http.Client.
		if err := configuredHTTPClient.CheckRedirect(nil, nil); err != nil || redirectChecks != 2 {
			t.Fatalf("caller CheckRedirect after Execute() = %v with %d calls, want original callback", err, redirectChecks)
		}
	})
}

func TestHTTPDefaultRedirectLimitIsPolicyFailure(t *testing.T) {
	calls := 0
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		return redirectTo(request, "/loop"), nil
	}, httpclient.Config{Retry: httpclient.RetryConfig{
		MaxAttempts:          2,
		Methods:              []string{http.MethodGet},
		RetryTransportErrors: true,
	}})
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
	result := client.Execute(request)
	if result.FailureClass != clientkit.FailurePolicy || result.Err == nil || len(result.Attempts) != 1 || calls != 10 {
		t.Fatalf("Execute() = %#v with %d calls, want one non-retried redirect-limit failure after 10 requests", result, calls)
	}
}

func TestHTTPRedirectRejectsNonHTTPSchemeEvenWhenCrossOriginIsEnabled(t *testing.T) {
	calls := 0
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		return redirectTo(request, "file://other.test/private"), nil
	}, httpclient.Config{
		AllowCrossOrigin: true,
		Retry:            httpclient.NoRetryConfig(),
	})
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
	result := client.Execute(request)
	if result.Response == nil || result.Err == nil || result.FailureClass != clientkit.FailurePolicy || len(result.Attempts) != 1 || calls != 1 {
		t.Fatalf("Execute() = %#v with %d calls, want rejected redirect before non-HTTP transport", result, calls)
	}
}

func redirectTo(request *http.Request, location string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusFound,
		Header:     http.Header{"Location": []string{location}},
		Body:       http.NoBody,
		Request:    request,
	}
}
