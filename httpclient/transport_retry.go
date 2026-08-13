package httpclient

import (
	"errors"
	"fmt"
	"net"

	"github.com/jaredjakacky/clientkit"
)

// TransportRetryMode controls whether non-timeout HTTP transport failures may
// be retried. Every mode remains subject to the configured method, attempt
// limit, RetrySafety, request-body replayability, and operation context.
// RetryTimeouts independently controls recognized timeouts.
type TransportRetryMode string

const (
	// TransportRetryNone disables retries for non-timeout transport failures.
	// It is the zero value so an explicit RetryConfig does not opt in silently.
	TransportRetryNone TransportRetryMode = ""
	// TransportRetryDefault retries transient-looking and unclassified transport
	// failures, but not recognized TLS failures, DNS not-found responses, or an
	// invalid no-response/no-error result from a RoundTripper.
	TransportRetryDefault TransportRetryMode = "default"
	// TransportRetryAll retries every non-timeout transport execution failure.
	// It is the escape hatch for callers that intentionally want broader behavior.
	TransportRetryAll TransportRetryMode = "all"
)

func validateTransportRetryMode(mode TransportRetryMode) error {
	switch mode {
	case TransportRetryNone, TransportRetryDefault, TransportRetryAll:
		return nil
	default:
		return fmt.Errorf("clientkit: invalid HTTP transport retry mode %q", mode)
	}
}

func (mode TransportRetryMode) allows(failureClass clientkit.FailureClass, err error) bool {
	switch mode {
	case TransportRetryNone:
		return false
	case TransportRetryAll:
		return true
	case TransportRetryDefault:
	default:
		return false
	}

	if errors.Is(err, errHTTPTransportNoResponse) {
		return false
	}

	switch failureClass {
	case clientkit.FailureTLS:
		return false
	case clientkit.FailureNameResolution:
		var dnsError *net.DNSError
		return !errors.As(err, &dnsError) || !dnsError.IsNotFound
	case clientkit.FailureConnectionRefused,
		clientkit.FailureConnectionReset,
		clientkit.FailureConnectionClosed,
		clientkit.FailureTransport:
		return true
	default:
		return false
	}
}
