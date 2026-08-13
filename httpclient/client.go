package httpclient

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/internal/configvalue"
	"github.com/jaredjakacky/clientkit/internal/httpotel"
	clientkitotel "github.com/jaredjakacky/clientkit/otel"
)

// Client executes HTTP requests and optionally maintains cached dependency
// health through explicitly enabled checks.
type Client struct {
	core               *clientkit.Client
	baseURL            *url.URL
	httpClient         *http.Client
	timeout            time.Duration
	attemptTimeout     time.Duration
	check              normalizedCheckConfig
	retry              RetryConfig
	propagator         HeaderPropagator
	responseClassifier safeResponseClassifier
	allowCrossOrigin   bool
	allowHostOverride  bool
	transportInjects   bool
}

var (
	_ clientkit.RegisteredClient        = (*Client)(nil)
	_ clientkit.HealthChecker           = (*Client)(nil)
	_ clientkit.HealthCheckConfigurable = (*Client)(nil)
	_ clientkit.IdleConnectionCloser    = (*Client)(nil)
)

// New validates and constructs an HTTP client without performing network I/O.
// Nil HTTPClient, Observer, Propagator, and ResponseClassifier fields select
// their documented production defaults. Health checks remain disabled unless
// Check.Enabled is true. A non-nil HTTPClient is shallow-copied; its referenced
// transport, jar, and callback state remain shared and caller-owned.
func New(cfg Config) (*Client, error) {
	if err := cfg.Config.Validate(); err != nil {
		return nil, err
	}

	timeout, err := configvalue.Duration("timeout", cfg.Timeout, cfg.DisableTimeout, DefaultTimeout, 0)
	if err != nil {
		return nil, err
	}
	attemptTimeout, err := configvalue.Duration("attempt timeout", cfg.AttemptTimeout, cfg.DisableAttemptTimeout, DefaultAttemptTimeout, 0)
	if err != nil {
		return nil, err
	}

	retry := normalizeRetryConfig(cfg.Retry)
	if err := validateRetryConfig(retry); err != nil {
		return nil, err
	}

	check, err := normalizeCheckConfig(cfg.Check)
	if err != nil {
		return nil, err
	}
	if cfg.Config.ReadinessPolicy.BlocksReadiness() && !check.enabled {
		return nil, errors.New("clientkit: readiness-blocking HTTP client requires an enabled health check")
	}

	baseURLValue := strings.TrimSpace(cfg.BaseURL)
	if baseURLValue == "" {
		return nil, errors.New("clientkit: base URL is required")
	}

	baseURL, err := url.Parse(baseURLValue)
	if err != nil {
		return nil, errors.New("clientkit: base URL is invalid")
	}
	if (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Hostname() == "" {
		return nil, errors.New("clientkit: base URL must use http or https and include a host")
	}
	port := baseURL.Port()
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return nil, errors.New("clientkit: base URL port must be between 1 and 65535")
		}
	}
	if baseURL.User != nil {
		return nil, errors.New("clientkit: base URL must not include user information")
	}
	if baseURL.Fragment != "" {
		return nil, errors.New("clientkit: base URL must not include a fragment")
	}
	if baseURL.RawQuery != "" || baseURL.ForceQuery {
		return nil, errors.New("clientkit: base URL must not include query parameters")
	}
	if baseURL.Path == "" {
		baseURL.Path = "/"
	} else if !strings.HasSuffix(baseURL.EscapedPath(), "/") {
		baseURL.Path += "/"
		if baseURL.RawPath != "" {
			baseURL.RawPath += "/"
		}
	}

	usesOwnedHTTPClient := cfg.HTTPClient == nil
	usesDefaultObserver := cfg.Config.Observer == nil
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = DefaultHTTPClient()
	} else {
		copiedHTTPClient := *httpClient
		httpClient = &copiedHTTPClient
	}

	baseConfig := cfg.Config
	if baseConfig.Observer == nil {
		telemetry, err := clientkitotel.New()
		if err != nil {
			return nil, err
		}
		baseConfig.Observer = telemetry
	}
	client, err := clientkit.New(baseConfig)
	if err != nil {
		return nil, err
	}

	propagator := cfg.Propagator
	if propagator == nil {
		propagator = newDefaultOpenTelemetryHeaderPropagator()
	}
	propagator = SafeHeaderPropagator(propagator)

	transportInjects := false
	if usesOwnedHTTPClient && usesDefaultObserver {
		instrumented, err := httpotel.NewTransport(httpClient.Transport, httpotel.Config{
			Inject: propagator.Inject,
		})
		if err != nil {
			return nil, err
		}
		httpClient.Transport = instrumented
		transportInjects = true
	}

	return &Client{
		core:               client,
		baseURL:            baseURL,
		httpClient:         httpClient,
		timeout:            timeout,
		attemptTimeout:     attemptTimeout,
		check:              check,
		retry:              retry,
		propagator:         propagator,
		responseClassifier: normalizeResponseClassifier(cfg.ResponseClassifier),
		allowCrossOrigin:   cfg.AllowCrossOrigin,
		allowHostOverride:  cfg.AllowHostOverride,
		transportInjects:   transportInjects,
	}, nil
}

// Name returns the client's immutable logical name.
func (c *Client) Name() string {
	if c == nil || c.core == nil {
		return ""
	}
	return c.core.Name()
}

// Protocol returns the client's stable HTTP family identity.
func (c *Client) Protocol() string {
	if c == nil || c.core == nil {
		return ""
	}
	return ProtocolHTTP
}

// ReadinessPolicy returns the client's immutable normalized readiness policy.
func (c *Client) ReadinessPolicy() clientkit.ReadinessPolicy {
	if c == nil || c.core == nil {
		return clientkit.ReadinessOptional
	}
	return c.core.ReadinessPolicy()
}

// CloseIdleConnections synchronously closes currently idle connections held by
// the configured HTTP client. It does not cancel or wait for active requests,
// permanently close this Client, or prevent future requests and health checks
// from opening new connections. A nil or unusable Client is a no-op.
//
// This explicit call also applies to the construction-time transport of a
// caller-supplied HTTP client. If that transport is shared, other users of the
// same idle pool may be affected. Clientkit neither detects that sharing nor
// claims ownership, and it never performs this cleanup automatically.
// Applications own active-work draining and shutdown ordering.
//
// Individual cleanup can be requested directly:
//
//	payments.CloseIdleConnections()
func (c *Client) CloseIdleConnections() {
	if c == nil || c.httpClient == nil {
		return
	}
	c.httpClient.CloseIdleConnections()
}

// Propagator returns the client's concurrency-safe outbound header propagator.
// A nil or unusable client returns NopHeaderPropagator.
func (c *Client) Propagator() HeaderPropagator {
	if c == nil || c.core == nil || c.propagator == nil {
		return NopHeaderPropagator{}
	}
	return c.propagator
}

// ResponseClassifier returns the client's immutable panic-safe ordinary HTTP
// response classifier. A nil or unusable client returns the default 2xx
// classifier.
func (c *Client) ResponseClassifier() ResponseClassifier {
	if c == nil || c.core == nil || c.responseClassifier.classifier == nil {
		return normalizeResponseClassifier(nil)
	}
	return c.responseClassifier
}

// Snapshot returns the client's identity, readiness policy, and effective
// cached health. It never performs a synchronous dependency check.
func (c *Client) Snapshot() clientkit.ClientSnapshot {
	return clientkit.ClientSnapshot{
		Name:            c.Name(),
		Protocol:        c.Protocol(),
		ReadinessPolicy: c.ReadinessPolicy(),
		Health:          c.Health(),
	}
}
