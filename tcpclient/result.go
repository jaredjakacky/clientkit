package tcpclient

import (
	"net"
	"time"

	"github.com/jaredjakacky/clientkit"
)

// Outcome classifies one TCP connection attempt using bounded values.
type Outcome string

const (
	// OutcomeSuccess indicates that a connection was established.
	OutcomeSuccess Outcome = "success"
	// OutcomeTimeout indicates that connection establishment timed out.
	OutcomeTimeout Outcome = "timeout"
	// OutcomeCanceled indicates that connection establishment was canceled.
	OutcomeCanceled Outcome = "canceled"
	// OutcomeDialError indicates another connection-establishment failure.
	OutcomeDialError Outcome = "dial_error"
	// OutcomeTLSError indicates a non-timeout, non-cancellation TLS handshake
	// failure.
	OutcomeTLSError Outcome = "tls_error"
)

// Result describes one completed plaintext or TLS connection attempt. When
// Conn is non-nil, the caller owns it and must close it.
type Result struct {
	// Conn is the successfully established caller-owned connection.
	Conn net.Conn
	// Outcome is the bounded connection-attempt outcome.
	Outcome Outcome
	// FailureClass is the stable classified connection failure and supplements
	// Err and Outcome without replacing either.
	FailureClass clientkit.FailureClass
	// StartedAt is the UTC time at which the dial operation began.
	StartedAt time.Time
	// Duration is the complete dial operation duration.
	Duration time.Duration
	// Err is the caller-visible connection-establishment error. When a custom
	// dialer returns after its context is done, Err is the winning context error.
	Err error
}
