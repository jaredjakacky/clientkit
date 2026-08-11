package httpclient_test

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func TestSafeAndMultiHeaderPropagator(t *testing.T) {
	headers := http.Header{"Existing": []string{"value"}}
	panicking := httpclient.HeaderPropagatorFunc(func(_ context.Context, headers http.Header) {
		headers.Set("Partial", "value")
		panic("inject")
	})
	httpclient.SafeHeaderPropagator(panicking).Inject(context.Background(), headers)
	if !reflect.DeepEqual(headers, http.Header{"Existing": []string{"value"}}) {
		t.Fatalf("headers after panic = %#v, want restored input", headers)
	}

	order := make([]string, 0, 3)
	multi := httpclient.MultiHeaderPropagator(
		httpclient.HeaderPropagatorFunc(func(_ context.Context, headers http.Header) {
			order = append(order, "first")
			headers.Set("First", "1")
		}),
		panicking,
		httpclient.HeaderPropagatorFunc(func(_ context.Context, headers http.Header) {
			order = append(order, "last")
			headers.Set("Last", "3")
		}),
	)
	headers = make(http.Header)
	multi.Inject(context.Background(), headers)
	if !reflect.DeepEqual(order, []string{"first", "last"}) {
		t.Fatalf("propagator order = %v, want [first last]", order)
	}
	if headers.Get("First") != "1" || headers.Get("Last") != "3" || headers.Get("Partial") != "" {
		t.Fatalf("multi headers = %#v, want successful propagators only", headers)
	}

	for name, propagator := range map[string]httpclient.HeaderPropagator{
		"nil safe":    httpclient.SafeHeaderPropagator(nil),
		"empty multi": httpclient.MultiHeaderPropagator(),
		"nil multi":   httpclient.MultiHeaderPropagator(nil),
	} {
		t.Run(name, func(t *testing.T) {
			propagator.Inject(context.Background(), nil)
		})
	}

	single := httpclient.MultiHeaderPropagator(nil, httpclient.HeaderPropagatorFunc(func(_ context.Context, headers http.Header) {
		headers.Set("Single", "value")
	}))
	headers = make(http.Header)
	single.Inject(context.Background(), headers)
	if headers.Get("Single") != "value" {
		t.Fatalf("single multi propagator headers = %#v, want injected value", headers)
	}
}

func TestHTTPClientCapturesDefaultOTelPropagatorAtConstruction(t *testing.T) {
	// OpenTelemetry's propagator is process-global, so this test must remain
	// sequential and restore the application setting when it completes.
	previous := apiotel.GetTextMapPropagator()
	t.Cleanup(func() { apiotel.SetTextMapPropagator(previous) })

	apiotel.SetTextMapPropagator(staticTextMapPropagator{value: "first"})
	first, err := httpclient.New(httpclient.Config{
		Config:  clientkit.Config{Name: "first-propagator", Observer: clientkit.NopObserver{}},
		BaseURL: "https://example.test",
	})
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}

	apiotel.SetTextMapPropagator(staticTextMapPropagator{value: "second"})
	second, err := httpclient.New(httpclient.Config{
		Config:  clientkit.Config{Name: "second-propagator", Observer: clientkit.NopObserver{}},
		BaseURL: "https://example.test",
	})
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}

	firstHeaders := make(http.Header)
	first.Propagator().Inject(context.Background(), firstHeaders)
	secondHeaders := make(http.Header)
	second.Propagator().Inject(context.Background(), secondHeaders)
	if firstHeaders.Get("X-Test-Trace") != "first" || secondHeaders.Get("X-Test-Trace") != "second" {
		t.Fatalf("captured propagation = (%q, %q), want (first, second)", firstHeaders.Get("X-Test-Trace"), secondHeaders.Get("X-Test-Trace"))
	}
	first.Propagator().Inject(context.Background(), nil)
}

func TestHTTPPropagationRunsPerAttemptWithoutMutatingCallerRequest(t *testing.T) {
	propagationCalls := 0
	var attemptHeaders []http.Header
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		attemptHeaders = append(attemptHeaders, request.Header.Clone())
		statusCode := http.StatusServiceUnavailable
		if len(attemptHeaders) == 2 {
			statusCode = http.StatusNoContent
		}
		return &http.Response{StatusCode: statusCode, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	}, httpclient.Config{
		Propagator: httpclient.HeaderPropagatorFunc(func(_ context.Context, headers http.Header) {
			propagationCalls++
			headers.Set("X-Attempt", strconv.Itoa(propagationCalls))
		}),
		Retry: httpclient.RetryConfig{
			MaxAttempts: 2,
			StatusCodes: []int{http.StatusServiceUnavailable},
			Methods:     []string{http.MethodGet},
		},
	})
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
	request.Header.Set("Caller", "preserved")
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeSuccess || propagationCalls != 2 || len(attemptHeaders) != 2 {
		t.Fatalf("Execute() = %#v with %d propagation calls and %d attempts, want retry success", result, propagationCalls, len(attemptHeaders))
	}
	if attemptHeaders[0].Get("X-Attempt") != "1" || attemptHeaders[1].Get("X-Attempt") != "2" {
		t.Fatalf("attempt headers = %#v, want fresh per-attempt injection", attemptHeaders)
	}
	if request.Header.Get("X-Attempt") != "" || request.Header.Get("Caller") != "preserved" {
		t.Fatalf("caller request headers = %#v, want unchanged", request.Header)
	}
}

func TestHTTPPropagationRunsPerRoundTripAcrossRedirects(t *testing.T) {
	propagationCalls := 0
	var wireHeaders []http.Header
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		wireHeaders = append(wireHeaders, request.Header.Clone())
		if len(wireHeaders) == 1 {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"/final"}},
				Body:       http.NoBody,
				Request:    request,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	}, httpclient.Config{
		Propagator: httpclient.HeaderPropagatorFunc(func(_ context.Context, headers http.Header) {
			propagationCalls++
			headers.Set("X-Wire-Attempt", strconv.Itoa(propagationCalls))
		}),
		Retry: httpclient.NoRetryConfig(),
	})
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeSuccess || result.StatusCode != http.StatusNoContent || len(result.Attempts) != 1 {
		t.Fatalf("Execute() = %#v, want one logical execution attempt ending in 204", result)
	}
	if propagationCalls != 2 || len(wireHeaders) != 2 {
		t.Fatalf("propagation calls/wire requests = (%d, %d), want (2, 2)", propagationCalls, len(wireHeaders))
	}
	if wireHeaders[0].Get("X-Wire-Attempt") != "1" || wireHeaders[1].Get("X-Wire-Attempt") != "2" {
		t.Fatalf("wire headers = %#v, want fresh injection for redirect resend", wireHeaders)
	}
	if request.Header.Get("X-Wire-Attempt") != "" {
		t.Fatalf("caller request headers = %#v, want unchanged", request.Header)
	}
}

type staticTextMapPropagator struct {
	value string
}

func (propagator staticTextMapPropagator) Inject(_ context.Context, carrier propagation.TextMapCarrier) {
	carrier.Set("X-Test-Trace", propagator.value)
}

func (staticTextMapPropagator) Extract(ctx context.Context, _ propagation.TextMapCarrier) context.Context {
	return ctx
}

func (staticTextMapPropagator) Fields() []string {
	return []string{"X-Test-Trace"}
}
