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

func TestDialRejectsLateCustomDialerResults(t *testing.T) {
	wantErr := errors.New("late dial failure")
	tests := []struct {
		name           string
		withConnection bool
		err            error
	}{
		{name: "connection and nil error", withConnection: true},
		{name: "connection and error", withConnection: true, err: wantErr},
		{name: "nil connection and error", err: wantErr},
		{name: "nil connection and nil error"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var connection *trackedConnection
			if test.withConnection {
				connection, _ = newTrackedPipe(t)
			}
			dialContext := make(chan context.Context, 1)
			release := make(chan struct{})
			observer := &tcpObserver{}
			client := newCustomTCPClient(t, func(ctx context.Context, _, _ string) (net.Conn, error) {
				dialContext <- ctx
				<-release
				if connection == nil {
					return nil, test.err
				}
				return connection, test.err
			}, func(config *tcpclient.Config) {
				config.DialTimeout = 10 * time.Millisecond
				config.Config.Observer = observer
			})

			results := make(chan tcpclient.Result, 1)
			go func() {
				results <- client.DialResult(context.Background())
			}()

			var ctx context.Context
			select {
			case ctx = <-dialContext:
			case <-time.After(time.Second):
				t.Fatal("custom dialer was not invoked")
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("custom dialer context did not expire")
			}
			close(release)

			var result tcpclient.Result
			select {
			case result = <-results:
			case <-time.After(time.Second):
				t.Fatal("DialResult did not return after releasing custom dialer")
			}
			if result.Conn != nil || !errors.Is(result.Err, context.DeadlineExceeded) {
				t.Fatalf("DialResult() = %#v, want deadline with no connection", result)
			}
			if result.Outcome != tcpclient.OutcomeTimeout || result.FailureClass != clientkit.FailureTimeout {
				t.Fatalf("classification = (%q, %q), want timeout", result.Outcome, result.FailureClass)
			}
			if connection != nil && !connection.closed.Load() {
				t.Fatal("late custom connection was not closed")
			}

			events := observer.snapshot()
			if len(events.attempts) != 1 || len(events.ends) != 1 {
				t.Fatalf("observer events = (%d attempts, %d ends), want one each", len(events.attempts), len(events.ends))
			}
			for _, event := range []struct {
				outcome      string
				succeeded    bool
				failureClass clientkit.FailureClass
				err          error
			}{
				{events.attempts[0].Outcome, events.attempts[0].Succeeded, events.attempts[0].FailureClass, events.attempts[0].Err},
				{events.ends[0].Outcome, events.ends[0].Succeeded, events.ends[0].FailureClass, events.ends[0].Err},
			} {
				if event.outcome != string(tcpclient.OutcomeTimeout) || event.succeeded || event.failureClass != clientkit.FailureTimeout || !errors.Is(event.err, context.DeadlineExceeded) {
					t.Fatalf("observer event = %#v, want timeout matching caller result", event)
				}
			}
		})
	}
}

func TestDialRejectsCustomSuccessAfterParentCancellation(t *testing.T) {
	connection, _ := newTrackedPipe(t)
	dialContext := make(chan context.Context, 1)
	release := make(chan struct{})
	client := newCustomTCPClient(t, func(ctx context.Context, _, _ string) (net.Conn, error) {
		dialContext <- ctx
		<-release
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.DisableDialTimeout = true
	})
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan tcpclient.Result, 1)
	go func() {
		results <- client.DialResult(ctx)
	}()

	var callbackContext context.Context
	select {
	case callbackContext = <-dialContext:
	case <-time.After(time.Second):
		t.Fatal("custom dialer was not invoked")
	}
	cancel()
	<-callbackContext.Done()
	close(release)

	select {
	case result := <-results:
		if result.Conn != nil || !errors.Is(result.Err, context.Canceled) || result.Outcome != tcpclient.OutcomeCanceled || result.FailureClass != clientkit.FailureCanceled {
			t.Fatalf("DialResult() = %#v, want canceled late success", result)
		}
	case <-time.After(time.Second):
		t.Fatal("DialResult did not return after releasing custom dialer")
	}
	if !connection.closed.Load() {
		t.Fatal("connection returned after parent cancellation was not closed")
	}
}

func TestCustomDialCompletionWinsBeforeLaterCancellation(t *testing.T) {
	connection, _ := newTrackedPipe(t)
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.DisableDialTimeout = true
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := client.DialResult(ctx)
	if result.Conn != connection || result.Err != nil || result.Outcome != tcpclient.OutcomeSuccess {
		t.Fatalf("DialResult() = %#v, want successful custom dial", result)
	}
	cancel()
	if connection.closed.Load() {
		t.Fatal("later parent cancellation closed caller-owned connection")
	}
	_ = result.Conn.Close()
}

func TestPreCanceledContextDoesNotInvokeCustomDialer(t *testing.T) {
	calls := 0
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, nil
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := client.DialResult(ctx)
	if calls != 0 || result.Conn != nil || !errors.Is(result.Err, context.Canceled) || result.Outcome != tcpclient.OutcomeCanceled || result.FailureClass != clientkit.FailureCanceled {
		t.Fatalf("DialResult() = %#v, calls=%d; want pre-dial cancellation", result, calls)
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
