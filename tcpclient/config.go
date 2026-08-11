package tcpclient

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/jaredjakacky/clientkit"
)

// DialContextFunc replaces built-in raw connection establishment for a
// configured network and address. Clientkit still applies its dial timeout and
// optional TLS stage. If the function already returns a TLS-secured connection,
// TLSConfig.Enabled must remain false to avoid wrapping it twice. The function
// owns socket options such as TCP keepalive. Implementations must be safe for
// concurrent use and honor context cancellation. Clientkit cannot forcibly stop
// a function that ignores its context. Once the function returns, Clientkit
// rejects the result if its context is done and closes any returned connection.
type DialContextFunc func(context.Context, string, string) (net.Conn, error)

// ConnectionProbe evaluates protocol-level health over an established
// connection. Implementations must honor ctx, remain concurrency-safe, and must
// not retain or close conn. Clientkit applies the context deadline, closes the
// connection on cancellation, always closes it after the probe, and rejects an
// assessment returned after the probe context is done. Clientkit cannot forcibly
// stop a probe that ignores its context.
type ConnectionProbe interface {
	Probe(context.Context, net.Conn) clientkit.HealthAssessment
}

// ConnectionProbeFunc adapts a function to ConnectionProbe.
type ConnectionProbeFunc func(context.Context, net.Conn) clientkit.HealthAssessment

// Probe invokes fn. A nil function returns a stable policy failure rather than
// panicking.
func (fn ConnectionProbeFunc) Probe(ctx context.Context, conn net.Conn) clientkit.HealthAssessment {
	if fn == nil {
		return clientkit.HealthAssessment{State: clientkit.HealthUnhealthy, FailureClass: clientkit.FailurePolicy, Message: "TCP health probe is not configured"}
	}
	return fn(ctx, conn)
}

// Config configures a raw TCP client.
type Config struct {
	// Config supplies the shared Clientkit identity, readiness, and observer.
	clientkit.Config

	// Network selects the dialing network. Empty defaults to DefaultNetwork. A
	// custom DialContext receives the trimmed, lowercased value and may support
	// values beyond tcp, tcp4, and tcp6.
	Network string
	// Address is the required destination passed to the dialer after trimming.
	// The built-in dialer requires host:numeric-port form; a custom DialContext
	// receives the trimmed value without further address validation.
	Address string

	// DialTimeout bounds built-in connection establishment and the acceptance of
	// a custom DialContext result. A custom function that ignores its context can
	// delay Dial beyond this duration, but its late result is rejected and any
	// returned connection is closed. Zero uses DefaultDialTimeout.
	DialTimeout time.Duration
	// DisableDialTimeout disables the connection-establishment timeout.
	DisableDialTimeout bool

	// KeepAlive configures the built-in net.Dialer TCP keepalive interval. Zero
	// uses DefaultKeepAlive. It cannot be set with DialContext.
	KeepAlive time.Duration
	// DisableKeepAlive disables built-in TCP keepalive configuration. It cannot
	// be set with DialContext.
	DisableKeepAlive bool

	// DialContext replaces the built-in net.Dialer when non-nil. Clientkit still
	// supplies Network and Address, applies DialTimeout to the callback context and
	// accepted result, and performs its optional TLS stage. KeepAlive and
	// DisableKeepAlive must remain zero.
	DialContext DialContextFunc

	// Check configures optional connect-and-close health checks.
	Check CheckConfig
	// TLS configures optional TLS wrapping and handshaking.
	TLS TLSConfig
}

// CheckConfig configures optional TCP connect-and-close health checks.
type CheckConfig struct {
	// Enabled allows direct and registry-driven active health checks.
	Enabled bool

	// Timeout bounds the accepted result of the complete health check, including
	// the optional Probe. A probe that ignores its context can delay Check beyond
	// this duration, but its late assessment is rejected.
	Timeout time.Duration
	// DisableTimeout disables the health-check timeout.
	DisableTimeout bool

	// StaleAfter controls when cached health is projected as unknown. It should
	// exceed the maximum expected completion-to-completion refresh gap, including
	// scheduler wait, check-group execution and queueing, positive jitter, and
	// scheduler delay.
	StaleAfter time.Duration
	// DisableStaleAfter disables cached-health staleness projection.
	DisableStaleAfter bool

	// Probe optionally performs a protocol-level health exchange after the TCP or
	// TLS connection is established. Nil treats successful establishment as
	// healthy.
	Probe ConnectionProbe
}

type normalizedCheckConfig struct {
	enabled    bool
	timeout    time.Duration
	staleAfter time.Duration
	probe      ConnectionProbe
}

// TLSConfig configures optional TLS wrapping after raw connection
// establishment. A supplied Config is cloned and treated as the complete TLS
// policy override. Setting InsecureSkipVerify disables standard certificate
// verification and should be done only deliberately.
type TLSConfig struct {
	// Enabled enables TLS wrapping and a completed handshake before Dial returns.
	Enabled bool

	// Config supplies a complete TLS policy override and is cloned during Client
	// construction. Its ServerName participates in the rules below. As with
	// tls.Config.Clone, callers must not mutate referenced pools, callbacks, or
	// slices after constructing the client.
	Config *tls.Config
	// ServerName explicitly selects the TLS verification name. It must not
	// conflict with Config.ServerName.
	ServerName string

	// DisableServerNameInference prevents inference from Address. Unless
	// certificate verification is disabled in Config, a server name must then be
	// supplied through ServerName or Config.ServerName.
	DisableServerNameInference bool

	// HandshakeTimeout bounds the accepted TLS handshake result. A caller-supplied
	// TLS callback that blocks can delay Dial beyond this duration, but
	// HandshakeContext returns the winning context error once the callback returns.
	HandshakeTimeout time.Duration
	// DisableHandshakeTimeout disables the dedicated TLS handshake timeout.
	DisableHandshakeTimeout bool
}

type normalizedTLSConfig struct {
	enabled          bool
	config           *tls.Config
	handshakeTimeout time.Duration
}
