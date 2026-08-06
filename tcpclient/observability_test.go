package tcpclient_test

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/tcpclient"
	"github.com/jaredjakacky/opskit"
)

func TestTCPNetworkTelemetryUsesBoundedVocabulary(t *testing.T) {
	tests := []struct {
		name    string
		network string
		want    string
	}{
		{name: "default", want: "tcp"},
		{name: "tcp", network: "tcp", want: "tcp"},
		{name: "tcp4", network: "tcp4", want: "tcp4"},
		{name: "tcp6", network: "tcp6", want: "tcp6"},
		{name: "custom", network: "tenant-network-42", want: "custom"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := &tcpAttributeObserver{}
			client, err := tcpclient.New(tcpclient.Config{
				Config: clientkit.Config{
					Name:     "network-telemetry",
					Observer: observer,
				},
				Network: test.network,
				Address: "dialer-owned-address",
				DialContext: func(context.Context, string, string) (net.Conn, error) {
					connection, peer := net.Pipe()
					_ = peer.Close()
					return connection, nil
				},
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			connection, err := client.Dial(context.Background())
			if err != nil {
				t.Fatalf("Dial() error = %v", err)
			}
			if err := connection.Close(); err != nil {
				t.Fatalf("connection.Close() error = %v", err)
			}

			if got := attributeValue(observer.attempt.Attributes, "clientkit.network"); got != test.want {
				t.Fatalf("clientkit.network = %q, want %q", got, test.want)
			}
			if test.want == "custom" && attributeContainsValue(observer.attempt.Attributes, test.network) {
				t.Fatalf("attempt attributes exposed custom network %q", test.network)
			}
		})
	}
}

func TestTCPObserverVocabulary(t *testing.T) {
	if tcpclient.ProtocolTCP != "tcp" {
		t.Fatalf("ProtocolTCP = %q, want tcp", tcpclient.ProtocolTCP)
	}
	if tcpclient.OperationDial != "dial" {
		t.Fatalf("OperationDial = %q, want dial", tcpclient.OperationDial)
	}
}

func TestTCPDialObserverLifecycle(t *testing.T) {
	observer := &tcpObserver{}
	connection, _ := newTrackedPipe(t)
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.Observer = observer
	})

	result := client.DialResult(context.Background())
	if result.Err != nil {
		t.Fatalf("DialResult() error = %v", result.Err)
	}
	_ = result.Conn.Close()
	events := observer.snapshot()
	if len(events.starts) != 1 || len(events.attempts) != 1 || len(events.ends) != 1 || len(events.health) != 0 {
		t.Fatalf("observer counts = (%d starts, %d attempts, %d ends, %d health), want 1,1,1,0", len(events.starts), len(events.attempts), len(events.ends), len(events.health))
	}
	start, attempt, end := events.starts[0], events.attempts[0], events.ends[0]
	if start.Client != "payments" || start.Protocol != tcpclient.ProtocolTCP || start.Operation != tcpclient.OperationDial || start.StartedAt.Location() != time.UTC {
		t.Fatalf("start event = %#v, want TCP dial identity", start)
	}
	if attempt.Number != 1 || !attempt.Succeeded || attempt.Outcome != string(tcpclient.OutcomeSuccess) || attempt.FailureClass != clientkit.FailureNone || attempt.Err != nil {
		t.Fatalf("attempt event = %#v, want one successful attempt", attempt)
	}
	if end.Attempts != 1 || !end.Succeeded || end.Outcome != string(tcpclient.OutcomeSuccess) || end.FailureClass != clientkit.FailureNone || end.Err != nil {
		t.Fatalf("end event = %#v, want successful operation", end)
	}
	if attempt.StartedAt.Location() != time.UTC || attempt.EndedAt.Location() != time.UTC || attempt.Duration < 0 || end.Duration < 0 {
		t.Fatalf("observer timing = (%#v, %#v), want UTC non-negative durations", attempt, end)
	}
	if got := attributeValue(start.Attributes, "client.security"); got != "custom" {
		t.Fatalf("start client.security = %q, want custom", got)
	}
}

func TestCustomDialerTLSFailureUsesBoundedTelemetry(t *testing.T) {
	wantErr := tls.RecordHeaderError{RecordHeader: [5]byte{'s', 'e', 'c', 'r', 't'}}
	observer := &tcpObserver{}
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return nil, wantErr
	}, func(config *tcpclient.Config) {
		config.Observer = observer
	})

	result := client.DialResult(context.Background())
	var recordError tls.RecordHeaderError
	if result.Outcome != tcpclient.OutcomeTLSError || result.FailureClass != clientkit.FailureTLS || !errors.As(result.Err, &recordError) {
		t.Fatalf("DialResult() = %#v, want original classified TLS failure", result)
	}
	events := observer.snapshot()
	if len(events.attempts) != 1 || len(events.ends) != 1 {
		t.Fatalf("observer events = (%d attempts, %d ends), want one each", len(events.attempts), len(events.ends))
	}
	for _, err := range []error{events.attempts[0].Err, events.ends[0].Err} {
		if err == nil || err.Error() != "clientkit: TLS handshake failed" {
			t.Fatalf("observer error = %v, want stable TLS failure", err)
		}
	}
	if got := attributeValue(events.starts[0].Attributes, "client.security"); got != "custom" {
		t.Fatalf("client.security = %q, want custom", got)
	}
}

func TestTCPDialFailureObserverLifecycle(t *testing.T) {
	wantErr := errors.New("dial failed")
	observer := &tcpObserver{}
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return nil, wantErr
	}, func(config *tcpclient.Config) {
		config.Observer = observer
	})

	result := client.DialResult(context.Background())
	if !errors.Is(result.Err, wantErr) || result.Outcome != tcpclient.OutcomeDialError || result.FailureClass != clientkit.FailureTransport {
		t.Fatalf("DialResult() = %#v, want original transport failure", result)
	}
	events := observer.snapshot()
	if !slices.Equal(events.order, []string{"start", "attempt", "end"}) {
		t.Fatalf("observer order = %v, want start, attempt, end", events.order)
	}
	if len(events.attempts) != 1 || len(events.ends) != 1 {
		t.Fatalf("observer counts = (%d attempts, %d ends), want one each", len(events.attempts), len(events.ends))
	}
	attempt, end := events.attempts[0], events.ends[0]
	if attempt.Number != 1 || attempt.Succeeded || attempt.Outcome != string(tcpclient.OutcomeDialError) || attempt.FailureClass != clientkit.FailureTransport || !errors.Is(attempt.Err, wantErr) {
		t.Fatalf("attempt event = %#v, want original transport failure", attempt)
	}
	if end.Attempts != 1 || end.Succeeded || end.Outcome != string(tcpclient.OutcomeDialError) || end.FailureClass != clientkit.FailureTransport || !errors.Is(end.Err, wantErr) {
		t.Fatalf("end event = %#v, want original transport failure", end)
	}
}

func TestTCPObserverPanicDoesNotAffectDial(t *testing.T) {
	connection, _ := newTrackedPipe(t)
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.Observer = panickingTCPObserver{NopObserver: clientkit.NopObserver{}}
	})

	result := client.DialResult(context.Background())
	if result.Err != nil || result.Outcome != tcpclient.OutcomeSuccess {
		t.Fatalf("DialResult() = %#v, want success despite observer panic", result)
	}
	_ = result.Conn.Close()
}

func TestTCPOperationEndPanicDoesNotAffectDial(t *testing.T) {
	connection, _ := newTrackedPipe(t)
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.Observer = endPanickingTCPObserver{NopObserver: clientkit.NopObserver{}}
	})

	result := client.DialResult(context.Background())
	if result.Err != nil || result.Outcome != tcpclient.OutcomeSuccess {
		t.Fatalf("DialResult() = %#v, want success despite operation-end panic", result)
	}
	_ = result.Conn.Close()
}

type tcpAttributeObserver struct {
	attempt clientkit.AttemptEvent
}

type panickingTCPObserver struct {
	clientkit.NopObserver
}

type endPanickingTCPObserver struct {
	clientkit.NopObserver
}

func (panickingTCPObserver) StartOperation(context.Context, clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	panic("start")
}

func (panickingTCPObserver) ObserveAttempt(context.Context, clientkit.AttemptEvent) {
	panic("attempt")
}

func (endPanickingTCPObserver) StartOperation(ctx context.Context, _ clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	return ctx, clientkit.OperationObservationFunc(func(context.Context, clientkit.OperationEndEvent) {
		panic("end")
	})
}

func (*tcpAttributeObserver) StartOperation(ctx context.Context, _ clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	return ctx, clientkit.NopOperationObservation{}
}

func (o *tcpAttributeObserver) ObserveAttempt(_ context.Context, event clientkit.AttemptEvent) {
	o.attempt = event
}

func (*tcpAttributeObserver) ObserveRetry(context.Context, clientkit.RetryEvent)   {}
func (*tcpAttributeObserver) ObserveHealth(context.Context, clientkit.HealthEvent) {}

func attributeValue(attributes []opskit.Attribute, key string) string {
	for _, attribute := range attributes {
		if attribute.Key == key {
			return attribute.Value
		}
	}
	return ""
}

func attributeContainsValue(attributes []opskit.Attribute, value string) bool {
	for _, attribute := range attributes {
		if attribute.Value == value {
			return true
		}
	}
	return false
}
