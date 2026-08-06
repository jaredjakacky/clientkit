package httpclient_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
	"github.com/jaredjakacky/opskit"
)

func TestHTTPMethodTelemetryUsesBoundedVocabulary(t *testing.T) {
	observer := &httpAttributeObserver{}
	transportCalls := 0
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		transportCalls++
		statusCode := http.StatusServiceUnavailable
		if transportCalls == 2 {
			statusCode = http.StatusNoContent
		}
		return &http.Response{
			StatusCode: statusCode,
			Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	}, httpclient.Config{
		Config: clientkit.Config{Name: "method-telemetry", Observer: observer},
		Retry: httpclient.RetryConfig{
			MaxAttempts: 2,
			StatusCodes: []int{http.StatusServiceUnavailable},
			Methods:     []string{"PURGE"},
		},
	})

	request, err := http.NewRequest("PURGE", "https://example.test/resource", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	result := client.ExecuteWithOptions(request, httpclient.DoOptions{
		RetrySafety: httpclient.RetrySafetyIdempotent,
	})
	if result.Err != nil || result.Outcome != httpclient.OutcomeSuccess || len(result.Attempts) != 2 {
		t.Fatalf("ExecuteWithOptions() = %#v, want successful two-attempt operation", result)
	}
	if len(observer.attempts) != 2 || len(observer.retries) != 1 {
		t.Fatalf("observer events = %d attempts and %d retries, want 2 and 1", len(observer.attempts), len(observer.retries))
	}

	// Every neutral lifecycle event must be safe to forward directly to metrics.
	attributeSets := [][]opskit.Attribute{observer.start.Attributes, observer.end.Attributes}
	for _, attempt := range observer.attempts {
		attributeSets = append(attributeSets, attempt.Attributes)
	}
	for _, retry := range observer.retries {
		attributeSets = append(attributeSets, retry.Attributes)
	}
	for index, attributes := range attributeSets {
		if got := httpEventAttribute(attributes, "http.method"); got != "OTHER" {
			t.Errorf("event %d http.method = %q, want OTHER", index, got)
		}
		if httpEventContainsValue(attributes, "PURGE") {
			t.Errorf("event %d exposed custom method PURGE", index)
		}
	}
}

func TestHTTPEmptyMethodUsesGETTelemetryVocabulary(t *testing.T) {
	observer := &httpAttributeObserver{}
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	}, httpclient.Config{Config: clientkit.Config{Name: "default-method", Observer: observer}, Retry: httpclient.NoRetryConfig()})
	requestURL, _ := url.Parse("https://example.test/resource")
	request := &http.Request{URL: requestURL}
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeSuccess || len(observer.attempts) != 1 {
		t.Fatalf("Execute() = %#v, want successful operation", result)
	}
	for index, attributes := range [][]opskit.Attribute{observer.start.Attributes, observer.attempts[0].Attributes, observer.end.Attributes} {
		if got := httpEventAttribute(attributes, "http.method"); got != http.MethodGet {
			t.Errorf("event %d http.method = %q, want GET", index, got)
		}
	}
}

func TestHTTPStatusTelemetryUsesStatusClasses(t *testing.T) {
	tests := []struct {
		statusCode int
		want       string
	}{
		{statusCode: http.StatusSwitchingProtocols, want: "1xx"},
		{statusCode: http.StatusNoContent, want: "2xx"},
		{statusCode: http.StatusNotModified, want: "3xx"},
		{statusCode: http.StatusNotFound, want: "4xx"},
		{statusCode: http.StatusServiceUnavailable, want: "5xx"},
	}
	for _, test := range tests {
		t.Run(test.want, func(t *testing.T) {
			observer := &httpAttributeObserver{}
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.statusCode, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
			}, httpclient.Config{Config: clientkit.Config{Name: "status-class", Observer: observer}, Retry: httpclient.NoRetryConfig()})
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
			client.Execute(request)
			if len(observer.attempts) != 1 || httpEventAttribute(observer.attempts[0].Attributes, "http.status_class") != test.want || httpEventAttribute(observer.end.Attributes, "http.status_class") != test.want {
				t.Fatalf("status %d telemetry = (%#v, %#v), want %q", test.statusCode, observer.attempts, observer.end.Attributes, test.want)
			}
		})
	}
}

type httpAttributeObserver struct {
	clientkit.NopObserver
	start    clientkit.OperationStartEvent
	attempts []clientkit.AttemptEvent
	retries  []clientkit.RetryEvent
	end      clientkit.OperationEndEvent
}

func (observer *httpAttributeObserver) StartOperation(ctx context.Context, event clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	observer.start = event
	return ctx, clientkit.OperationObservationFunc(func(_ context.Context, event clientkit.OperationEndEvent) {
		observer.end = event
	})
}

func (observer *httpAttributeObserver) ObserveAttempt(_ context.Context, event clientkit.AttemptEvent) {
	observer.attempts = append(observer.attempts, event)
}

func (observer *httpAttributeObserver) ObserveRetry(_ context.Context, event clientkit.RetryEvent) {
	observer.retries = append(observer.retries, event)
}

func httpEventAttribute(attributes []opskit.Attribute, key string) string {
	for index := len(attributes) - 1; index >= 0; index-- {
		if attributes[index].Key == key {
			return attributes[index].Value
		}
	}
	return ""
}

func httpEventContainsValue(attributes []opskit.Attribute, forbidden string) bool {
	for _, attribute := range attributes {
		if attribute.Value == forbidden {
			return true
		}
	}
	return false
}
