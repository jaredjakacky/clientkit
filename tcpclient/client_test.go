package tcpclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/tcpclient"
)

func TestTCPClientConstructionAndNormalization(t *testing.T) {
	var gotNetwork, gotAddress string
	var gotDeadline time.Time
	connection, _ := newTrackedPipe(t)
	client := newCustomTCPClient(t, func(ctx context.Context, network, address string) (net.Conn, error) {
		gotNetwork = network
		gotAddress = address
		gotDeadline, _ = ctx.Deadline()
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.Network = " TCP4 "
		config.Address = " dialer-address "
		config.Config.ReadinessPolicy = clientkit.ReadinessInformational
	})

	if client.Name() != "payments" || client.Protocol() != tcpclient.ProtocolTCP || client.ReadinessPolicy() != clientkit.ReadinessInformational {
		t.Fatalf("client identity = (%q, %q, %q), want configured values", client.Name(), client.Protocol(), client.ReadinessPolicy())
	}
	if health := client.Health(); health.State != clientkit.HealthUnknown {
		t.Fatalf("initial Health() = %#v, want unknown", health)
	}
	beforeDial := time.Now()
	result := client.DialResult(context.Background())
	afterDial := time.Now()
	if result.Err != nil {
		t.Fatalf("DialResult() error = %v", result.Err)
	}
	if gotNetwork != "tcp4" || gotAddress != "dialer-address" {
		t.Fatalf("dial target = (%q, %q), want normalized values", gotNetwork, gotAddress)
	}
	earliestDeadline := beforeDial.Add(tcpclient.DefaultDialTimeout)
	latestDeadline := afterDial.Add(tcpclient.DefaultDialTimeout)
	if gotDeadline.Before(earliestDeadline) || gotDeadline.After(latestDeadline) {
		t.Fatalf("dial deadline = %v, want between %v and %v", gotDeadline, earliestDeadline, latestDeadline)
	}
	_ = result.Conn.Close()
}

func TestTCPClientCanDisableDialTimeout(t *testing.T) {
	hadDeadline := true
	connection, _ := newTrackedPipe(t)
	client := newCustomTCPClient(t, func(ctx context.Context, _, _ string) (net.Conn, error) {
		_, hadDeadline = ctx.Deadline()
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.DisableDialTimeout = true
	})

	result := client.DialResult(context.Background())
	if result.Err != nil {
		t.Fatalf("DialResult() error = %v", result.Err)
	}
	if hadDeadline {
		t.Fatal("custom dialer context has a deadline with DialTimeout disabled")
	}
	_ = result.Conn.Close()
}

func TestTCPClientSnapshotAndNilReceiver(t *testing.T) {
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return nil, context.Canceled
	}, nil)
	snapshot := client.Snapshot()
	if snapshot.Name != "payments" || snapshot.Protocol != tcpclient.ProtocolTCP || snapshot.ReadinessPolicy != clientkit.ReadinessOptional || snapshot.Health.State != clientkit.HealthUnknown {
		t.Fatalf("Snapshot() = %#v, want configured identity and unknown health", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal(Snapshot()) error = %v", err)
	}
	if strings.Contains(string(encoded), "example.test") || strings.Contains(string(encoded), "443") {
		t.Fatalf("Snapshot JSON = %s, want no connection configuration", encoded)
	}

	var nilClient *tcpclient.Client
	if nilClient.Name() != "" || nilClient.Protocol() != "" || nilClient.ReadinessPolicy() != clientkit.ReadinessOptional {
		t.Fatalf("nil client identity = (%q, %q, %q), want empty name and protocol with optional policy", nilClient.Name(), nilClient.Protocol(), nilClient.ReadinessPolicy())
	}
	if health := nilClient.Health(); health.State != clientkit.HealthUnknown || health.Message == "" {
		t.Fatalf("nil Health() = %#v, want stable unknown health", health)
	}
	if snapshot := nilClient.Snapshot(); snapshot.Name != "" || snapshot.Protocol != "" || snapshot.ReadinessPolicy != clientkit.ReadinessOptional || snapshot.Health.State != clientkit.HealthUnknown {
		t.Fatalf("nil Snapshot() = %#v, want empty identity and unknown health", snapshot)
	}
	if nilClient.HealthCheckEnabled() {
		t.Fatal("nil HealthCheckEnabled() = true")
	}
}

func TestTCPClientProtocolIsStableAcrossConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tcpclient.Config)
	}{
		{
			name: "built-in tcp4",
			mutate: func(config *tcpclient.Config) {
				config.Network = "tcp4"
			},
		},
		{
			name: "custom dialer and tcp6",
			mutate: func(config *tcpclient.Config) {
				config.Network = "tcp6"
				config.DialContext = func(context.Context, string, string) (net.Conn, error) {
					return nil, context.Canceled
				}
			},
		},
		{
			name: "TLS",
			mutate: func(config *tcpclient.Config) {
				config.TLS.Enabled = true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := baseTCPConfig()
			test.mutate(&config)
			client, err := tcpclient.New(config)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if got := client.Protocol(); got != tcpclient.ProtocolTCP {
				t.Fatalf("Protocol() = %q, want %q", got, tcpclient.ProtocolTCP)
			}
		})
	}
}

func TestTCPClientSupportsConcurrentOperations(t *testing.T) {
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		connection, peer := net.Pipe()
		_ = peer.Close()
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.Check.Enabled = true
	})

	const workers = 16
	var wait sync.WaitGroup
	errorsFound := make(chan error, workers)
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result := client.DialResult(context.Background())
			if result.Err != nil || result.Outcome != tcpclient.OutcomeSuccess {
				errorsFound <- fmt.Errorf("worker %d DialResult() = %#v", worker, result)
				return
			}
			_ = result.Conn.Close()
			if health := client.Check(context.Background()); health.State != clientkit.HealthHealthy {
				errorsFound <- fmt.Errorf("worker %d Check() = %#v", worker, health)
				return
			}
			if health := client.Health(); health.State != clientkit.HealthHealthy {
				errorsFound <- fmt.Errorf("worker %d Health() = %#v", worker, health)
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}
