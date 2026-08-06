package httpclient_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHeaderValueProviderFunc(t *testing.T) {
	if value, ok := httpclient.HeaderValueProviderFunc(nil).Value(context.Background()); ok || value != "" {
		t.Fatalf("nil Value() = (%q, %t), want empty unavailable value", value, ok)
	}
	ctx := context.WithValue(context.Background(), headerContextKey("id"), "request-123")
	provider := httpclient.HeaderValueProviderFunc(func(ctx context.Context) (string, bool) {
		value, ok := ctx.Value(headerContextKey("id")).(string)
		return value, ok
	})
	if value, ok := provider.Value(ctx); !ok || value != "request-123" {
		t.Fatalf("Value() = (%q, %t), want request-123", value, ok)
	}
}

func TestNewContextHeaderPropagatorValidation(t *testing.T) {
	provider := httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { return "value", true })
	tests := []struct {
		name     string
		bindings []httpclient.ContextHeaderBinding
	}{
		{name: "missing header", bindings: []httpclient.ContextHeaderBinding{{Provider: provider}}},
		{name: "surrounding whitespace", bindings: []httpclient.ContextHeaderBinding{{Header: " X-Test ", Provider: provider}}},
		{name: "invalid header", bindings: []httpclient.ContextHeaderBinding{{Header: "X Test", Provider: provider}}},
		{name: "nil provider", bindings: []httpclient.ContextHeaderBinding{{Header: "X-Test"}}},
		{name: "typed nil provider", bindings: []httpclient.ContextHeaderBinding{{Header: "X-Test", Provider: httpclient.HeaderValueProviderFunc(nil)}}},
		{name: "invalid existing policy", bindings: []httpclient.ContextHeaderBinding{{Header: "X-Test", Provider: provider, ExistingPolicy: "invalid"}}},
		{name: "negative value limit", bindings: []httpclient.ContextHeaderBinding{{Header: "X-Test", Provider: provider, MaxValueBytes: -1}}},
		{name: "value limit set and disabled", bindings: []httpclient.ContextHeaderBinding{{Header: "X-Test", Provider: provider, MaxValueBytes: 1, DisableValueLimit: true}}},
		{
			name: "case-insensitive duplicate",
			bindings: []httpclient.ContextHeaderBinding{
				{Header: "X-Test", Provider: provider},
				{Header: "x-test", Provider: provider},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if propagator, err := httpclient.NewContextHeaderPropagator(test.bindings...); err == nil || propagator != nil {
				t.Fatalf("NewContextHeaderPropagator() = (%T, %v), want validation error", propagator, err)
			}
		})
	}

	propagator, err := httpclient.NewContextHeaderPropagator()
	if err != nil {
		t.Fatalf("NewContextHeaderPropagator() error = %v", err)
	}
	if _, ok := propagator.(httpclient.NopHeaderPropagator); !ok {
		t.Fatalf("empty propagator = %T, want NopHeaderPropagator", propagator)
	}
}

func TestContextHeaderPropagation(t *testing.T) {
	ctx := context.WithValue(context.Background(), headerContextKey("id"), "context-value")
	provider := httpclient.HeaderValueProviderFunc(func(ctx context.Context) (string, bool) {
		value, ok := ctx.Value(headerContextKey("id")).(string)
		return value, ok
	})

	preserve, err := httpclient.NewContextHeaderPropagator(httpclient.ContextHeaderBinding{
		Header:   "x-request-id",
		Provider: provider,
	})
	if err != nil {
		t.Fatalf("NewContextHeaderPropagator() error = %v", err)
	}
	nonCanonicalKey := strings.ToLower("X-Request-ID")
	headers := http.Header{nonCanonicalKey: []string{"existing"}}
	preserve.Inject(ctx, headers)
	if got := headers[nonCanonicalKey]; !reflect.DeepEqual(got, []string{"existing"}) {
		t.Fatalf("preserved header = %v, want [existing]", got)
	}
	overwrite, err := httpclient.NewContextHeaderPropagator(httpclient.ContextHeaderBinding{
		Header:         "x-request-id",
		Provider:       provider,
		ExistingPolicy: httpclient.OverwriteExistingHeader,
	})
	if err != nil {
		t.Fatalf("NewContextHeaderPropagator() error = %v", err)
	}
	overwrite.Inject(ctx, headers)
	if got := headers.Values("X-Request-ID"); !reflect.DeepEqual(got, []string{"context-value"}) {
		t.Fatalf("overwritten header = %v, want [context-value]", got)
	}
	if _, exists := headers[nonCanonicalKey]; exists {
		t.Fatal("overwrite retained non-canonical duplicate header key")
	}
}

func TestContextHeaderPropagationOmitsUnsafeValuesAndPanics(t *testing.T) {
	tests := []struct {
		name     string
		provider httpclient.HeaderValueProvider
		limit    int
		disable  bool
		want     string
	}{
		{name: "unavailable", provider: httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { return "value", false })},
		{name: "empty", provider: httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { return "", true })},
		{name: "newline", provider: httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { return "bad\nvalue", true })},
		{name: "delete", provider: httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { return "bad\x7fvalue", true })},
		{name: "too long", provider: httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { return "12345", true }), limit: 4},
		{name: "panic", provider: httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { panic("provider") })},
		{name: "tab allowed", provider: httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { return "a\tb", true }), want: "a\tb"},
		{name: "limit disabled", provider: httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { return strings.Repeat("x", 300), true }), disable: true, want: strings.Repeat("x", 300)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			propagator, err := httpclient.NewContextHeaderPropagator(httpclient.ContextHeaderBinding{
				Header:            "X-Test",
				Provider:          test.provider,
				MaxValueBytes:     test.limit,
				DisableValueLimit: test.disable,
			})
			if err != nil {
				t.Fatalf("NewContextHeaderPropagator() error = %v", err)
			}
			headers := make(http.Header)
			propagator.Inject(context.Background(), headers)
			if got := headers.Get("X-Test"); got != test.want {
				t.Fatalf("X-Test = %q, want %q", got, test.want)
			}
			propagator.Inject(context.Background(), nil)
		})
	}
}

func TestRequestMetadataPropagator(t *testing.T) {
	requestProvider := httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { return "request-123", true })
	correlationProvider := httpclient.HeaderValueProviderFunc(func(context.Context) (string, bool) { return "correlation-456", true })
	propagator, err := httpclient.NewRequestMetadataPropagator(httpclient.RequestMetadataConfig{
		RequestID:     requestProvider,
		CorrelationID: correlationProvider,
	})
	if err != nil {
		t.Fatalf("NewRequestMetadataPropagator() error = %v", err)
	}
	headers := make(http.Header)
	propagator.Inject(context.Background(), headers)
	if headers.Get(httpclient.DefaultRequestIDHeader) != "request-123" || headers.Get(httpclient.DefaultCorrelationIDHeader) != "correlation-456" {
		t.Fatalf("metadata headers = %#v, want conventional values", headers)
	}

	tests := []struct {
		name   string
		config httpclient.RequestMetadataConfig
	}{
		{name: "request header without provider", config: httpclient.RequestMetadataConfig{RequestIDHeader: "X-Custom"}},
		{name: "correlation header without provider", config: httpclient.RequestMetadataConfig{CorrelationIDHeader: "X-Custom"}},
		{name: "invalid existing policy", config: httpclient.RequestMetadataConfig{ExistingPolicy: "invalid"}},
		{name: "negative value limit", config: httpclient.RequestMetadataConfig{MaxValueBytes: -1}},
		{name: "value limit set and disabled", config: httpclient.RequestMetadataConfig{MaxValueBytes: 1, DisableValueLimit: true}},
		{name: "duplicate headers", config: httpclient.RequestMetadataConfig{RequestID: requestProvider, CorrelationID: correlationProvider, RequestIDHeader: "X-Same", CorrelationIDHeader: "x-same"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := httpclient.NewRequestMetadataPropagator(test.config); err == nil || got != nil {
				t.Fatalf("NewRequestMetadataPropagator() = (%T, %v), want validation error", got, err)
			}
		})
	}

	nop, err := httpclient.NewRequestMetadataPropagator(httpclient.RequestMetadataConfig{})
	if err != nil {
		t.Fatalf("zero NewRequestMetadataPropagator() error = %v", err)
	}
	if _, ok := nop.(httpclient.NopHeaderPropagator); !ok {
		t.Fatalf("zero metadata propagator = %T, want NopHeaderPropagator", nop)
	}
}

type headerContextKey string
