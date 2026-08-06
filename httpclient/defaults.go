package httpclient

import (
	"net"
	"net/http"
	"time"
)

const (
	// DefaultTimeout limits the complete Clientkit request execution.
	DefaultTimeout = 30 * time.Second
	// DefaultAttemptTimeout limits one Clientkit request attempt, including final
	// response-body use.
	DefaultAttemptTimeout = 10 * time.Second
	// DefaultDialTimeout limits connection establishment.
	DefaultDialTimeout = 5 * time.Second
	// DefaultDialKeepAlive controls TCP keep-alive probes.
	DefaultDialKeepAlive = 30 * time.Second
	// DefaultMaxIdleConns limits idle connections across all hosts.
	DefaultMaxIdleConns = 100
	// DefaultMaxIdleConnsPerHost limits idle connections retained per host.
	DefaultMaxIdleConnsPerHost = 20
	// DefaultMaxConnsPerHost bounds active and idle connections per host.
	DefaultMaxConnsPerHost = 100
	// DefaultIdleConnTimeout limits how long idle connections remain pooled.
	DefaultIdleConnTimeout = 90 * time.Second
	// DefaultTLSHandshakeTimeout limits TLS handshakes.
	DefaultTLSHandshakeTimeout = 10 * time.Second
	// DefaultResponseHeaderTimeout limits waiting for response headers.
	DefaultResponseHeaderTimeout = 10 * time.Second
	// DefaultExpectContinueTimeout limits waiting for a 100-continue response.
	DefaultExpectContinueTimeout = 1 * time.Second
	// DefaultRetryMaxAttempts is the total number of attempts, including the initial request.
	DefaultRetryMaxAttempts = 3
	// DefaultRetryBackoff is the delay before the first retry.
	DefaultRetryBackoff = 200 * time.Millisecond
	// DefaultRetryBackoffMultiplier controls exponential retry-delay growth.
	DefaultRetryBackoffMultiplier = 2.0
	// DefaultRetryMaxBackoff caps retry delays.
	DefaultRetryMaxBackoff = 2 * time.Second
	// DefaultRetryJitter bounds random variation applied to retry delays.
	DefaultRetryJitter = 100 * time.Millisecond
	// DefaultRespectRetryAfter enables bounded server-directed retry timing for
	// responses already retryable under the configured policy.
	DefaultRespectRetryAfter = true
	// DefaultMaxRetryAfter caps the server-requested portion of a retry delay.
	DefaultMaxRetryAfter = 30 * time.Second
)

// DefaultTransport returns a new production-oriented HTTP transport. Each call
// returns an independently configurable transport.
func DefaultTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   DefaultDialTimeout,
		KeepAlive: DefaultDialKeepAlive,
	}
	// Build from a literal instead of cloning http.DefaultTransport. The global
	// transport is mutable, so cloning it could silently inherit application or
	// dependency TLS hooks, trust roots, proxy credentials, and protocol policy.
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          DefaultMaxIdleConns,
		MaxIdleConnsPerHost:   DefaultMaxIdleConnsPerHost,
		MaxConnsPerHost:       DefaultMaxConnsPerHost,
		IdleConnTimeout:       DefaultIdleConnTimeout,
		TLSHandshakeTimeout:   DefaultTLSHandshakeTimeout,
		ResponseHeaderTimeout: DefaultResponseHeaderTimeout,
		ExpectContinueTimeout: DefaultExpectContinueTimeout,
	}
}

// DefaultHTTPClient returns a new HTTP client using DefaultTransport. Its
// Timeout remains zero because Clientkit applies operation timeouts with
// contexts.
func DefaultHTTPClient() *http.Client {
	return &http.Client{Transport: DefaultTransport()}
}
