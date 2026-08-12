package tcpclient_test

import (
	"context"
	"net"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/tcpclient"
)

func TestConnectionProbeFunc(t *testing.T) {
	want := clientkit.HealthAssessment{State: clientkit.HealthDegraded, Message: "fallback"}
	probe := tcpclient.ConnectionProbeFunc(func(context.Context, net.Conn) clientkit.HealthAssessment {
		return want
	})
	if got := probe.Probe(context.Background(), nil); got != want {
		t.Fatalf("Probe() = %#v, want %#v", got, want)
	}

	var nilProbe tcpclient.ConnectionProbeFunc
	got := nilProbe.Probe(context.Background(), nil)
	if got.State != clientkit.HealthUnhealthy || got.FailureClass != clientkit.FailurePolicy || got.Message == "" {
		t.Fatalf("nil Probe() = %#v, want stable policy failure", got)
	}
}

func TestNewAcceptsSupportedTCPConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tcpclient.Config)
	}{
		{name: "default network", mutate: func(*tcpclient.Config) {}},
		{name: "default observer", mutate: func(config *tcpclient.Config) { config.Observer = nil }},
		{name: "tcp4", mutate: func(config *tcpclient.Config) { config.Network = " TCP4 " }},
		{name: "tcp6 IPv6", mutate: func(config *tcpclient.Config) {
			config.Network = "tcp6"
			config.Address = "[::1]:65535"
		}},
		{name: "explicit durations", mutate: func(config *tcpclient.Config) {
			config.DialTimeout = time.Second
			config.KeepAlive = 2 * time.Second
		}},
		{name: "disabled durations", mutate: func(config *tcpclient.Config) {
			config.DisableDialTimeout = true
			config.DisableKeepAlive = true
		}},
		{name: "enabled check defaults", mutate: func(config *tcpclient.Config) {
			config.Check.Enabled = true
		}},
		{name: "disabled check durations", mutate: func(config *tcpclient.Config) {
			config.Check = tcpclient.CheckConfig{Enabled: true, DisableTimeout: true, DisableStaleAfter: true}
		}},
		{name: "disabled TLS handshake timeout", mutate: func(config *tcpclient.Config) {
			config.TLS = tcpclient.TLSConfig{Enabled: true, DisableHandshakeTimeout: true}
		}},
		{name: "custom dialer tcp6", mutate: func(config *tcpclient.Config) {
			config.Network = " TCP6 "
			config.Address = " dialer-owned-address "
			config.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, context.Canceled }
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := baseTCPConfig()
			test.mutate(&config)
			client, err := tcpclient.New(config)
			if err != nil || client == nil {
				t.Fatalf("New() = (%v, %v), want configured client", client, err)
			}
		})
	}
}

func TestNewRejectsInvalidTCPConfiguration(t *testing.T) {
	probe := tcpclient.ConnectionProbeFunc(func(context.Context, net.Conn) clientkit.HealthAssessment {
		return clientkit.HealthAssessment{State: clientkit.HealthHealthy}
	})
	var nilProbe tcpclient.ConnectionProbeFunc
	tests := []struct {
		name   string
		mutate func(*tcpclient.Config)
	}{
		{name: "invalid client name", mutate: func(config *tcpclient.Config) { config.Name = "Payments" }},
		{name: "missing address", mutate: func(config *tcpclient.Config) { config.Address = " " }},
		{name: "URL scheme", mutate: func(config *tcpclient.Config) { config.Address = "tcp://example.test:443" }},
		{name: "unsupported built-in network", mutate: func(config *tcpclient.Config) { config.Network = "udp" }},
		{name: "unsupported custom dialer UDP network", mutate: func(config *tcpclient.Config) {
			config.Network = "udp"
			config.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
		}},
		{name: "unsupported custom dialer Unix network", mutate: func(config *tcpclient.Config) {
			config.Network = "unix"
			config.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
		}},
		{name: "unsupported custom dialer network token", mutate: func(config *tcpclient.Config) {
			config.Network = "tenant-network"
			config.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
		}},
		{name: "missing port", mutate: func(config *tcpclient.Config) { config.Address = "example.test" }},
		{name: "empty port", mutate: func(config *tcpclient.Config) { config.Address = "example.test:" }},
		{name: "missing host", mutate: func(config *tcpclient.Config) { config.Address = ":443" }},
		{name: "named port", mutate: func(config *tcpclient.Config) { config.Address = "example.test:https" }},
		{name: "zero port", mutate: func(config *tcpclient.Config) { config.Address = "example.test:0" }},
		{name: "port above range", mutate: func(config *tcpclient.Config) { config.Address = "example.test:65536" }},
		{name: "numeric port overflow", mutate: func(config *tcpclient.Config) {
			config.Address = "example.test:999999999999999999999999"
		}},
		{name: "negative dial timeout", mutate: func(config *tcpclient.Config) { config.DialTimeout = -time.Second }},
		{name: "disabled configured dial timeout", mutate: func(config *tcpclient.Config) {
			config.DialTimeout = time.Second
			config.DisableDialTimeout = true
		}},
		{name: "negative keepalive", mutate: func(config *tcpclient.Config) { config.KeepAlive = -time.Second }},
		{name: "disabled configured keepalive", mutate: func(config *tcpclient.Config) {
			config.KeepAlive = time.Second
			config.DisableKeepAlive = true
		}},
		{name: "custom dialer keepalive", mutate: func(config *tcpclient.Config) {
			config.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
			config.KeepAlive = time.Second
		}},
		{name: "custom dialer disabled keepalive", mutate: func(config *tcpclient.Config) {
			config.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
			config.DisableKeepAlive = true
		}},
		{name: "disabled check with timeout", mutate: func(config *tcpclient.Config) { config.Check.Timeout = time.Second }},
		{name: "disabled check with timeout switch", mutate: func(config *tcpclient.Config) { config.Check.DisableTimeout = true }},
		{name: "disabled check with staleness", mutate: func(config *tcpclient.Config) { config.Check.StaleAfter = time.Second }},
		{name: "disabled check with staleness switch", mutate: func(config *tcpclient.Config) { config.Check.DisableStaleAfter = true }},
		{name: "disabled check with probe", mutate: func(config *tcpclient.Config) { config.Check.Probe = probe }},
		{name: "negative check timeout", mutate: func(config *tcpclient.Config) {
			config.Check = tcpclient.CheckConfig{Enabled: true, Timeout: -time.Second}
		}},
		{name: "disabled configured check timeout", mutate: func(config *tcpclient.Config) {
			config.Check = tcpclient.CheckConfig{Enabled: true, Timeout: time.Second, DisableTimeout: true}
		}},
		{name: "negative stale after", mutate: func(config *tcpclient.Config) {
			config.Check = tcpclient.CheckConfig{Enabled: true, StaleAfter: -time.Second}
		}},
		{name: "disabled configured stale after", mutate: func(config *tcpclient.Config) {
			config.Check = tcpclient.CheckConfig{Enabled: true, StaleAfter: time.Second, DisableStaleAfter: true}
		}},
		{name: "typed nil probe", mutate: func(config *tcpclient.Config) {
			config.Check = tcpclient.CheckConfig{Enabled: true, Probe: nilProbe}
		}},
		{name: "blocking readiness without check", mutate: func(config *tcpclient.Config) {
			config.ReadinessPolicy = clientkit.ReadinessRequired
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := baseTCPConfig()
			test.mutate(&config)
			client, err := tcpclient.New(config)
			if err == nil || client != nil {
				t.Fatalf("New() = (%v, %v), want nil client and validation error", client, err)
			}
		})
	}
}
