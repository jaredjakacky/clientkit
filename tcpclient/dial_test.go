package tcpclient_test

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/tcpclient"
)

func TestDialResultSuccessAndCallerOwnership(t *testing.T) {
	connection, _ := newTrackedPipe(t)
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, nil)

	result := client.DialResult(context.Background())
	if result.Conn != connection || result.Err != nil {
		t.Fatalf("DialResult() = %#v, want caller-owned connection", result)
	}
	if result.Outcome != tcpclient.OutcomeSuccess || result.FailureClass != clientkit.FailureNone {
		t.Fatalf("DialResult classification = (%q, %q), want success", result.Outcome, result.FailureClass)
	}
	if result.StartedAt.IsZero() || result.StartedAt.Location() != time.UTC || result.Duration < 0 {
		t.Fatalf("DialResult timing = (%v, %v), want UTC start and non-negative duration", result.StartedAt, result.Duration)
	}
	if connection.closed.Load() {
		t.Fatal("successful connection was closed before ownership reached caller")
	}
	if err := result.Conn.Close(); err != nil {
		t.Fatalf("connection.Close() error = %v", err)
	}
	if !connection.closed.Load() {
		t.Fatal("caller close did not close returned connection")
	}
}

func TestDialContextEnforcesConfiguredEndpoint(t *testing.T) {
	calls := 0
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		calls++
		connection, _ := newTrackedPipe(t)
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.Network = "tcp4"
		config.Address = "example.test:8443"
	})

	connection, err := client.DialContext(context.Background(), " TCP4 ", " example.test:8443 ")
	if err != nil {
		t.Fatalf("DialContext(configured) error = %v", err)
	}
	_ = connection.Close()
	for _, target := range []struct {
		network string
		address string
	}{
		{network: "tcp", address: "example.test:8443"},
		{network: "tcp4", address: "other.test:8443"},
	} {
		if connection, err := client.DialContext(context.Background(), target.network, target.address); err == nil || connection != nil {
			t.Fatalf("DialContext(%q, %q) = (%v, %v), want target rejection", target.network, target.address, connection, err)
		}
	}
	if calls != 1 {
		t.Fatalf("custom dialer calls = %d, want 1 accepted endpoint", calls)
	}

	var nilClient *tcpclient.Client
	if connection, err := nilClient.DialContext(context.Background(), "tcp", "example.test:443"); err == nil || connection != nil {
		t.Fatalf("nil DialContext() = (%v, %v), want configuration error", connection, err)
	}
}

func TestDialClassifiesConnectionFailures(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantOutcome tcpclient.Outcome
		wantFailure clientkit.FailureClass
	}{
		{name: "canceled", err: context.Canceled, wantOutcome: tcpclient.OutcomeCanceled, wantFailure: clientkit.FailureCanceled},
		{name: "deadline", err: context.DeadlineExceeded, wantOutcome: tcpclient.OutcomeTimeout, wantFailure: clientkit.FailureTimeout},
		{name: "network timeout", err: timeoutError{}, wantOutcome: tcpclient.OutcomeTimeout, wantFailure: clientkit.FailureTimeout},
		{name: "DNS", err: &net.DNSError{Err: "not found", Name: "example.test"}, wantOutcome: tcpclient.OutcomeDialError, wantFailure: clientkit.FailureNameResolution},
		{name: "refused", err: syscall.ECONNREFUSED, wantOutcome: tcpclient.OutcomeDialError, wantFailure: clientkit.FailureConnectionRefused},
		{name: "reset", err: syscall.ECONNRESET, wantOutcome: tcpclient.OutcomeDialError, wantFailure: clientkit.FailureConnectionReset},
		{name: "closed", err: io.EOF, wantOutcome: tcpclient.OutcomeDialError, wantFailure: clientkit.FailureConnectionClosed},
		{name: "transport", err: errors.New("dial failed"), wantOutcome: tcpclient.OutcomeDialError, wantFailure: clientkit.FailureTransport},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
				return nil, test.err
			}, nil)
			result := client.DialResult(context.Background())
			if result.Conn != nil || !errors.Is(result.Err, test.err) {
				t.Fatalf("DialResult() = %#v, want original error", result)
			}
			if result.Outcome != test.wantOutcome || result.FailureClass != test.wantFailure {
				t.Fatalf("classification = (%q, %q), want (%q, %q)", result.Outcome, result.FailureClass, test.wantOutcome, test.wantFailure)
			}
		})
	}
}

func TestDialHandlesInvalidCustomDialerResults(t *testing.T) {
	t.Run("connection and error closes connection", func(t *testing.T) {
		connection, _ := newTrackedPipe(t)
		wantErr := errors.New("partial dial")
		client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
			return connection, wantErr
		}, nil)
		result := client.DialResult(context.Background())
		if result.Conn != nil || !errors.Is(result.Err, wantErr) || !connection.closed.Load() {
			t.Fatalf("DialResult() = %#v, closed=%t; want closed partial connection", result, connection.closed.Load())
		}
	})

	t.Run("nil connection and nil error becomes transport failure", func(t *testing.T) {
		client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
			return nil, nil
		}, nil)
		result := client.DialResult(context.Background())
		if result.Conn != nil || result.Err == nil || result.Outcome != tcpclient.OutcomeDialError || result.FailureClass != clientkit.FailureTransport {
			t.Fatalf("DialResult() = %#v, want transport failure", result)
		}
	})
}

func TestDialTimeoutBoundsCustomDialer(t *testing.T) {
	client := newCustomTCPClient(t, func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, func(config *tcpclient.Config) {
		config.DialTimeout = 10 * time.Millisecond
	})

	result := client.DialResult(context.Background())
	if result.Outcome != tcpclient.OutcomeTimeout || result.FailureClass != clientkit.FailureTimeout || !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("DialResult() = %#v, want timeout", result)
	}
}

func TestBuiltInDialerHonorsCanceledContext(t *testing.T) {
	client, err := tcpclient.New(baseTCPConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// A pre-canceled context exercises the real net.Dialer path without making
	// this unit test depend on DNS, an available port, or host networking.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := client.DialResult(ctx)
	if result.Conn != nil || result.Outcome != tcpclient.OutcomeCanceled || result.FailureClass != clientkit.FailureCanceled || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("DialResult() = %#v, want canceled built-in dial", result)
	}
}

func TestNilTCPClientDialFailsSafely(t *testing.T) {
	var client *tcpclient.Client
	result := client.DialResult(context.Background())
	if result.Conn != nil || result.Err == nil || result.Outcome != tcpclient.OutcomeDialError || result.FailureClass != clientkit.FailureConfiguration {
		t.Fatalf("nil DialResult() = %#v, want configuration failure", result)
	}
	connection, err := client.Dial(context.Background())
	if connection != nil || err == nil {
		t.Fatalf("nil Dial() = (%v, %v), want configuration error", connection, err)
	}
}
