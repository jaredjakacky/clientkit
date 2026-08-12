package httpclient

import (
	"context"
	"net/http"
	"testing"
)

func FuzzContextHeaderValueBoundary(f *testing.F) {
	for _, value := range []string{
		"request-123", "", "value\twith-tab", "value\nwith-newline",
		"value\rwith-return", string([]byte{0x7f}), string(make([]byte, DefaultContextHeaderMaxValueBytes+1)),
	} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		propagator, err := NewContextHeaderPropagator(ContextHeaderBinding{
			Header: "X-Fuzz-Value",
			Provider: HeaderValueProviderFunc(func(context.Context) (string, bool) {
				return value, true
			}),
		})
		if err != nil {
			t.Fatalf("NewContextHeaderPropagator() error = %v", err)
		}
		headers := make(http.Header)
		propagator.Inject(context.Background(), headers)
		if validContextHeaderValue(value, DefaultContextHeaderMaxValueBytes) {
			if got := headers.Get("X-Fuzz-Value"); got != value {
				t.Fatalf("valid value %q was not propagated: %q", value, got)
			}
		} else if _, exists := headers["X-Fuzz-Value"]; exists {
			t.Fatalf("unsafe value %q reached outbound headers %#v", value, headers)
		}
	})
}
