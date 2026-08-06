package tcpclient

import (
	"crypto/tls"
	"errors"
	"net"
	"strings"

	"github.com/jaredjakacky/clientkit/internal/configvalue"
)

func normalizeTLSConfig(cfg TLSConfig, address string) (normalizedTLSConfig, error) {
	if !cfg.Enabled {
		if cfg.Config != nil || cfg.ServerName != "" || cfg.DisableServerNameInference || cfg.HandshakeTimeout != 0 || cfg.DisableHandshakeTimeout {
			return normalizedTLSConfig{}, errors.New("clientkit: TCP TLS configuration requires TLS to be enabled")
		}
		return normalizedTLSConfig{}, nil
	}

	handshakeTimeout, err := configvalue.Duration("TCP TLS handshake timeout", cfg.HandshakeTimeout, cfg.DisableHandshakeTimeout, DefaultTLSHandshakeTimeout, 0)
	if err != nil {
		return normalizedTLSConfig{}, err
	}

	explicitServerName := strings.TrimSpace(cfg.ServerName)
	if cfg.ServerName != "" && explicitServerName == "" {
		return normalizedTLSConfig{}, errors.New("clientkit: TCP TLS server name must not be empty")
	}
	configuredServerName := ""
	if cfg.Config != nil {
		configuredServerName = cfg.Config.ServerName
	}
	if explicitServerName != "" && configuredServerName != "" && explicitServerName != configuredServerName {
		return normalizedTLSConfig{}, errors.New("clientkit: TCP TLS server name conflicts with tls.Config ServerName")
	}

	serverName := explicitServerName
	if serverName == "" {
		serverName = configuredServerName
	}
	if serverName == "" && !cfg.DisableServerNameInference {
		serverName = configuredAddressHost(address)
	}

	var policy *tls.Config
	if cfg.Config == nil {
		policy = DefaultTLSConfig(serverName)
	} else {
		policy = cfg.Config.Clone()
		policy.ServerName = serverName
	}
	if policy.ServerName == "" && !policy.InsecureSkipVerify {
		return normalizedTLSConfig{}, errors.New("clientkit: TCP TLS server name is required")
	}

	return normalizedTLSConfig{enabled: true, config: policy, handshakeTimeout: handshakeTimeout}, nil
}

func configuredAddressHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(host)
}

func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS13:
		return "1.3"
	default:
		return ""
	}
}
