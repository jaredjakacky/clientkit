package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestRetryRecreatesBodyWhenNextAttemptBegins(t *testing.T) {
	events := make([]string, 0, 8)
	retryDelay := 5 * time.Millisecond
	observer := &orderedObserver{events: &events}
	attempt := 0
	transport := testRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		attempt++
		events = append(events, fmt.Sprintf("roundtrip-%d", attempt))
		if err := request.Body.Close(); err != nil {
			t.Fatalf("close attempt body: %v", err)
		}
		statusCode := http.StatusServiceUnavailable
		if attempt == 2 {
			statusCode = http.StatusNoContent
		}
		responseBody := io.ReadCloser(http.NoBody)
		if attempt == 1 {
			responseBody = eventReadCloser{
				Reader: strings.NewReader("retry response"),
				close:  func() { events = append(events, "response-close") },
			}
		}
		return &http.Response{
			StatusCode: statusCode,
			Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
			Header:     make(http.Header),
			Body:       responseBody,
			Request:    request,
		}, nil
	})

	client, err := httpclient.New(httpclient.Config{
		Config: clientkit.Config{
			Name:     "retry-order",
			Observer: observer,
		},
		BaseURL:    "https://example.test",
		HTTPClient: &http.Client{Transport: transport},
		Retry: httpclient.RetryConfig{
			MaxAttempts:       2,
			Backoff:           retryDelay,
			BackoffMultiplier: 1,
			StatusCodes:       []int{http.StatusServiceUnavailable},
			Methods:           []string{http.MethodPut},
		},
	})
	if err != nil {
		t.Fatalf("construct client: %v", err)
	}

	request, err := http.NewRequest(http.MethodPut, "https://example.test/resource", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("construct request: %v", err)
	}
	request.GetBody = func() (io.ReadCloser, error) {
		events = append(events, "get-body")
		if elapsed := time.Since(observer.retryObservedAt); elapsed < retryDelay {
			t.Errorf("GetBody called %v after retry event, want at least %v", elapsed, retryDelay)
		}
		return io.NopCloser(strings.NewReader("payload")), nil
	}

	result := client.Execute(request)
	if result.Err != nil || result.Outcome != httpclient.OutcomeSuccess || len(result.Attempts) != 2 {
		t.Fatalf("result = %#v, want successful two-attempt operation", result)
	}

	want := []string{
		"operation-start",
		"roundtrip-1",
		"attempt-1",
		"response-close",
		"retry-1",
		"get-body",
		"roundtrip-2",
		"attempt-2",
		"operation-end",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestRetryRejectsInvalidRecreatedBodies(t *testing.T) {
	recreateErr := errors.New("recreation failed")
	tests := []struct {
		name       string
		getBody    func() (io.ReadCloser, error)
		recreated  *trackedReadCloser
		wantErr    error
		wantDetail string
	}{
		{
			name:       "nil body",
			getBody:    func() (io.ReadCloser, error) { return nil, nil },
			wantDetail: "GetBody returned nil",
		},
		{
			name:       "body returned with error",
			recreated:  &trackedReadCloser{Reader: strings.NewReader("unused")},
			wantErr:    recreateErr,
			wantDetail: "recreate request body",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			getBody := test.getBody
			if test.recreated != nil {
				getBody = func() (io.ReadCloser, error) {
					return test.recreated, recreateErr
				}
			}
			responseBody := &trackedReadCloser{Reader: strings.NewReader("retry response")}
			observer := &retryRecordingObserver{}
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				_ = request.Body.Close()
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: responseBody, Request: request}, nil
			}, httpclient.Config{
				Config: clientkit.Config{Name: "body-recreation", Observer: observer},
				Retry: httpclient.RetryConfig{
					MaxAttempts: 2,
					StatusCodes: []int{http.StatusServiceUnavailable},
					Methods:     []string{http.MethodPut},
				},
			})
			request, _ := http.NewRequest(http.MethodPut, "https://example.test/resource", strings.NewReader("payload"))
			request.GetBody = getBody
			result := client.Execute(request)
			if result.Outcome != httpclient.OutcomeTransportError || result.FailureClass != clientkit.FailureRequest || result.Err == nil || !strings.Contains(result.Err.Error(), test.wantDetail) || len(result.Attempts) != 1 || result.Response != nil {
				t.Fatalf("Execute() = %#v, want explicit body-recreation failure", result)
			}
			if test.wantErr != nil && !errors.Is(result.Err, test.wantErr) {
				t.Fatalf("Execute() error = %v, want wrapped %v", result.Err, test.wantErr)
			}
			if !responseBody.closed {
				t.Fatal("intermediate response body was not closed")
			}
			if test.recreated != nil && !test.recreated.closed {
				t.Fatal("GetBody result returned with an error was not closed")
			}
			if observer.retryCount != 1 || observer.attemptCount != 1 || observer.end.Err == nil {
				t.Fatalf("observer = %#v, want one attempt, one retry, and terminal recreation error", observer)
			}
		})
	}
}

func TestRetryClosesIntermediateResponsesWithoutDraining(t *testing.T) {
	for _, contentLength := range []int64{-1, 0, 1024, 1 << 20} {
		t.Run(fmt.Sprintf("content-length-%d", contentLength), func(t *testing.T) {
			intermediateBody := &countingResponseBody{}
			attempt := 0
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				attempt++
				status := http.StatusServiceUnavailable
				body := io.ReadCloser(intermediateBody)
				responseLength := contentLength
				if attempt == 2 {
					status = http.StatusNoContent
					body = http.NoBody
					responseLength = 0
				}
				return &http.Response{
					StatusCode:    status,
					Header:        make(http.Header),
					Body:          body,
					ContentLength: responseLength,
					Request:       request,
				}, nil
			}, httpclient.Config{Retry: httpclient.RetryConfig{
				MaxAttempts: 2,
				StatusCodes: []int{http.StatusServiceUnavailable},
				Methods:     []string{http.MethodGet},
			}})

			result := client.Execute(mustRequest(t, "https://example.test/resource"))
			if result.Err != nil || result.Outcome != httpclient.OutcomeSuccess {
				t.Fatalf("Execute() = %#v, want retry success", result)
			}
			if intermediateBody.reads != 0 || !intermediateBody.closed {
				t.Fatalf("intermediate body reads/closed = %d/%t, want 0/true", intermediateBody.reads, intermediateBody.closed)
			}
		})
	}
}

type countingResponseBody struct {
	reads  int
	closed bool
}

func (b *countingResponseBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (b *countingResponseBody) Close() error {
	b.closed = true
	return nil
}

type orderedObserver struct {
	clientkit.NopObserver
	events          *[]string
	retryObservedAt time.Time
}

func (observer *orderedObserver) StartOperation(ctx context.Context, _ clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	*observer.events = append(*observer.events, "operation-start")
	return ctx, clientkit.OperationObservationFunc(func(context.Context, clientkit.OperationEndEvent) {
		*observer.events = append(*observer.events, "operation-end")
	})
}

func (observer *orderedObserver) ObserveAttempt(_ context.Context, event clientkit.AttemptEvent) {
	*observer.events = append(*observer.events, fmt.Sprintf("attempt-%d", event.Number))
}

func (observer *orderedObserver) ObserveRetry(_ context.Context, event clientkit.RetryEvent) {
	observer.retryObservedAt = time.Now()
	*observer.events = append(*observer.events, fmt.Sprintf("retry-%d", event.AfterAttempt))
}

type eventReadCloser struct {
	io.Reader
	close func()
}

func (body eventReadCloser) Close() error {
	body.close()
	return nil
}

type testRoundTripperFunc func(*http.Request) (*http.Response, error)

func (fn testRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
