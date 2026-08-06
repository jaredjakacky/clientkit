// Package failure provides internal standard-library network failure
// classification shared by protocol implementations.
package failure

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"syscall"

	"github.com/jaredjakacky/clientkit"
)

// Network classifies a network error using the stable Clientkit vocabulary.
// tlsStage identifies failures known to have occurred during a TLS handshake.
func Network(err error, tlsStage bool) clientkit.FailureClass {
	if errors.Is(err, context.Canceled) {
		return clientkit.FailureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return clientkit.FailureTimeout
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return clientkit.FailureTimeout
	}
	if tlsStage || isTLSFailure(err) {
		return clientkit.FailureTLS
	}

	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return clientkit.FailureNameResolution
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return clientkit.FailureConnectionRefused
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return clientkit.FailureConnectionReset
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) {
		return clientkit.FailureConnectionClosed
	}
	return clientkit.FailureTransport
}

func isTLSFailure(err error) bool {
	var certificateVerificationError *tls.CertificateVerificationError
	if errors.As(err, &certificateVerificationError) {
		return true
	}
	var alertError tls.AlertError
	if errors.As(err, &alertError) {
		return true
	}
	var recordHeaderError tls.RecordHeaderError
	if errors.As(err, &recordHeaderError) {
		return true
	}
	var unknownAuthorityError x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityError) {
		return true
	}
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return true
	}
	var certificateInvalidError x509.CertificateInvalidError
	if errors.As(err, &certificateInvalidError) {
		return true
	}
	var constraintViolationError x509.ConstraintViolationError
	if errors.As(err, &constraintViolationError) {
		return true
	}
	var insecureAlgorithmError x509.InsecureAlgorithmError
	if errors.As(err, &insecureAlgorithmError) {
		return true
	}
	var systemRootsError x509.SystemRootsError
	return errors.As(err, &systemRootsError)
}
