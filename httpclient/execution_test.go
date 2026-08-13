package httpclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPExecuteSuccessAndResponseLifecycle(t *testing.T) {
	ended := make(chan clientkit.OperationEndEvent, 2)
	observer := executionObserver{
		end: func(event clientkit.OperationEndEvent) { ended <- event },
	}
	body := &trackedReadCloser{Reader: strings.NewReader("payload")}
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
			Request:    request,
		}, nil
	}, httpclient.Config{Config: clientkit.Config{Name: "lifecycle", Observer: observer}})
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)

	response, err := client.Do(request)
	if err != nil || response == nil || response.StatusCode != http.StatusOK {
		t.Fatalf("Do() = (%v, %v), want 200 response", response, err)
	}
	event := <-ended
	if !event.Succeeded || event.Outcome != string(httpclient.OutcomeSuccess) || event.FailureClass != clientkit.FailureNone || event.Attempts != 1 {
		t.Fatalf("operation end = %#v, want one successful header-time completion", event)
	}
	content, err := io.ReadAll(response.Body)
	if err != nil || string(content) != "payload" {
		t.Fatalf("ReadAll() = (%q, %v), want payload", content, err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("Body.Close() error = %v", err)
	}
	select {
	case duplicate := <-ended:
		t.Fatalf("operation ended more than once: %#v", duplicate)
	default:
	}
}

func TestHTTPExecuteRejectedResponseUsesStandardSemantics(t *testing.T) {
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	}, httpclient.Config{Retry: httpclient.NoRetryConfig()})
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)

	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeResponseRejected || result.FailureClass != clientkit.FailureRemoteResponse || result.StatusCode != http.StatusNotFound {
		t.Fatalf("Execute() = %#v, want rejected 404", result)
	}
	if result.Err != nil || result.Response == nil || len(result.Attempts) != 1 {
		t.Fatalf("Execute() response/error/attempts = %#v, want response with nil error", result)
	}
	response, err := client.Do(request)
	if err != nil || response == nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("Do() = (%v, %v), want standard rejected-response semantics", response, err)
	}
}

func TestHTTPDoPreservesErrorSemantics(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		transportErr := errors.New("transport unavailable")
		client := newHTTPTestClient(t, func(*http.Request) (*http.Response, error) {
			return nil, transportErr
		}, httpclient.Config{Retry: httpclient.NoRetryConfig()})
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
		response, err := client.Do(request)
		if response != nil || !errors.Is(err, transportErr) {
			t.Fatalf("Do() = (%v, %v), want nil response wrapping transport error", response, err)
		}
	})

	t.Run("redirect policy returns response and error", func(t *testing.T) {
		redirectErr := errors.New("redirect rejected")
		body := &trackedReadCloser{Reader: strings.NewReader("redirect")}
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			response := redirectResponse(request)
			response.Body = body
			return response, nil
		}, httpclient.Config{
			HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return redirectErr }},
			Retry:      httpclient.NoRetryConfig(),
		})
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
		response, err := client.Do(request)
		if response == nil || response.StatusCode != http.StatusFound || !errors.Is(err, redirectErr) {
			t.Fatalf("Do() = (%v, %v), want redirect response and policy error", response, err)
		}
		if !body.closed {
			t.Fatal("net/http did not close redirect response body after policy rejection")
		}
	})
}

func TestHTTPExecuteInputValidation(t *testing.T) {
	client := newHTTPTestClient(t, nil, httpclient.Config{})
	tests := []struct {
		name    string
		request *http.Request
	}{
		{name: "nil request"},
		{name: "nil URL", request: &http.Request{Method: http.MethodGet}},
		{name: "relative URL", request: &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/resource"}}},
		{name: "opaque URL", request: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "example.test", Opaque: "opaque"}}},
		{name: "URL user information", request: mustRequest(t, "https://user@example.test/resource")},
		{name: "URL fragment", request: mustRequest(t, "https://example.test/resource#fragment")},
		{name: "client RequestURI", request: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "example.test", Path: "/resource"}, RequestURI: "/resource"}},
		{name: "invalid method", request: &http.Request{Method: "BAD METHOD", URL: &url.URL{Scheme: "https", Host: "example.test", Path: "/resource"}}},
		{name: "unsupported scheme", request: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "file", Host: "example.test", Path: "/resource"}}},
		{name: "missing hostname", request: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: ":443", Path: "/resource"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body *trackedReadCloser
			if test.request != nil {
				body = &trackedReadCloser{Reader: strings.NewReader("payload")}
				test.request.Body = body
			}
			result := client.Execute(test.request)
			if result.Err == nil || result.FailureClass == clientkit.FailureNone || len(result.Attempts) != 0 {
				t.Fatalf("Execute() = %#v, want pre-attempt failure", result)
			}
			if body != nil && !body.closed {
				t.Fatal("pre-attempt validation did not close request body")
			}
		})
	}

	var nilClient *httpclient.Client
	nilRequest := mustRequest(t, "https://example.test/resource")
	nilBody := &trackedReadCloser{Reader: strings.NewReader("payload")}
	nilRequest.Body = nilBody
	result := nilClient.Execute(nilRequest)
	if result.Err == nil || result.FailureClass != clientkit.FailureConfiguration {
		t.Fatalf("nil Execute() = %#v, want configuration failure", result)
	}
	if !nilBody.closed {
		t.Fatal("nil client did not close request body")
	}
}

func TestHTTPExecuteRejectsNonHTTPSchemeWhenCrossOriginIsEnabled(t *testing.T) {
	calls := 0
	client := newHTTPTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("transport must not run")
	}, httpclient.Config{AllowCrossOrigin: true, Retry: httpclient.NoRetryConfig()})
	request := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Scheme: "file", Host: "other.test", Path: "/resource"},
		Header: make(http.Header),
		Body:   &trackedReadCloser{Reader: strings.NewReader("payload")},
	}
	result := client.Execute(request)
	if result.FailureClass != clientkit.FailureRequest || result.Err == nil || len(result.Attempts) != 0 || calls != 0 {
		t.Fatalf("Execute() = %#v with %d calls, want pre-attempt request rejection", result, calls)
	}
	if !request.Body.(*trackedReadCloser).closed {
		t.Fatal("rejected non-HTTP request body was not closed")
	}
}

func TestHTTPExecuteCanceledContextDoesNotReachTransport(t *testing.T) {
	calls := 0
	client := newHTTPTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("transport should not run")
	}, httpclient.Config{Retry: httpclient.NoRetryConfig()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := &trackedReadCloser{Reader: strings.NewReader("payload")}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/resource", body)
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeCanceled || result.FailureClass != clientkit.FailureCanceled || !errors.Is(result.Err, context.Canceled) || len(result.Attempts) != 0 || calls != 0 {
		t.Fatalf("Execute() = %#v with %d calls, want pre-attempt cancellation", result, calls)
	}
	if !body.closed {
		t.Fatal("pre-attempt cancellation did not close request body")
	}
}

func TestHTTPRetryDelayRespectsTotalTimeout(t *testing.T) {
	observer := &retryRecordingObserver{}
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	}, httpclient.Config{
		Config:  clientkit.Config{Name: "retry-timeout", Observer: observer},
		Timeout: 20 * time.Millisecond,
		Retry: httpclient.RetryConfig{
			MaxAttempts: 2,
			Backoff:     time.Second,
			StatusCodes: []int{http.StatusServiceUnavailable},
			Methods:     []string{http.MethodPut},
		},
	})
	request, _ := http.NewRequest(http.MethodPut, "https://example.test/resource", strings.NewReader("payload"))
	getBodyCalls := 0
	request.GetBody = func() (io.ReadCloser, error) {
		getBodyCalls++
		return io.NopCloser(strings.NewReader("payload")), nil
	}
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeTimeout || result.FailureClass != clientkit.FailureTimeout || !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("Execute() = %#v, want total-timeout failure during delay", result)
	}
	if len(result.Attempts) != 1 || observer.retryCount != 1 || getBodyCalls != 0 {
		t.Fatalf("attempts/retries/GetBody calls = %d/%d/%d, want 1/1/0", len(result.Attempts), observer.retryCount, getBodyCalls)
	}
}

func TestHTTPAttemptTimeout(t *testing.T) {
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}, httpclient.Config{
		DisableTimeout: true,
		AttemptTimeout: 15 * time.Millisecond,
		Retry:          httpclient.NoRetryConfig(),
	})
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeTimeout || result.FailureClass != clientkit.FailureTimeout || !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("Execute() = %#v, want attempt timeout", result)
	}
}

func TestHTTPExecuteRejectsLateTransportResults(t *testing.T) {
	tests := []struct {
		name       string
		returnBoth bool
		returnErr  bool
	}{
		{name: "response"},
		{name: "response and error", returnBoth: true, returnErr: true},
		{name: "error", returnErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &trackedReadCloser{Reader: strings.NewReader("late")}
			lateErr := errors.New("late transport failure")
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				var response *http.Response
				if !test.returnErr || test.returnBoth {
					response = &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       body,
						Request:    request,
					}
				}
				if test.returnErr {
					return response, lateErr
				}
				return response, nil
			}, httpclient.Config{
				DisableTimeout: true,
				AttemptTimeout: 10 * time.Millisecond,
				Retry:          httpclient.NoRetryConfig(),
			})
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
			result := client.Execute(request)
			if result.Response != nil || result.Outcome != httpclient.OutcomeTimeout || result.FailureClass != clientkit.FailureTimeout || !errors.Is(result.Err, context.DeadlineExceeded) || len(result.Attempts) != 1 {
				t.Fatalf("Execute() = %#v, want one deadline-exceeded attempt", result)
			}
			if (!test.returnErr || test.returnBoth) && !body.closed {
				t.Fatal("late response body was not closed")
			}
		})
	}
}

func TestHTTPExecuteRejectsLateSuccessAfterCallerHTTPClientTimeout(t *testing.T) {
	body := &trackedReadCloser{Reader: strings.NewReader("late")}
	configuredHTTPClient := &http.Client{Timeout: 10 * time.Millisecond}
	configuredHTTPClient.Transport = testRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		select {
		case <-request.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: request}, nil
	})
	client, err := httpclient.New(httpclient.Config{
		Config:                clientkit.Config{Name: "caller-timeout", Observer: clientkit.NopObserver{}},
		BaseURL:               "https://example.test",
		HTTPClient:            configuredHTTPClient,
		Propagator:            httpclient.NopHeaderPropagator{},
		DisableTimeout:        true,
		DisableAttemptTimeout: true,
		Retry:                 httpclient.NoRetryConfig(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	configuredHTTPClient.Timeout = 0

	request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
	result := client.Execute(request)
	if result.Response != nil {
		defer result.Response.Body.Close()
	}
	if result.Response != nil || result.Outcome != httpclient.OutcomeTimeout || result.FailureClass != clientkit.FailureTimeout || !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("Execute() = %#v, want construction-time caller HTTP client timeout", result)
	}
	if !body.closed {
		t.Fatal("late response after caller HTTP client timeout was not closed")
	}
}

func TestHTTPExecuteParentCancellationWinsConcurrentTransportSuccess(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	body := &trackedReadCloser{Reader: strings.NewReader("late")}
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: request}, nil
	}, httpclient.Config{
		DisableTimeout:        true,
		DisableAttemptTimeout: true,
		Retry:                 httpclient.NoRetryConfig(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/resource", nil)
	resultCh := make(chan httpclient.Result, 1)
	go func() { resultCh <- client.Execute(request) }()
	<-started
	cancel()
	close(release)
	result := <-resultCh
	if result.Response != nil || result.Outcome != httpclient.OutcomeCanceled || result.FailureClass != clientkit.FailureCanceled || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Execute() = %#v, want parent cancellation", result)
	}
	if !body.closed {
		t.Fatal("response losing the cancellation race was not closed")
	}
}

func TestHTTPExecuteRejectsClassificationCompletedAfterCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	body := &trackedReadCloser{Reader: strings.NewReader("late")}
	classifier := httpclient.ResponseClassifierFunc(func(*http.Response) httpclient.ResponseDisposition {
		close(entered)
		<-release
		return httpclient.ResponseAccepted
	})
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: request}, nil
	}, httpclient.Config{
		DisableTimeout:        true,
		DisableAttemptTimeout: true,
		ResponseClassifier:    classifier,
		Retry:                 httpclient.NoRetryConfig(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/resource", nil)
	resultCh := make(chan httpclient.Result, 1)
	go func() { resultCh <- client.Execute(request) }()
	<-entered
	cancel()
	close(release)
	result := <-resultCh
	if result.Response != nil || result.Outcome != httpclient.OutcomeCanceled || result.FailureClass != clientkit.FailureCanceled || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Execute() = %#v, want cancellation after late classification", result)
	}
	if !body.closed {
		t.Fatal("response was not closed after late classification")
	}
}

func TestHTTPExecuteRejectsBodyRecreatedAfterCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	recreated := &trackedReadCloser{Reader: strings.NewReader("payload")}
	calls := 0
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		_ = request.Body.Close()
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	}, httpclient.Config{Retry: httpclient.RetryConfig{
		MaxAttempts: 2,
		StatusCodes: []int{http.StatusServiceUnavailable},
		Methods:     []string{http.MethodPut},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPut, "https://example.test/resource", strings.NewReader("payload"))
	request.GetBody = func() (io.ReadCloser, error) {
		close(entered)
		<-release
		return recreated, nil
	}
	resultCh := make(chan httpclient.Result, 1)
	go func() { resultCh <- client.Execute(request) }()
	<-entered
	cancel()
	close(release)
	result := <-resultCh
	if result.Response != nil || result.Outcome != httpclient.OutcomeCanceled || result.FailureClass != clientkit.FailureCanceled || !errors.Is(result.Err, context.Canceled) || len(result.Attempts) != 1 || calls != 1 {
		t.Fatalf("Execute() = %#v with %d transport calls, want canceled before retry transport", result, calls)
	}
	if !recreated.closed {
		t.Fatal("body recreated after cancellation was not closed")
	}
}

func TestHTTPConfiguredRedirectPolicy(t *testing.T) {
	t.Run("custom rejection is a policy failure", func(t *testing.T) {
		rejection := errors.New("redirect rejected")
		calls := 0
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			return redirectResponse(request), nil
		}, httpclient.Config{
			HTTPClient: &http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error { return rejection },
			},
			Retry: httpclient.RetryConfig{MaxAttempts: 2, Methods: []string{http.MethodGet}, TransportErrors: httpclient.TransportRetryAll},
		})
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
		result := client.Execute(request)
		if result.Outcome != httpclient.OutcomeExecutionError || !errors.Is(result.Err, rejection) || result.FailureClass != clientkit.FailurePolicy || len(result.Attempts) != 1 || calls != 1 {
			t.Fatalf("Execute() = %#v with %d calls, want non-retried redirect policy failure", result, calls)
		}
	})

	t.Run("construction-time callback is retained", func(t *testing.T) {
		rejection := errors.New("construction-time redirect rejection")
		configuredChecks := 0
		replacementChecks := 0
		calls := 0
		configuredHTTPClient := &http.Client{
			Transport: testRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return redirectResponse(request), nil
				}
				return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
			}),
			CheckRedirect: func(*http.Request, []*http.Request) error {
				configuredChecks++
				return rejection
			},
		}
		client, err := httpclient.New(httpclient.Config{
			Config:     clientkit.Config{Name: "redirect-snapshot", Observer: clientkit.NopObserver{}},
			BaseURL:    "https://example.test",
			HTTPClient: configuredHTTPClient,
			Propagator: httpclient.NopHeaderPropagator{},
			Retry:      httpclient.NoRetryConfig(),
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		configuredHTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			replacementChecks++
			return nil
		}

		request, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
		result := client.Execute(request)
		if result.Response == nil || result.Outcome != httpclient.OutcomeExecutionError || result.FailureClass != clientkit.FailurePolicy || !errors.Is(result.Err, rejection) || calls != 1 || configuredChecks != 1 || replacementChecks != 0 {
			t.Fatalf("Execute() = %#v with calls/checks %d/%d/%d, want construction-time redirect rejection", result, calls, configuredChecks, replacementChecks)
		}
	})

	t.Run("ErrUseLastResponse returns caller-owned response", func(t *testing.T) {
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			return redirectResponse(request), nil
		}, httpclient.Config{HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Retry: httpclient.NoRetryConfig()})
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
		result := client.Execute(request)
		if result.Outcome != httpclient.OutcomeResponseRejected || result.Err != nil || result.Response == nil || result.StatusCode != http.StatusFound || result.FailureClass != clientkit.FailureRemoteResponse {
			t.Fatalf("Execute() = %#v, want rejected redirect response with nil error", result)
		}
		_ = result.Response.Body.Close()

		request, _ = http.NewRequest(http.MethodGet, "https://example.test/start", nil)
		response, err := client.Do(request)
		if err != nil || response == nil || response.StatusCode != http.StatusFound {
			t.Fatalf("Do() = (%v, %v), want caller-owned redirect response", response, err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatalf("Body.Close() error = %v", err)
		}
	})
}

func TestHTTPExecutionPolicyOptionValidation(t *testing.T) {
	calls := 0
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	}, httpclient.Config{})
	tests := []struct {
		name    string
		options httpclient.ExecuteOptions
	}{
		{name: "invalid retry safety", options: httpclient.ExecuteOptions{RetrySafety: "invalid"}},
		{name: "retry configured and disabled", options: httpclient.ExecuteOptions{Retry: httpclient.ExecutionRetry{Config: httpclient.NoRetryConfig(), Disable: true}}},
		{name: "invalid retry policy", options: httpclient.ExecuteOptions{Retry: httpclient.ExecutionRetry{Config: httpclient.RetryConfig{MaxAttempts: -1}}}},
		{name: "negative total timeout", options: httpclient.ExecuteOptions{Timeouts: httpclient.ExecutionTimeouts{Timeout: -time.Second}}},
		{name: "total timeout set and disabled", options: httpclient.ExecuteOptions{Timeouts: httpclient.ExecutionTimeouts{Timeout: time.Second, DisableTimeout: true}}},
		{name: "negative attempt timeout", options: httpclient.ExecuteOptions{Timeouts: httpclient.ExecutionTimeouts{AttemptTimeout: -time.Second}}},
		{name: "attempt timeout set and disabled", options: httpclient.ExecuteOptions{Timeouts: httpclient.ExecutionTimeouts{AttemptTimeout: time.Second, DisableAttemptTimeout: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &trackedReadCloser{Reader: strings.NewReader("payload")}
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", body)
			result := client.ExecuteWithOptions(request, test.options)
			if result.Outcome != httpclient.OutcomeExecutionError || result.Err == nil || result.FailureClass != clientkit.FailurePolicy || len(result.Attempts) != 0 {
				t.Fatalf("ExecuteWithOptions() = %#v, want pre-attempt policy failure", result)
			}
			if !body.closed {
				t.Fatal("invalid execution policy did not close request body")
			}
		})
	}
	if calls != 0 {
		t.Fatalf("transport calls = %d, want 0 for invalid options", calls)
	}
}

type executionObserver struct {
	clientkit.NopObserver
	end func(clientkit.OperationEndEvent)
}

func (o executionObserver) StartOperation(ctx context.Context, _ clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	return ctx, clientkit.OperationObservationFunc(func(_ context.Context, event clientkit.OperationEndEvent) {
		if o.end != nil {
			o.end(event)
		}
	})
}

type retryRecordingObserver struct {
	clientkit.NopObserver
	attemptCount int
	retryCount   int
	end          clientkit.OperationEndEvent
}

func (o *retryRecordingObserver) StartOperation(ctx context.Context, _ clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	return ctx, clientkit.OperationObservationFunc(func(_ context.Context, event clientkit.OperationEndEvent) {
		o.end = event
	})
}

func (o *retryRecordingObserver) ObserveAttempt(context.Context, clientkit.AttemptEvent) {
	o.attemptCount++
}

func (o *retryRecordingObserver) ObserveRetry(context.Context, clientkit.RetryEvent) {
	o.retryCount++
}

type trackedReadCloser struct {
	io.Reader
	closed bool
}

func (b *trackedReadCloser) Close() error {
	b.closed = true
	return nil
}

func mustRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest(%q) error = %v", rawURL, err)
	}
	return request
}

func redirectResponse(request *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusFound,
		Header:     http.Header{"Location": []string{"/next"}},
		Body:       io.NopCloser(strings.NewReader("redirect")),
		Request:    request,
	}
}
