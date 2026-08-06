package tcpclient

import (
	"crypto/tls"
	"time"
)

const (
	// DefaultNetwork is the default network used for TCP dialing.
	DefaultNetwork = "tcp"
	// DefaultDialTimeout bounds connection establishment by default.
	DefaultDialTimeout = 5 * time.Second
	// DefaultKeepAlive configures the default TCP keepalive interval.
	DefaultKeepAlive = 30 * time.Second
	// DefaultCheckTimeout bounds the complete TCP health check by default.
	DefaultCheckTimeout = 5 * time.Second
	// DefaultCheckStaleAfter is the default age after which cached TCP health is
	// stale.
	DefaultCheckStaleAfter = 30 * time.Second
	// DefaultTLSHandshakeTimeout bounds TLS handshaking by default.
	DefaultTLSHandshakeTimeout = 10 * time.Second
	// DefaultTLSMinVersion is the minimum version in Clientkit's default TLS
	// policy.
	DefaultTLSMinVersion = tls.VersionTLS12
)

// DefaultTLSConfig returns a new certificate-verifying TLS configuration with
// the supplied server name and Clientkit's minimum TLS version. It does not
// load certificates or mutate global TLS state.
func DefaultTLSConfig(serverName string) *tls.Config {
	return &tls.Config{MinVersion: DefaultTLSMinVersion, ServerName: serverName}
}
