package httpclient_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"testing"

	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPProductionDefaults(t *testing.T) {
	first := httpclient.DefaultTransport()
	second := httpclient.DefaultTransport()
	if first == second {
		t.Fatal("DefaultTransport returned shared transport")
	}
	if first.MaxIdleConns != httpclient.DefaultMaxIdleConns ||
		first.MaxIdleConnsPerHost != httpclient.DefaultMaxIdleConnsPerHost ||
		first.MaxConnsPerHost != httpclient.DefaultMaxConnsPerHost ||
		first.IdleConnTimeout != httpclient.DefaultIdleConnTimeout ||
		first.TLSHandshakeTimeout != httpclient.DefaultTLSHandshakeTimeout ||
		first.ResponseHeaderTimeout != httpclient.DefaultResponseHeaderTimeout ||
		first.ExpectContinueTimeout != httpclient.DefaultExpectContinueTimeout ||
		!first.ForceAttemptHTTP2 || first.DialContext == nil || first.Proxy == nil {
		t.Fatalf("DefaultTransport() = %#v, want documented defaults", first)
	}
	first.MaxIdleConns = 1
	if second.MaxIdleConns != httpclient.DefaultMaxIdleConns {
		t.Fatal("mutating one default transport affected another")
	}

	client := httpclient.DefaultHTTPClient()
	if client.Timeout != 0 {
		t.Fatalf("DefaultHTTPClient().Timeout = %v, want 0", client.Timeout)
	}
	if _, ok := client.Transport.(*http.Transport); !ok {
		t.Fatalf("DefaultHTTPClient().Transport = %T, want *http.Transport", client.Transport)
	}

	// DefaultTransport must remain usable even when an application replaces
	// net/http's process-wide default with a different RoundTripper type.
	previousDefault := http.DefaultTransport
	http.DefaultTransport = testRoundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, io.EOF })
	t.Cleanup(func() { http.DefaultTransport = previousDefault })
	if fallback := httpclient.DefaultTransport(); fallback == nil || fallback.DialContext == nil {
		t.Fatalf("DefaultTransport() fallback = %#v, want configured transport", fallback)
	}
}

func TestDefaultTransportDoesNotInheritMutableGlobalPolicy(t *testing.T) {
	// http.DefaultTransport is process-global and mutable. Clientkit defaults
	// must not inherit another component's trust or protocol decisions.
	previousDefault := http.DefaultTransport
	http.DefaultTransport = &http.Transport{
		TLSClientConfig:    &tls.Config{ServerName: "inherited.test"},
		ProxyConnectHeader: http.Header{"Proxy-Authorization": []string{"secret"}},
	}
	t.Cleanup(func() { http.DefaultTransport = previousDefault })

	transport := httpclient.DefaultTransport()
	if transport.TLSClientConfig != nil {
		t.Fatalf("TLSClientConfig = %#v, want secure Go defaults", transport.TLSClientConfig)
	}
	if transport.ProxyConnectHeader != nil {
		t.Fatalf("ProxyConnectHeader = %#v, want no inherited proxy credentials", transport.ProxyConnectHeader)
	}
}
