package tcpclient_test

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/tcpclient"
)

func TestTCPCheckDisabledAndNilClient(t *testing.T) {
	calls := 0
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, nil
	}, nil)
	if client.HealthCheckEnabled() {
		t.Fatal("HealthCheckEnabled() = true with disabled check")
	}
	cached := client.Client.UpdateHealth(clientkit.Health{
		State:     clientkit.HealthHealthy,
		CheckedAt: time.Now().UTC(),
		Message:   "cached",
	})
	health := client.Check(context.Background())
	if health.State != clientkit.HealthUnknown || health.Message != "TCP health check is disabled" || calls != 0 {
		t.Fatalf("Check() = %#v, calls=%d; want disabled result without dial", health, calls)
	}
	if got := client.Health(); got != cached {
		t.Fatalf("Health() = %#v after disabled Check, want cached %#v", got, cached)
	}

	var nilClient *tcpclient.Client
	health = nilClient.Check(context.Background())
	if health.State != clientkit.HealthUnhealthy || health.FailureClass != clientkit.FailureConfiguration || health.Message != "TCP client is not configured" {
		t.Fatalf("nil Check() = %#v, want configuration failure", health)
	}
}

func TestTCPCheckSuccessClosesAndCachesConnection(t *testing.T) {
	connection, _ := newTrackedPipe(t)
	observer := &tcpObserver{}
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.Observer = observer
		config.Check.Enabled = true
	})
	if !client.HealthCheckEnabled() {
		t.Fatal("HealthCheckEnabled() = false")
	}

	health := client.Check(context.Background())
	if health.State != clientkit.HealthHealthy || health.FailureClass != clientkit.FailureNone || health.Message != "TCP connection established" {
		t.Fatalf("Check() = %#v, want healthy connection", health)
	}
	if health.CheckedAt.IsZero() || health.CheckedAt.Location() != time.UTC || health.Duration < 0 {
		t.Fatalf("health timing = (%v, %v), want completed UTC record", health.CheckedAt, health.Duration)
	}
	if !connection.closed.Load() {
		t.Fatal("health check did not close established connection")
	}
	if cached := client.Health(); cached != health {
		t.Fatalf("Health() = %#v, want cached %#v", cached, health)
	}

	events := observer.snapshot()
	if len(events.attempts) != 1 || len(events.ends) != 1 || len(events.health) != 1 {
		t.Fatalf("observer counts = (%d attempts, %d ends, %d health), want one each", len(events.attempts), len(events.ends), len(events.health))
	}
	event := events.health[0]
	if event.Client != "payments" || event.Protocol != tcpclient.ProtocolTCP || event.State != health.State || event.CheckedAt != health.CheckedAt {
		t.Fatalf("health event = %#v, want completed health", event)
	}
	if got := attributeValue(event.Attributes, "client.operation"); got != "health_check" {
		t.Fatalf("client.operation = %q, want health_check", got)
	}
	if got := attributeValue(event.Attributes, "client.security"); got != "custom" {
		t.Fatalf("client.security = %q, want custom", got)
	}
}

func TestTCPCheckConnectionFailures(t *testing.T) {
	tests := []struct {
		name        string
		dial        tcpclient.DialContextFunc
		mutate      func(*tcpclient.Config)
		cancel      bool
		wantFailure clientkit.FailureClass
		wantMessage string
	}{
		{
			name: "timeout",
			dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
			mutate:      func(config *tcpclient.Config) { config.Check.Timeout = 10 * time.Millisecond },
			wantFailure: clientkit.FailureTimeout,
			wantMessage: "TCP connection timed out",
		},
		{
			name: "canceled",
			dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return nil, ctx.Err()
			},
			cancel:      true,
			wantFailure: clientkit.FailureCanceled,
			wantMessage: "TCP connection canceled",
		},
		{
			name: "no connection",
			dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, nil
			},
			wantFailure: clientkit.FailureTransport,
			wantMessage: "TCP dial returned no connection",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newCustomTCPClient(t, test.dial, func(config *tcpclient.Config) {
				config.Check.Enabled = true
				if test.mutate != nil {
					test.mutate(config)
				}
			})
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			health := client.Check(ctx)
			if health.State != clientkit.HealthUnhealthy || health.FailureClass != test.wantFailure || health.Message != test.wantMessage {
				t.Fatalf("Check() = %#v, want %q %q", health, test.wantFailure, test.wantMessage)
			}
		})
	}
}

func TestTCPCheckProbeAssessments(t *testing.T) {
	tests := []struct {
		name        string
		probe       tcpclient.ConnectionProbe
		wantState   clientkit.HealthState
		wantFailure clientkit.FailureClass
		wantMessage string
	}{
		{
			name: "healthy clears failure",
			probe: tcpclient.ConnectionProbeFunc(func(context.Context, net.Conn) clientkit.HealthAssessment {
				return clientkit.HealthAssessment{State: clientkit.HealthHealthy, FailureClass: clientkit.FailureTransport, Message: "ready"}
			}),
			wantState: clientkit.HealthHealthy, wantMessage: "ready",
		},
		{
			name: "degraded receives remote response failure",
			probe: tcpclient.ConnectionProbeFunc(func(context.Context, net.Conn) clientkit.HealthAssessment {
				return clientkit.HealthAssessment{State: clientkit.HealthDegraded, Message: "fallback"}
			}),
			wantState: clientkit.HealthDegraded, wantFailure: clientkit.FailureRemoteResponse, wantMessage: "fallback",
		},
		{
			name: "unhealthy preserves failure",
			probe: tcpclient.ConnectionProbeFunc(func(context.Context, net.Conn) clientkit.HealthAssessment {
				return clientkit.HealthAssessment{State: clientkit.HealthUnhealthy, FailureClass: clientkit.FailurePolicy, Message: "rejected"}
			}),
			wantState: clientkit.HealthUnhealthy, wantFailure: clientkit.FailurePolicy, wantMessage: "rejected",
		},
		{
			name: "empty message uses stable default",
			probe: tcpclient.ConnectionProbeFunc(func(context.Context, net.Conn) clientkit.HealthAssessment {
				return clientkit.HealthAssessment{State: clientkit.HealthHealthy}
			}),
			wantState: clientkit.HealthHealthy, wantMessage: "TCP health probe completed",
		},
		{
			name: "invalid state",
			probe: tcpclient.ConnectionProbeFunc(func(context.Context, net.Conn) clientkit.HealthAssessment {
				return clientkit.HealthAssessment{State: clientkit.HealthUnknown, Message: "raw"}
			}),
			wantState: clientkit.HealthUnhealthy, wantFailure: clientkit.FailurePolicy, wantMessage: "TCP health probe returned an invalid state",
		},
		{
			name: "panic",
			probe: tcpclient.ConnectionProbeFunc(func(context.Context, net.Conn) clientkit.HealthAssessment {
				panic("probe")
			}),
			wantState: clientkit.HealthUnhealthy, wantFailure: clientkit.FailurePolicy, wantMessage: "TCP health probe panicked",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection, _ := newTrackedPipe(t)
			client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
				return connection, nil
			}, func(config *tcpclient.Config) {
				config.Check = tcpclient.CheckConfig{Enabled: true, Probe: test.probe}
			})
			health := client.Check(context.Background())
			if health.State != test.wantState || health.FailureClass != test.wantFailure || health.Message != test.wantMessage {
				t.Fatalf("Check() = %#v, want (%q, %q, %q)", health, test.wantState, test.wantFailure, test.wantMessage)
			}
			if !connection.closed.Load() {
				t.Fatal("health check did not close probed connection")
			}
		})
	}
}

func TestTCPCheckProbeReceivesOwnedConnectionAndDeadline(t *testing.T) {
	connection, _ := newTrackedPipe(t)
	probeCalled := false
	probe := tcpclient.ConnectionProbeFunc(func(ctx context.Context, got net.Conn) clientkit.HealthAssessment {
		probeCalled = true
		if got != connection {
			t.Errorf("probe connection = %v, want established connection", got)
		}
		if connection.closed.Load() {
			t.Error("connection was closed before probe completed")
		}
		if deadline, ok := ctx.Deadline(); !ok || deadline.IsZero() {
			t.Error("probe context has no check deadline")
		}
		return clientkit.HealthAssessment{State: clientkit.HealthHealthy}
	})
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.Check = tcpclient.CheckConfig{Enabled: true, Timeout: time.Second, Probe: probe}
	})

	health := client.Check(context.Background())
	if !probeCalled || health.State != clientkit.HealthHealthy {
		t.Fatalf("Check() = %#v, probeCalled=%t; want successful probe", health, probeCalled)
	}
	if connection.Deadline().IsZero() {
		t.Fatal("probe connection deadline was not set from check context")
	}
	if !connection.closed.Load() {
		t.Fatal("connection was not closed after probe")
	}
}

func TestTCPCheckTimeoutClosesConnectionToUnblockProbe(t *testing.T) {
	tracked, _ := newTrackedPipe(t)
	connection := &deadlineIgnoringConnection{trackedConnection: tracked}
	probe := tcpclient.ConnectionProbeFunc(func(ctx context.Context, connection net.Conn) clientkit.HealthAssessment {
		// This probe deliberately ignores ctx while blocked in network I/O. The
		// client must close the connection when ctx expires to release it.
		_, _ = connection.Read(make([]byte, 1))
		<-ctx.Done()
		return clientkit.HealthAssessment{State: clientkit.HealthUnhealthy, FailureClass: clientkit.FailureTimeout, Message: "timed out"}
	})
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.Check = tcpclient.CheckConfig{Enabled: true, Timeout: 10 * time.Millisecond, Probe: probe}
	})

	result := make(chan clientkit.Health, 1)
	go func() {
		result <- client.Check(context.Background())
	}()
	var health clientkit.Health
	select {
	case health = <-result:
	case <-time.After(time.Second):
		t.Fatal("Check() did not unblock the probe after its timeout")
	}
	if health.State != clientkit.HealthUnhealthy || health.FailureClass != clientkit.FailureTimeout {
		t.Fatalf("Check() = %#v, want timed-out probe assessment", health)
	}
	if !tracked.closed.Load() {
		t.Fatal("check timeout did not close blocked probe connection")
	}
}

func TestTCPCheckCanDisableTimeout(t *testing.T) {
	connection, _ := newTrackedPipe(t)
	probe := tcpclient.ConnectionProbeFunc(func(ctx context.Context, _ net.Conn) clientkit.HealthAssessment {
		if _, ok := ctx.Deadline(); ok {
			t.Error("probe context has a deadline with check timeout disabled")
		}
		return clientkit.HealthAssessment{State: clientkit.HealthHealthy}
	})
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.Check = tcpclient.CheckConfig{Enabled: true, DisableTimeout: true, Probe: probe}
	})

	if health := client.Check(context.Background()); health.State != clientkit.HealthHealthy {
		t.Fatalf("Check() = %#v, want healthy result without check timeout", health)
	}
}

func TestTCPHealthProjectsStalenessWithoutMutatingCache(t *testing.T) {
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return nil, context.Canceled
	}, func(config *tcpclient.Config) {
		config.Check = tcpclient.CheckConfig{Enabled: true, StaleAfter: time.Second}
	})

	tests := []struct {
		name       string
		checkedAt  time.Time
		wantPhrase string
	}{
		{name: "missing timestamp", wantPhrase: "no timestamp"},
		{name: "future timestamp", checkedAt: time.Now().UTC().Add(time.Hour), wantPhrase: "future"},
		{name: "stale timestamp", checkedAt: time.Now().UTC().Add(-time.Hour), wantPhrase: "stale"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := client.Client.UpdateHealth(clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: test.checkedAt, Message: "raw"})
			projected := client.Health()
			if projected.State != clientkit.HealthUnknown || !strings.Contains(projected.Message, test.wantPhrase) {
				t.Fatalf("Health() = %#v, want unknown %q projection", projected, test.wantPhrase)
			}
			if cached := client.Client.Health(); cached != raw {
				t.Fatalf("cached Health() = %#v, want unchanged %#v", cached, raw)
			}
		})
	}

	fresh := client.Client.UpdateHealth(clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC(), Message: "fresh"})
	if got := client.Snapshot().Health; got != fresh {
		t.Fatalf("Snapshot().Health = %#v, want fresh %#v", got, fresh)
	}
}

func TestTCPHealthCanDisableStalenessProjection(t *testing.T) {
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return nil, context.Canceled
	}, func(config *tcpclient.Config) {
		config.Check = tcpclient.CheckConfig{Enabled: true, DisableStaleAfter: true}
	})
	cached := client.Client.UpdateHealth(clientkit.Health{
		State:     clientkit.HealthHealthy,
		CheckedAt: time.Now().UTC().Add(-24 * time.Hour),
		Message:   "old but authoritative",
	})

	if got := client.Health(); got != cached {
		t.Fatalf("Health() = %#v, want unprojected cached health %#v", got, cached)
	}
}

func TestTCPCheckTLSHealthMessage(t *testing.T) {
	certificate, roots := testCertificate(t, "example.test")
	serverResults := make(chan error, 1)
	client := newCustomTCPClient(t, tlsPipeDialer(t, &tls.Config{Certificates: []tls.Certificate{certificate}}, serverResults), func(config *tcpclient.Config) {
		config.Check.Enabled = true
		config.TLS = tcpclient.TLSConfig{Enabled: true, Config: &tls.Config{RootCAs: roots}}
	})

	health := client.Check(context.Background())
	if health.State != clientkit.HealthHealthy || health.Message != "TLS connection established" {
		t.Fatalf("Check() = %#v, want healthy TLS connection", health)
	}
	if err := <-serverResults; err != nil {
		t.Fatalf("server handshake error = %v", err)
	}
}

func TestTCPCheckTLSFailureMessages(t *testing.T) {
	tests := []struct {
		name        string
		newContext  func() (context.Context, context.CancelFunc)
		dial        func(*testing.T) tcpclient.DialContextFunc
		mutate      func(*tcpclient.Config)
		wantFailure clientkit.FailureClass
		wantMessage string
	}{
		{
			name: "handshake failure",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			dial: func(t *testing.T) tcpclient.DialContextFunc {
				connection, peer := newTrackedPipe(t)
				go func() {
					// Consume the ClientHello before returning an invalid record;
					// simultaneous writes would block forever on net.Pipe.
					buffer := make([]byte, 4096)
					_, _ = peer.Read(buffer)
					_, _ = peer.Write([]byte("abcde"))
				}()
				return func(context.Context, string, string) (net.Conn, error) { return connection, nil }
			},
			wantFailure: clientkit.FailureTLS,
			wantMessage: "TLS handshake failed",
		},
		{
			name: "handshake timeout",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			dial: func(t *testing.T) tcpclient.DialContextFunc {
				connection, _ := newTrackedPipe(t)
				return func(context.Context, string, string) (net.Conn, error) { return connection, nil }
			},
			mutate:      func(config *tcpclient.Config) { config.Check.Timeout = 10 * time.Millisecond },
			wantFailure: clientkit.FailureTimeout,
			wantMessage: "TLS handshake timed out",
		},
		{
			name: "handshake cancellation",
			newContext: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			dial: func(t *testing.T) tcpclient.DialContextFunc {
				connection, _ := newTrackedPipe(t)
				return func(context.Context, string, string) (net.Conn, error) { return connection, nil }
			},
			wantFailure: clientkit.FailureCanceled,
			wantMessage: "TLS handshake canceled",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newCustomTCPClient(t, test.dial(t), func(config *tcpclient.Config) {
				config.Check.Enabled = true
				config.TLS = tcpclient.TLSConfig{Enabled: true, Config: &tls.Config{InsecureSkipVerify: true}}
				if test.mutate != nil {
					test.mutate(config)
				}
			})
			ctx, cancel := test.newContext()
			defer cancel()

			health := client.Check(ctx)
			if health.State != clientkit.HealthUnhealthy || health.FailureClass != test.wantFailure || health.Message != test.wantMessage {
				t.Fatalf("Check() = %#v, want (%q, %q)", health, test.wantFailure, test.wantMessage)
			}
		})
	}
}
