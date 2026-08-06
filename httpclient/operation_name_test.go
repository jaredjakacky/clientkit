package httpclient_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPOperationNameValidation(t *testing.T) {
	calls := 0
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	}, httpclient.Config{})

	invalid := []httpclient.OperationName{
		"Invalid",
		"1invalid",
		"invalid.",
		"invalid/name",
		httpclient.OperationName(strings.Repeat("a", httpclient.MaxOperationNameBytes+1)),
	}
	for _, operation := range invalid {
		t.Run(string(operation), func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
			result := client.ExecuteWithOptions(request, httpclient.DoOptions{Operation: operation})
			if result.Err == nil || result.FailureClass != clientkit.FailurePolicy || len(result.Attempts) != 0 {
				t.Fatalf("ExecuteWithOptions() = %#v, want pre-attempt policy failure", result)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("transport calls = %d, want zero for invalid operation names", calls)
	}
}

func TestHTTPOperationNameReachesEveryLifecycleEvent(t *testing.T) {
	tests := []struct {
		name      string
		operation httpclient.OperationName
		want      string
	}{
		{name: "default", want: httpclient.OperationHTTPRequest},
		{name: "semantic", operation: "payments.lookup", want: "payments.lookup"},
		{name: "maximum length", operation: httpclient.OperationName(strings.Repeat("a", httpclient.MaxOperationNameBytes)), want: strings.Repeat("a", httpclient.MaxOperationNameBytes)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := &operationNameObserver{}
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
			}, httpclient.Config{Config: clientkit.Config{Name: "operation-name", Observer: observer}})
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
			result := client.ExecuteWithOptions(request, httpclient.DoOptions{Operation: test.operation})
			if result.Outcome != httpclient.OutcomeSuccess {
				t.Fatalf("ExecuteWithOptions() = %#v, want success", result)
			}
			if observer.start != test.want || observer.attempt != test.want || observer.end != test.want {
				t.Fatalf("lifecycle operations = (%q, %q, %q), want %q", observer.start, observer.attempt, observer.end, test.want)
			}
		})
	}
}

type operationNameObserver struct {
	clientkit.NopObserver
	start   string
	attempt string
	end     string
}

func (observer *operationNameObserver) StartOperation(ctx context.Context, event clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	observer.start = event.Operation
	return ctx, clientkit.OperationObservationFunc(func(_ context.Context, event clientkit.OperationEndEvent) {
		observer.end = event.Operation
	})
}

func (observer *operationNameObserver) ObserveAttempt(_ context.Context, event clientkit.AttemptEvent) {
	observer.attempt = event.Operation
}
