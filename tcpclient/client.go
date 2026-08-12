package tcpclient

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/internal/configvalue"
	clientkitotel "github.com/jaredjakacky/clientkit/otel"
)

// Client establishes raw network connections and exposes Clientkit identity,
// readiness, cached health, and observability.
type Client struct {
	core *clientkit.Client

	network     string
	address     string
	dialTimeout time.Duration
	keepAlive   time.Duration
	dialContext DialContextFunc
	dialer      *net.Dialer
	check       normalizedCheckConfig
	tls         normalizedTLSConfig
}

var (
	_ clientkit.RegisteredClient        = (*Client)(nil)
	_ clientkit.HealthChecker           = (*Client)(nil)
	_ clientkit.HealthCheckConfigurable = (*Client)(nil)
)

// New validates and constructs a TCP client without performing network I/O.
// A nil Observer selects the default OpenTelemetry observer. Caller-supplied
// TLS configuration is cloned; a custom DialContext is retained and must follow
// the DialContextFunc contract.
func New(cfg Config) (*Client, error) {
	if err := cfg.Config.Validate(); err != nil {
		return nil, err
	}

	network := strings.ToLower(strings.TrimSpace(cfg.Network))
	if network == "" {
		network = DefaultNetwork
	}
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, errors.New("clientkit: TCP network must be tcp, tcp4, or tcp6")
	}

	address := strings.TrimSpace(cfg.Address)
	if address == "" {
		return nil, errors.New("clientkit: TCP address is required")
	}
	if strings.Contains(address, "://") {
		return nil, errors.New("clientkit: TCP address must not include a URL scheme")
	}

	if cfg.DialContext == nil {
		if err := validateTCPAddress(address); err != nil {
			return nil, err
		}
	}

	dialTimeout, err := configvalue.Duration("TCP dial timeout", cfg.DialTimeout, cfg.DisableDialTimeout, DefaultDialTimeout, 0)
	if err != nil {
		return nil, err
	}

	keepAlive := time.Duration(0)
	if cfg.DialContext != nil {
		if cfg.KeepAlive != 0 || cfg.DisableKeepAlive {
			return nil, errors.New("clientkit: TCP keepalive cannot be configured with a custom dial context")
		}
	} else {
		keepAlive, err = configvalue.Duration("TCP keepalive", cfg.KeepAlive, cfg.DisableKeepAlive, DefaultKeepAlive, -1)
		if err != nil {
			return nil, err
		}
	}

	check, err := normalizeCheckConfig(cfg.Check)
	if err != nil {
		return nil, err
	}
	if cfg.Config.ReadinessPolicy.BlocksReadiness() && !check.enabled {
		return nil, errors.New("clientkit: readiness-blocking TCP client requires an enabled health check")
	}
	tlsConfig, err := normalizeTLSConfig(cfg.TLS, address)
	if err != nil {
		return nil, err
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

	var dialer *net.Dialer
	if cfg.DialContext == nil {
		dialer = &net.Dialer{Timeout: dialTimeout, KeepAlive: keepAlive}
	}

	return &Client{
		core:        client,
		network:     network,
		address:     address,
		dialTimeout: dialTimeout,
		keepAlive:   keepAlive,
		dialContext: cfg.DialContext,
		dialer:      dialer,
		check:       check,
		tls:         tlsConfig,
	}, nil
}

// Name returns the client's immutable logical name.
func (c *Client) Name() string {
	if c == nil || c.core == nil {
		return ""
	}
	return c.core.Name()
}

// Protocol returns the client's stable TCP family identity.
func (c *Client) Protocol() string {
	if c == nil || c.core == nil {
		return ""
	}
	return ProtocolTCP
}

// ReadinessPolicy returns the client's immutable normalized readiness policy.
func (c *Client) ReadinessPolicy() clientkit.ReadinessPolicy {
	if c == nil || c.core == nil {
		return clientkit.ReadinessOptional
	}
	return c.core.ReadinessPolicy()
}

func validateTCPAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("clientkit: TCP address must include a host and numeric port")
	}
	if strings.TrimSpace(host) == "" {
		return errors.New("clientkit: TCP address host is required")
	}
	if port == "" {
		return errors.New("clientkit: TCP address port is required")
	}
	for _, character := range port {
		if character < '0' || character > '9' {
			return errors.New("clientkit: TCP address port must be between 1 and 65535")
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("clientkit: TCP address port must be between 1 and 65535")
	}
	return nil
}
