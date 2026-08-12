package httpclient

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jaredjakacky/clientkit"
)

func FuzzNewRequestRetainsConfiguredOrigin(f *testing.F) {
	client, err := New(Config{
		Config:     clientkit.Config{Name: "request-fuzz", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test:8443/api/v1/",
		Propagator: NopHeaderPropagator{},
	})
	if err != nil {
		f.Fatalf("New() error = %v", err)
	}
	for _, reference := range []string{
		"payments", "../admin", "../../x", "/absolute", "%2e%2e/admin",
		"foo%2Fbar", "?token=secret", "", "//other.test/path", "https://other.test/path",
	} {
		f.Add(reference)
	}

	f.Fuzz(func(t *testing.T, reference string) {
		request, requestErr := client.NewRequest(context.Background(), http.MethodGet, reference, nil)
		if requestErr != nil {
			return
		}
		if request.URL.Scheme != "https" || request.URL.Hostname() != "example.test" || request.URL.Port() != "8443" {
			t.Fatalf("accepted reference %q changed origin to %q", reference, request.URL)
		}
		if request.URL.User != nil || request.URL.Fragment != "" || request.URL.RawFragment != "" {
			t.Fatalf("accepted reference %q produced unsafe URL %#v", reference, request.URL)
		}
		parsed, parseErr := url.Parse(request.URL.String())
		if parseErr != nil || parsed.String() != request.URL.String() {
			t.Fatalf("accepted reference %q produced unstable URL %q: %v", reference, request.URL, parseErr)
		}
	})
}

func FuzzValidateRequestOriginDoesNotAcceptAnotherOrigin(f *testing.F) {
	client, err := New(Config{
		Config:     clientkit.Config{Name: "origin-fuzz", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test:8443/api/",
		Propagator: NopHeaderPropagator{},
	})
	if err != nil {
		f.Fatalf("New() error = %v", err)
	}
	for _, rawURL := range []string{
		"https://example.test:8443/path", "https://EXAMPLE.test:8443/path",
		"https://example.test/path", "http://example.test:8443/path",
		"https://other.test:8443/path", "file://example.test:8443/path",
		"https://user@example.test:8443/path", "https://[::1]:8443/path",
	} {
		f.Add(rawURL)
	}

	f.Fuzz(func(t *testing.T, rawURL string) {
		parsed, parseErr := url.Parse(rawURL)
		if parseErr != nil {
			return
		}
		request := &http.Request{Method: http.MethodGet, URL: parsed, Header: make(http.Header)}
		if err := client.validateRequestOrigin(request); err != nil {
			return
		}
		if request.URL.Scheme != "https" || request.URL.Hostname() == "" ||
			!strings.EqualFold(request.URL.Hostname(), "example.test") || effectiveURLPort(request.URL.Scheme, request.URL.Port()) != "8443" {
			t.Fatalf("origin validation accepted %q", rawURL)
		}
	})
}
