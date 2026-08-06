package httpclient_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPTransportFailureClassification(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		outcome httpclient.Outcome
		failure clientkit.FailureClass
	}{
		{name: "canceled", err: context.Canceled, outcome: httpclient.OutcomeCanceled, failure: clientkit.FailureCanceled},
		{name: "deadline", err: context.DeadlineExceeded, outcome: httpclient.OutcomeTimeout, failure: clientkit.FailureTimeout},
		{name: "DNS", err: &net.DNSError{Err: "missing", Name: "example.test"}, outcome: httpclient.OutcomeTransportError, failure: clientkit.FailureNameResolution},
		{name: "refused", err: syscall.ECONNREFUSED, outcome: httpclient.OutcomeTransportError, failure: clientkit.FailureConnectionRefused},
		{name: "reset", err: syscall.ECONNRESET, outcome: httpclient.OutcomeTransportError, failure: clientkit.FailureConnectionReset},
		{name: "closed", err: io.EOF, outcome: httpclient.OutcomeTransportError, failure: clientkit.FailureConnectionClosed},
		{name: "TLS", err: tls.RecordHeaderError{Msg: "invalid record"}, outcome: httpclient.OutcomeTransportError, failure: clientkit.FailureTLS},
		{name: "other", err: errors.New("transport"), outcome: httpclient.OutcomeTransportError, failure: clientkit.FailureTransport},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newHTTPTestClient(t, func(*http.Request) (*http.Response, error) { return nil, test.err }, httpclient.Config{Retry: httpclient.NoRetryConfig()})
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
			result := client.Execute(request)
			if result.Outcome != test.outcome || result.FailureClass != test.failure || result.Err == nil {
				t.Fatalf("Execute() = %#v, want outcome %q failure %q", result, test.outcome, test.failure)
			}
		})
	}
}
