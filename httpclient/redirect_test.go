package httpclient_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPMethodPreservingRedirectRetrySafety(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		body        bool
		retrySafety httpclient.RetrySafety
		wantCalls   int
		wantOutcome httpclient.Outcome
		wantFailure clientkit.FailureClass
	}{
		{name: "default POST", method: http.MethodPost, body: true, wantCalls: 1, wantOutcome: httpclient.OutcomeExecutionError, wantFailure: clientkit.FailurePolicy},
		{name: "never POST", method: http.MethodPost, body: true, retrySafety: httpclient.RetrySafetyNever, wantCalls: 1, wantOutcome: httpclient.OutcomeExecutionError, wantFailure: clientkit.FailurePolicy},
		{name: "idempotent POST", method: http.MethodPost, body: true, retrySafety: httpclient.RetrySafetyIdempotent, wantCalls: 2, wantOutcome: httpclient.OutcomeSuccess},
		{name: "default PUT", method: http.MethodPut, body: true, wantCalls: 2, wantOutcome: httpclient.OutcomeSuccess},
		{name: "never PUT", method: http.MethodPut, body: true, retrySafety: httpclient.RetrySafetyNever, wantCalls: 1, wantOutcome: httpclient.OutcomeExecutionError, wantFailure: clientkit.FailurePolicy},
		{name: "default custom method", method: "PURGE", body: true, wantCalls: 1, wantOutcome: httpclient.OutcomeExecutionError, wantFailure: clientkit.FailurePolicy},
		{name: "default bodyless POST", method: http.MethodPost, wantCalls: 1, wantOutcome: httpclient.OutcomeExecutionError, wantFailure: clientkit.FailurePolicy},
		{name: "idempotent bodyless POST", method: http.MethodPost, retrySafety: httpclient.RetrySafetyIdempotent, wantCalls: 2, wantOutcome: httpclient.OutcomeSuccess},
	}

	for _, statusCode := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, test := range tests {
			t.Run(fmt.Sprintf("%d/%s", statusCode, test.name), func(t *testing.T) {
				observer := &httpAttributeObserver{}
				calls := 0
				var redirectBody *trackedReadCloser
				var wireMethods []string
				var wireBodies []string
				client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
					calls++
					wireMethods = append(wireMethods, request.Method)
					content := ""
					if request.Body != nil {
						payload, err := io.ReadAll(request.Body)
						if err != nil {
							t.Fatalf("read request body: %v", err)
						}
						content = string(payload)
					}
					wireBodies = append(wireBodies, content)
					if calls == 1 {
						redirectBody = &trackedReadCloser{Reader: strings.NewReader("redirect")}
						return redirectToStatus(request, statusCode, "/final", redirectBody), nil
					}
					return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
				}, httpclient.Config{
					Config: clientkit.Config{Name: "redirect-safety", Observer: observer},
					Retry:  httpclient.NoRetryConfig(),
				})

				var body io.Reader
				if test.body {
					body = strings.NewReader("payload")
				}
				request, err := http.NewRequest(test.method, "https://example.test/start", body)
				if err != nil {
					t.Fatalf("http.NewRequest() error = %v", err)
				}
				result := client.ExecuteWithOptions(request, httpclient.ExecuteOptions{RetrySafety: test.retrySafety})

				if result.Outcome != test.wantOutcome || result.FailureClass != test.wantFailure || calls != test.wantCalls || len(result.Attempts) != 1 {
					t.Fatalf("ExecuteWithOptions() = %#v with %d calls, want outcome %q, failure %q, and %d calls", result, calls, test.wantOutcome, test.wantFailure, test.wantCalls)
				}
				if len(observer.attempts) != 1 || len(observer.retries) != 0 || observer.end.Attempts != 1 || observer.end.Outcome != string(test.wantOutcome) || observer.end.FailureClass != test.wantFailure {
					t.Fatalf("observer = %#v, want one completed attempt, no retry, and matching operation result", observer)
				}

				if test.wantCalls == 1 {
					var redirectErr *url.Error
					if result.Response == nil || result.StatusCode != statusCode || result.Err == nil || !errors.As(result.Err, &redirectErr) {
						t.Fatalf("blocked redirect result = %#v, want %d response and *url.Error", result, statusCode)
					}
					if result.Attempts[0].Outcome != httpclient.OutcomeExecutionError || result.Attempts[0].FailureClass != clientkit.FailurePolicy || result.Attempts[0].StatusCode != statusCode || result.Attempts[0].Err == nil {
						t.Fatalf("blocked redirect attempt = %#v, want policy execution error", result.Attempts[0])
					}
					if redirectBody == nil || !redirectBody.closed {
						t.Fatal("net/http did not close the rejected redirect response body")
					}
					return
				}

				if result.Err != nil || result.Response == nil || result.StatusCode != http.StatusNoContent || len(wireMethods) != 2 || wireMethods[0] != test.method || wireMethods[1] != test.method {
					t.Fatalf("followed redirect result/methods = (%#v, %v), want successful repeated %s", result, wireMethods, test.method)
				}
				wantBody := ""
				if test.body {
					wantBody = "payload"
				}
				if wireBodies[0] != wantBody || wireBodies[1] != wantBody {
					t.Fatalf("wire bodies = %q, want repeated %q", wireBodies, wantBody)
				}
			})
		}
	}
}

func TestHTTPRedirectMethodRewritesRemainNormal(t *testing.T) {
	for _, statusCode := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			for _, retrySafety := range []httpclient.RetrySafety{httpclient.RetrySafetyDefault, httpclient.RetrySafetyNever, httpclient.RetrySafetyIdempotent} {
				name := fmt.Sprintf("%d/%s/%s", statusCode, method, retrySafety)
				if retrySafety == httpclient.RetrySafetyDefault {
					name = fmt.Sprintf("%d/%s/default", statusCode, method)
				}
				t.Run(name, func(t *testing.T) {
					var methods []string
					var bodies []string
					client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
						methods = append(methods, request.Method)
						content := ""
						if request.Body != nil {
							payload, err := io.ReadAll(request.Body)
							if err != nil {
								t.Fatalf("read request body: %v", err)
							}
							content = string(payload)
						}
						bodies = append(bodies, content)
						if len(methods) == 1 {
							return redirectToStatus(request, statusCode, "/final", http.NoBody), nil
						}
						return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
					}, httpclient.Config{Retry: httpclient.NoRetryConfig()})
					request, _ := http.NewRequest(method, "https://example.test/start", strings.NewReader("payload"))
					result := client.ExecuteWithOptions(request, httpclient.ExecuteOptions{RetrySafety: retrySafety})
					if result.Outcome != httpclient.OutcomeSuccess || result.Err != nil || len(result.Attempts) != 1 || len(methods) != 2 || methods[0] != method || methods[1] != http.MethodGet || bodies[0] != "payload" || bodies[1] != "" {
						t.Fatalf("ExecuteWithOptions() = %#v with methods %v and bodies %q, want %s then bodyless GET", result, methods, bodies, method)
					}
				})
			}
		}
	}
}

func TestHTTPRedirectReplaySafetyUsesUpcomingMethod(t *testing.T) {
	var methods []string
	var bodies []string
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		methods = append(methods, request.Method)
		content := ""
		if request.Body != nil {
			payload, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			content = string(payload)
		}
		bodies = append(bodies, content)
		switch len(methods) {
		case 1:
			return redirectToStatus(request, http.StatusSeeOther, "/retrieve", http.NoBody), nil
		case 2:
			return redirectToStatus(request, http.StatusTemporaryRedirect, "/final", http.NoBody), nil
		default:
			return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
		}
	}, httpclient.Config{Retry: httpclient.NoRetryConfig()})
	request, _ := http.NewRequest(http.MethodPost, "https://example.test/start", strings.NewReader("payload"))
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeSuccess || result.Err != nil || len(result.Attempts) != 1 || strings.Join(methods, ",") != "POST,GET,GET" || strings.Join(bodies, ",") != "payload,," {
		t.Fatalf("Execute() = %#v with methods %v and bodies %q, want POST rewritten to safely redirected GET", result, methods, bodies)
	}
}

func TestHTTPMethodPreservingRedirectWithNonReplayableBodyReturnsResponse(t *testing.T) {
	for _, statusCode := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, retrySafety := range []httpclient.RetrySafety{httpclient.RetrySafetyDefault, httpclient.RetrySafetyNever, httpclient.RetrySafetyIdempotent} {
			name := string(retrySafety)
			if retrySafety == httpclient.RetrySafetyDefault {
				name = "default"
			}
			t.Run(fmt.Sprintf("%d/%s", statusCode, name), func(t *testing.T) {
				redirectChecks := 0
				calls := 0
				responseBody := &trackedReadCloser{Reader: strings.NewReader("redirect")}
				configuredHTTPClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
					redirectChecks++
					return nil
				}}
				client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
					calls++
					_ = request.Body.Close()
					return redirectToStatus(request, statusCode, "/final", responseBody), nil
				}, httpclient.Config{HTTPClient: configuredHTTPClient, Retry: httpclient.NoRetryConfig()})
				requestBody := &trackedReadCloser{Reader: strings.NewReader("payload")}
				request, _ := http.NewRequest(http.MethodPost, "https://example.test/start", io.Reader(requestBody))
				if request.GetBody != nil {
					t.Fatal("test request unexpectedly has GetBody")
				}
				result := client.ExecuteWithOptions(request, httpclient.ExecuteOptions{RetrySafety: retrySafety})
				if result.Outcome != httpclient.OutcomeResponseRejected || result.FailureClass != clientkit.FailureRemoteResponse || result.Err != nil || result.Response == nil || result.StatusCode != statusCode || len(result.Attempts) != 1 || calls != 1 || redirectChecks != 0 {
					t.Fatalf("ExecuteWithOptions() = %#v with %d calls and %d redirect checks, want caller-owned %d response", result, calls, redirectChecks, statusCode)
				}
				if !requestBody.closed || responseBody.closed {
					t.Fatalf("request/response bodies closed = %t/%t, want true/false", requestBody.closed, responseBody.closed)
				}
				if err := result.Response.Body.Close(); err != nil || !responseBody.closed {
					t.Fatalf("response close = %v, closed = %t", err, responseBody.closed)
				}
			})
		}
	}
}

func TestHTTPMethodPreservingRedirectGetBodyFailurePrecedesPolicy(t *testing.T) {
	getBodyErr := errors.New("cannot recreate redirect body")
	for _, statusCode := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprintf("%d", statusCode), func(t *testing.T) {
			redirectChecks := 0
			calls := 0
			responseBody := &trackedReadCloser{Reader: strings.NewReader("redirect")}
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				calls++
				return redirectToStatus(request, statusCode, "/final", responseBody), nil
			}, httpclient.Config{
				HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
					redirectChecks++
					return nil
				}},
				Retry: httpclient.NoRetryConfig(),
			})
			request, _ := http.NewRequest(http.MethodPost, "https://example.test/start", strings.NewReader("payload"))
			request.GetBody = func() (io.ReadCloser, error) { return nil, getBodyErr }
			result := client.ExecuteWithOptions(request, httpclient.ExecuteOptions{RetrySafety: httpclient.RetrySafetyNever})
			if result.Outcome != httpclient.OutcomeExecutionError || result.FailureClass != clientkit.FailureTransport || !errors.Is(result.Err, getBodyErr) || result.Response != nil || result.StatusCode != 0 || len(result.Attempts) != 1 || calls != 1 || redirectChecks != 0 {
				t.Fatalf("ExecuteWithOptions() = %#v with %d calls and %d redirect checks, want pre-policy GetBody failure", result, calls, redirectChecks)
			}
			if !responseBody.closed {
				t.Fatal("net/http did not close redirect response after GetBody failure")
			}
		})
	}
}

func TestHTTPMethodPreservingRedirectPolicyComposition(t *testing.T) {
	t.Run("caller rejection takes precedence", func(t *testing.T) {
		callerErr := errors.New("caller rejected redirect")
		checks := 0
		calls := 0
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			return redirectToStatus(request, http.StatusTemporaryRedirect, "/final", http.NoBody), nil
		}, httpclient.Config{
			HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				checks++
				return callerErr
			}},
			Retry: httpclient.NoRetryConfig(),
		})
		request, _ := http.NewRequest(http.MethodPost, "https://example.test/start", strings.NewReader("payload"))
		result := client.Execute(request)
		if result.Outcome != httpclient.OutcomeExecutionError || result.FailureClass != clientkit.FailurePolicy || !errors.Is(result.Err, callerErr) || calls != 1 || checks != 1 {
			t.Fatalf("Execute() = %#v with %d calls and %d checks, want caller policy failure", result, calls, checks)
		}
	})

	t.Run("ErrUseLastResponse stops safely", func(t *testing.T) {
		checks := 0
		calls := 0
		responseBody := &trackedReadCloser{Reader: strings.NewReader("redirect")}
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			return redirectToStatus(request, http.StatusPermanentRedirect, "/final", responseBody), nil
		}, httpclient.Config{
			HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				checks++
				return http.ErrUseLastResponse
			}},
			Retry: httpclient.NoRetryConfig(),
		})
		request, _ := http.NewRequest(http.MethodPost, "https://example.test/start", strings.NewReader("payload"))
		result := client.ExecuteWithOptions(request, httpclient.ExecuteOptions{RetrySafety: httpclient.RetrySafetyNever})
		if result.Outcome != httpclient.OutcomeResponseRejected || result.FailureClass != clientkit.FailureRemoteResponse || result.Err != nil || result.Response == nil || result.StatusCode != http.StatusPermanentRedirect || calls != 1 || checks != 1 || responseBody.closed {
			t.Fatalf("ExecuteWithOptions() = %#v with %d calls and %d checks, want caller-owned last response", result, calls, checks)
		}
		if err := result.Response.Body.Close(); err != nil {
			t.Fatalf("Body.Close() error = %v", err)
		}
	})

	t.Run("permissive caller cannot bypass default safety", func(t *testing.T) {
		checks := 0
		calls := 0
		configuredHTTPClient := &http.Client{CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			checks++
			// The caller may mutate the redirect request, but that must not erase
			// the status that caused net/http to preserve the method and body.
			if request != nil {
				request.Response = nil
			}
			return nil
		}}
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			return redirectToStatus(request, http.StatusTemporaryRedirect, "/final", http.NoBody), nil
		}, httpclient.Config{HTTPClient: configuredHTTPClient, Retry: httpclient.NoRetryConfig()})
		request, _ := http.NewRequest(http.MethodPost, "https://example.test/start", strings.NewReader("payload"))
		result := client.Execute(request)
		if result.Outcome != httpclient.OutcomeExecutionError || result.FailureClass != clientkit.FailurePolicy || result.Err == nil || calls != 1 || checks != 1 {
			t.Fatalf("Execute() = %#v with %d calls and %d checks, want Clientkit policy failure", result, calls, checks)
		}
		if err := configuredHTTPClient.CheckRedirect(nil, nil); err != nil || checks != 2 {
			t.Fatalf("caller CheckRedirect after Execute() = %v with %d checks, want original callback", err, checks)
		}
	})

	t.Run("idempotent assertion still requires caller approval", func(t *testing.T) {
		checks := 0
		calls := 0
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return redirectToStatus(request, http.StatusPermanentRedirect, "/final", http.NoBody), nil
			}
			return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
		}, httpclient.Config{
			HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				checks++
				return nil
			}},
			Retry: httpclient.NoRetryConfig(),
		})
		request, _ := http.NewRequest(http.MethodPost, "https://example.test/start", strings.NewReader("payload"))
		result := client.ExecuteWithOptions(request, httpclient.ExecuteOptions{RetrySafety: httpclient.RetrySafetyIdempotent})
		if result.Outcome != httpclient.OutcomeSuccess || result.Err != nil || calls != 2 || checks != 1 {
			t.Fatalf("ExecuteWithOptions() = %#v with %d calls and %d checks, want composed redirect success", result, calls, checks)
		}
	})
}

func TestHTTPMethodPreservingRedirectOriginPolicy(t *testing.T) {
	t.Run("origin policy rejects an otherwise authorized replay", func(t *testing.T) {
		calls := 0
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			return redirectToStatus(request, http.StatusTemporaryRedirect, "https://other.test/final", http.NoBody), nil
		}, httpclient.Config{Retry: httpclient.NoRetryConfig()})
		request, _ := http.NewRequest(http.MethodPost, "https://example.test/start", strings.NewReader("payload"))
		result := client.ExecuteWithOptions(request, httpclient.ExecuteOptions{RetrySafety: httpclient.RetrySafetyIdempotent})
		if result.Outcome != httpclient.OutcomeExecutionError || result.FailureClass != clientkit.FailurePolicy || result.Err == nil || calls != 1 {
			t.Fatalf("ExecuteWithOptions() = %#v with %d calls, want redirect-origin policy failure", result, calls)
		}
	})

	t.Run("cross-origin override and idempotent assertion both permit replay", func(t *testing.T) {
		calls := 0
		var hosts []string
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			hosts = append(hosts, request.URL.Host)
			if calls == 1 {
				return redirectToStatus(request, http.StatusPermanentRedirect, "https://other.test/final", http.NoBody), nil
			}
			return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
		}, httpclient.Config{AllowCrossOrigin: true, Retry: httpclient.NoRetryConfig()})
		request, _ := http.NewRequest(http.MethodPost, "https://example.test/start", strings.NewReader("payload"))
		result := client.ExecuteWithOptions(request, httpclient.ExecuteOptions{RetrySafety: httpclient.RetrySafetyIdempotent})
		if result.Outcome != httpclient.OutcomeSuccess || result.Err != nil || calls != 2 || strings.Join(hosts, ",") != "example.test,other.test" {
			t.Fatalf("ExecuteWithOptions() = %#v with hosts %v, want explicit cross-origin replay", result, hosts)
		}
	})
}

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
			MaxAttempts:     2,
			Methods:         []string{http.MethodGet},
			TransportErrors: httpclient.TransportRetryAll,
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
		MaxAttempts:     2,
		Methods:         []string{http.MethodGet},
		TransportErrors: httpclient.TransportRetryAll,
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
	return redirectToStatus(request, http.StatusFound, location, http.NoBody)
}

func redirectToStatus(request *http.Request, statusCode int, location string, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Location": []string{location}},
		Body:       body,
		Request:    request,
	}
}
