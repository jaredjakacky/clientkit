package tcpclient_test

import (
	"context"
	"fmt"
	"net"
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
		config.Network = " CUSTOM-NETWORK "
		config.Address = " dialer-address "
		config.ReadinessPolicy = clientkit.ReadinessInformational
	})

	if client.Name() != "payments" || client.ReadinessPolicy() != clientkit.ReadinessInformational {
		t.Fatalf("client identity = (%q, %q), want configured values", client.Name(), client.ReadinessPolicy())
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
	if gotNetwork != "custom-network" || gotAddress != "dialer-address" {
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
	if snapshot.Name != "payments" || snapshot.ReadinessPolicy != clientkit.ReadinessOptional || snapshot.Health.State != clientkit.HealthUnknown {
		t.Fatalf("Snapshot() = %#v, want configured identity and unknown health", snapshot)
	}

	var nilClient *tcpclient.Client
	if health := nilClient.Health(); health.State != clientkit.HealthUnknown || health.Message == "" {
		t.Fatalf("nil Health() = %#v, want stable unknown health", health)
	}
	if snapshot := nilClient.Snapshot(); snapshot.Name != "" || snapshot.Health.State != clientkit.HealthUnknown {
		t.Fatalf("nil Snapshot() = %#v, want empty identity and unknown health", snapshot)
	}
	if nilClient.HealthCheckEnabled() {
		t.Fatal("nil HealthCheckEnabled() = true")
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
