package httpclient_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPResultResponseBodyDoesNotChangeCompletedTelemetry(t *testing.T) {
	t.Run("close without reading preserves success", func(t *testing.T) {
		ended := make(chan clientkit.OperationEndEvent, 2)
		body := &trackedReadCloser{Reader: strings.NewReader("payload")}
		client := lifecycleHTTPClient(t, body, ended)
		response := executeLifecycleRequest(t, client)
		event := <-ended
		if !event.Succeeded || event.Err != nil {
			t.Fatalf("operation end = %#v, want header-time success", event)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatalf("Body.Close() error = %v", err)
		}
		if !body.closed {
			t.Fatal("response body was not closed")
		}
	})

	t.Run("read failure does not replace observed outcome", func(t *testing.T) {
		ended := make(chan clientkit.OperationEndEvent, 2)
		readErr := io.ErrUnexpectedEOF
		client := lifecycleHTTPClient(t, &failingResponseBody{readErr: readErr}, ended)
		response := executeLifecycleRequest(t, client)
		event := <-ended
		if !event.Succeeded || event.Outcome != string(httpclient.OutcomeSuccess) || event.FailureClass != clientkit.FailureNone || event.Err != nil {
			t.Fatalf("operation end = %#v, want header-time success", event)
		}
		if _, err := response.Body.Read(make([]byte, 1)); !errors.Is(err, readErr) {
			t.Fatalf("Body.Read() error = %v, want %v", err, readErr)
		}
	})

	t.Run("close failure does not replace observed outcome", func(t *testing.T) {
		ended := make(chan clientkit.OperationEndEvent, 2)
		closeErr := errors.New("close response body")
		client := lifecycleHTTPClient(t, &failingResponseBody{Reader: strings.NewReader("payload"), closeErr: closeErr}, ended)
		response := executeLifecycleRequest(t, client)
		event := <-ended
		if !event.Succeeded || event.Outcome != string(httpclient.OutcomeSuccess) || event.FailureClass != clientkit.FailureNone || event.Err != nil {
			t.Fatalf("operation end = %#v, want header-time success", event)
		}
		if err := response.Body.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("Body.Close() error = %v, want %v", err, closeErr)
		}
	})

	t.Run("no body completes before return", func(t *testing.T) {
		ended := make(chan clientkit.OperationEndEvent, 2)
		client := lifecycleHTTPClient(t, http.NoBody, ended)
		response := executeLifecycleRequest(t, client)
		if response.Body != http.NoBody {
			t.Fatalf("response body = %T, want http.NoBody", response.Body)
		}
		select {
		case event := <-ended:
			if !event.Succeeded {
				t.Fatalf("operation end = %#v, want success", event)
			}
		default:
			t.Fatal("operation did not complete before returning a response without a body")
		}
	})
}

func TestHTTPResponseBodyHonorsExecutionTimeouts(t *testing.T) {
	tests := []struct {
		name       string
		config     httpclient.Config
		options    httpclient.DoOptions
		newContext func(*testing.T) context.Context
	}{
		{
			name:   "total timeout",
			config: httpclient.Config{Timeout: 10 * time.Millisecond, DisableAttemptTimeout: true},
			newContext: func(*testing.T) context.Context {
				return context.Background()
			},
		},
		{
			name:   "attempt timeout",
			config: httpclient.Config{DisableTimeout: true, AttemptTimeout: 10 * time.Millisecond},
			newContext: func(*testing.T) context.Context {
				return context.Background()
			},
		},
		{
			name: "caller deadline survives disabled Clientkit timeouts",
			config: httpclient.Config{
				Timeout:        time.Second,
				AttemptTimeout: time.Second,
			},
			options: httpclient.DoOptions{Timeouts: httpclient.ExecutionTimeouts{
				DisableTimeout:        true,
				DisableAttemptTimeout: true,
			}},
			newContext: func(t *testing.T) context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ended := make(chan clientkit.OperationEndEvent, 2)
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       contextBlockingBody{ctx: request.Context()},
					Request:    request,
				}, nil
			}, withHTTPObserver(test.config, func(event clientkit.OperationEndEvent) { ended <- event }))

			request, err := http.NewRequestWithContext(test.newContext(t), http.MethodGet, "https://example.test/resource", nil)
			if err != nil {
				t.Fatalf("http.NewRequestWithContext() error = %v", err)
			}
			result := client.ExecuteWithOptions(request, test.options)
			if result.Outcome != httpclient.OutcomeSuccess || result.Response == nil {
				t.Fatalf("ExecuteWithOptions() = %#v, want successful response headers", result)
			}

			event := <-ended
			if !event.Succeeded || event.Outcome != string(httpclient.OutcomeSuccess) || event.FailureClass != clientkit.FailureNone || event.Err != nil {
				t.Fatalf("operation end = %#v, want header-time success", event)
			}

			if _, err := result.Response.Body.Read(make([]byte, 1)); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Body.Read() error = %v, want deadline exceeded", err)
			}
			if err := result.Response.Body.Close(); err != nil {
				t.Fatalf("Body.Close() error = %v", err)
			}
			select {
			case duplicate := <-ended:
				t.Fatalf("operation ended more than once: %#v", duplicate)
			default:
			}
		})
	}
}

func TestHTTPResponseBodyCompletionReleasesExecutionContext(t *testing.T) {
	tests := []struct {
		name     string
		complete func(*testing.T, io.ReadCloser)
	}{
		{
			name: "EOF",
			complete: func(t *testing.T, body io.ReadCloser) {
				t.Helper()
				if _, err := io.ReadAll(body); err != nil {
					t.Fatalf("ReadAll() error = %v", err)
				}
			},
		},
		{
			name: "close",
			complete: func(t *testing.T, body io.ReadCloser) {
				t.Helper()
				if err := body.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var executionContext context.Context
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				executionContext = request.Context()
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("payload")),
					Request:    request,
				}, nil
			}, httpclient.Config{Retry: httpclient.NoRetryConfig()})
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
			result := client.Execute(request)
			if result.Response == nil || executionContext == nil {
				t.Fatalf("Execute() = %#v with context %v, want response and execution context", result, executionContext)
			}
			select {
			case <-executionContext.Done():
				t.Fatal("execution context ended before response-body completion")
			default:
			}

			test.complete(t, result.Response.Body)
			select {
			case <-executionContext.Done():
			case <-time.After(100 * time.Millisecond):
				t.Fatal("execution context remained active after response-body completion")
			}
		})
	}
}

func TestHTTPResponseBodyPreservesSwitchingProtocolWriter(t *testing.T) {
	body := &upgradedResponseBody{Reader: strings.NewReader("server data")}
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusSwitchingProtocols,
			Header:     make(http.Header),
			Body:       body,
			Request:    request,
		}, nil
	}, httpclient.Config{Retry: httpclient.NoRetryConfig()})
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/upgrade", nil)
	response, err := client.Do(request)
	if err != nil || response == nil {
		t.Fatalf("Do() = (%v, %v), want switching-protocol response", response, err)
	}
	upgraded, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatalf("response body = %T, want io.ReadWriteCloser", response.Body)
	}
	if _, err := upgraded.Write([]byte("client data")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := body.written.String(); got != "client data" {
		t.Fatalf("underlying write = %q, want client data", got)
	}
	if err := upgraded.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !body.closed {
		t.Fatal("upgraded response body was not closed")
	}

	ordinary := lifecycleHTTPClient(t, io.NopCloser(strings.NewReader("payload")), make(chan clientkit.OperationEndEvent, 1))
	ordinaryResponse := executeLifecycleRequest(t, ordinary)
	defer ordinaryResponse.Body.Close()
	if _, ok := ordinaryResponse.Body.(io.Writer); ok {
		t.Fatalf("ordinary response body = %T, unexpectedly implements io.Writer", ordinaryResponse.Body)
	}
}

func withHTTPObserver(cfg httpclient.Config, end func(clientkit.OperationEndEvent)) httpclient.Config {
	cfg.Config = clientkit.Config{Name: "body-timeout", Observer: executionObserver{end: end}}
	cfg.Retry = httpclient.NoRetryConfig()
	return cfg
}

func lifecycleHTTPClient(t *testing.T, body io.ReadCloser, ended chan<- clientkit.OperationEndEvent) *httpclient.Client {
	t.Helper()
	return newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: request}, nil
	}, httpclient.Config{Config: clientkit.Config{Name: "body-lifecycle", Observer: executionObserver{
		end: func(event clientkit.OperationEndEvent) { ended <- event },
	}}})
}

func executeLifecycleRequest(t *testing.T, client *httpclient.Client) *http.Response {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeSuccess || result.Err != nil || result.Response == nil {
		t.Fatalf("Execute() = %#v, want successful response headers", result)
	}
	return result.Response
}

type failingResponseBody struct {
	io.Reader
	readErr  error
	closeErr error
}

type contextBlockingBody struct {
	ctx context.Context
}

type upgradedResponseBody struct {
	io.Reader
	written bytes.Buffer
	closed  bool
}

func (body *upgradedResponseBody) Write(buffer []byte) (int, error) {
	return body.written.Write(buffer)
}

func (body *upgradedResponseBody) Close() error {
	body.closed = true
	return nil
}

func (body contextBlockingBody) Read([]byte) (int, error) {
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (contextBlockingBody) Close() error {
	return nil
}

func (body *failingResponseBody) Read(buffer []byte) (int, error) {
	if body.readErr != nil {
		return 0, body.readErr
	}
	return body.Reader.Read(buffer)
}

func (body *failingResponseBody) Close() error {
	return body.closeErr
}
