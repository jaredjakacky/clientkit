package failure_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/internal/failure"
)

func TestNetworkClassifiesStandardNetworkFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want clientkit.FailureClass
	}{
		{name: "canceled", err: fmt.Errorf("request stopped: %w", context.Canceled), want: clientkit.FailureCanceled},
		{name: "deadline", err: fmt.Errorf("request stopped: %w", context.DeadlineExceeded), want: clientkit.FailureTimeout},
		{name: "network timeout", err: &net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}}, want: clientkit.FailureTimeout},
		{name: "DNS timeout", err: &net.DNSError{Err: "lookup timed out", Name: "example.test", IsTimeout: true}, want: clientkit.FailureTimeout},
		{name: "DNS", err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "example.test"}}, want: clientkit.FailureNameResolution},
		{
			name: "connection refused",
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
			},
			want: clientkit.FailureConnectionRefused,
		},
		{name: "connection reset", err: fmt.Errorf("read failed: %w", syscall.ECONNRESET), want: clientkit.FailureConnectionReset},
		{name: "EOF", err: fmt.Errorf("read failed: %w", io.EOF), want: clientkit.FailureConnectionClosed},
		{name: "unexpected EOF", err: fmt.Errorf("read failed: %w", io.ErrUnexpectedEOF), want: clientkit.FailureConnectionClosed},
		{name: "closed network connection", err: fmt.Errorf("read failed: %w", net.ErrClosed), want: clientkit.FailureConnectionClosed},
		{name: "broken pipe", err: fmt.Errorf("write failed: %w", syscall.EPIPE), want: clientkit.FailureConnectionClosed},
		{name: "unrecognized transport error", err: errors.New("network unavailable"), want: clientkit.FailureTransport},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := failure.Network(test.err, false); got != test.want {
				t.Fatalf("Network(%v, false) = %q, want %q", test.err, got, test.want)
			}
		})
	}
}

func TestNetworkRecognizesTLSFailuresThroughWrapping(t *testing.T) {
	t.Parallel()

	certificate := &x509.Certificate{DNSNames: []string{"service.internal"}}
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "certificate verification",
			err: &tls.CertificateVerificationError{
				UnverifiedCertificates: []*x509.Certificate{certificate},
				Err:                    errors.New("certificate verification failed"),
			},
		},
		{name: "TLS alert", err: tls.AlertError(40)},
		{name: "record header", err: tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}},
		{name: "unknown authority", err: x509.UnknownAuthorityError{Cert: certificate}},
		{name: "hostname", err: x509.HostnameError{Certificate: certificate, Host: "api.example.test"}},
		{name: "invalid certificate", err: x509.CertificateInvalidError{Cert: certificate, Reason: x509.Expired}},
		{name: "constraint violation", err: x509.ConstraintViolationError{}},
		{name: "insecure algorithm", err: x509.InsecureAlgorithmError(x509.MD5WithRSA)},
		{name: "system roots", err: x509.SystemRootsError{Err: errors.New("root store unavailable")}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wrapped := fmt.Errorf("TLS negotiation failed: %w", test.err)
			if got := failure.Network(wrapped, false); got != clientkit.FailureTLS {
				t.Fatalf("Network(%T, false) = %q, want %q", test.err, got, clientkit.FailureTLS)
			}
		})
	}
}

func TestNetworkClassificationPrecedence(t *testing.T) {
	t.Parallel()

	dnsFailure := &net.DNSError{Err: "no such host", Name: "example.test"}
	tlsFailure := tls.AlertError(40)
	tests := []struct {
		name     string
		err      error
		tlsStage bool
		want     clientkit.FailureClass
	}{
		{name: "cancellation before deadline and TLS", err: errors.Join(context.Canceled, context.DeadlineExceeded, tlsFailure), tlsStage: true, want: clientkit.FailureCanceled},
		{name: "deadline before TLS", err: errors.Join(context.DeadlineExceeded, tlsFailure), tlsStage: true, want: clientkit.FailureTimeout},
		{name: "network timeout before TLS", err: errors.Join(timeoutError{}, tlsFailure), tlsStage: true, want: clientkit.FailureTimeout},
		{name: "TLS stage before connection reset", err: syscall.ECONNRESET, tlsStage: true, want: clientkit.FailureTLS},
		{name: "typed TLS before DNS", err: errors.Join(tlsFailure, dnsFailure), want: clientkit.FailureTLS},
		{name: "DNS before refused", err: errors.Join(dnsFailure, syscall.ECONNREFUSED), want: clientkit.FailureNameResolution},
		{name: "refused before reset", err: errors.Join(syscall.ECONNREFUSED, syscall.ECONNRESET), want: clientkit.FailureConnectionRefused},
		{name: "reset before closed", err: errors.Join(syscall.ECONNRESET, io.EOF), want: clientkit.FailureConnectionReset},
	}

	// Classification order is observable policy because it controls the stable
	// failure class emitted to results and telemetry.
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := failure.Network(test.err, test.tlsStage); got != test.want {
				t.Fatalf("Network(%v, %t) = %q, want %q", test.err, test.tlsStage, got, test.want)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "operation timed out" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
