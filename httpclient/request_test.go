package httpclient_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPClientNewRequest(t *testing.T) {
	client, err := httpclient.New(httpclient.Config{
		Config:     clientkit.Config{Name: "payments", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test/api/v1",
		Propagator: httpclient.NopHeaderPropagator{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request, err := client.NewRequest(context.Background(), http.MethodPost, "payments?limit=1", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if request.URL.String() != "https://example.test/api/v1/payments?limit=1" || request.Method != http.MethodPost {
		t.Fatalf("NewRequest() = %s %s, want resolved POST request", request.Method, request.URL)
	}
	if request.GetBody == nil {
		t.Fatal("NewRequest did not preserve net/http body replay support")
	}
}

func TestHTTPClientNewRequestPreservesEscapedBasePath(t *testing.T) {
	client, err := httpclient.New(httpclient.Config{
		Config:     clientkit.Config{Name: "escaped-path", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test/api%2Fv1",
		Propagator: httpclient.NopHeaderPropagator{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request, err := client.NewRequest(context.Background(), http.MethodGet, "payments", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if got := request.URL.EscapedPath(); got != "/api%2Fv1/payments" {
		t.Fatalf("escaped request path = %q, want /api%%2Fv1/payments", got)
	}
}

func TestHTTPClientNewRequestUsesRFC3986Resolution(t *testing.T) {
	client, err := httpclient.New(httpclient.Config{
		Config:     clientkit.Config{Name: "resolution", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test/api/v1/",
		Propagator: httpclient.NopHeaderPropagator{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	tests := []struct {
		reference string
		want      string
	}{
		{reference: "../admin", want: "https://example.test/api/admin"},
		{reference: "../../x", want: "https://example.test/x"},
		{reference: "/absolute-path", want: "https://example.test/absolute-path"},
		{reference: "./foo", want: "https://example.test/api/v1/foo"},
		{reference: "%2e%2e/admin", want: "https://example.test/api/v1/%2e%2e/admin"},
		{reference: "foo%2Fbar", want: "https://example.test/api/v1/foo%2Fbar"},
		{reference: "?limit=1", want: "https://example.test/api/v1/?limit=1"},
		{reference: "", want: "https://example.test/api/v1/"},
	}
	for _, test := range tests {
		t.Run(test.reference, func(t *testing.T) {
			request, requestErr := client.NewRequest(context.Background(), http.MethodGet, test.reference, nil)
			if requestErr != nil {
				t.Fatalf("NewRequest(%q) error = %v", test.reference, requestErr)
			}
			if got := request.URL.String(); got != test.want {
				t.Fatalf("NewRequest(%q) URL = %q, want %q", test.reference, got, test.want)
			}
		})
	}
}

func TestHTTPClientNewRequestTreatsEncodedBaseSlashAsPathData(t *testing.T) {
	client, err := httpclient.New(httpclient.Config{
		Config:     clientkit.Config{Name: "encoded-directory", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test/api%2F",
		Propagator: httpclient.NopHeaderPropagator{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request, err := client.NewRequest(context.Background(), http.MethodGet, "payments", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if got := request.URL.String(); got != "https://example.test/api%2F/payments" {
		t.Fatalf("request URL = %q, want encoded base segment plus structural slash", got)
	}
}

func TestHTTPClientNewRequestValidation(t *testing.T) {
	client, err := httpclient.New(httpclient.Config{
		Config:     clientkit.Config{Name: "payments", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test",
		Propagator: httpclient.NopHeaderPropagator{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	tests := []struct {
		name   string
		ctx    context.Context
		method string
		path   string
	}{
		{name: "nil context", method: http.MethodGet, path: "/payments"},
		{name: "absolute URL", ctx: context.Background(), method: http.MethodGet, path: "https://other.test/payments"},
		{name: "network path", ctx: context.Background(), method: http.MethodGet, path: "//other.test/payments"},
		{name: "fragment", ctx: context.Background(), method: http.MethodGet, path: "/payments#details"},
		{name: "invalid URL", ctx: context.Background(), method: http.MethodGet, path: "%"},
		{name: "invalid method", ctx: context.Background(), method: "BAD METHOD", path: "/payments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if request, err := client.NewRequest(test.ctx, test.method, test.path, nil); err == nil || request != nil {
				t.Fatalf("NewRequest() = (%v, %v), want validation error", request, err)
			}
		})
	}

	var nilClient *httpclient.Client
	if request, err := nilClient.NewRequest(context.Background(), http.MethodGet, "/", nil); err == nil || request != nil {
		t.Fatalf("nil Client.NewRequest() = (%v, %v), want configuration error", request, err)
	}
}
