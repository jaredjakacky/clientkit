package tcpclient_test

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/jaredjakacky/clientkit/tcpclient"
)

func TestTCPDefaults(t *testing.T) {
	if tcpclient.DefaultNetwork != "tcp" {
		t.Fatalf("DefaultNetwork = %q, want tcp", tcpclient.DefaultNetwork)
	}
	if tcpclient.DefaultDialTimeout != 5*time.Second {
		t.Fatalf("DefaultDialTimeout = %v, want 5s", tcpclient.DefaultDialTimeout)
	}
	if tcpclient.DefaultKeepAlive != 30*time.Second {
		t.Fatalf("DefaultKeepAlive = %v, want 30s", tcpclient.DefaultKeepAlive)
	}
	if tcpclient.DefaultCheckTimeout != 5*time.Second {
		t.Fatalf("DefaultCheckTimeout = %v, want 5s", tcpclient.DefaultCheckTimeout)
	}
	if tcpclient.DefaultCheckStaleAfter != 90*time.Second {
		t.Fatalf("DefaultCheckStaleAfter = %v, want 90s", tcpclient.DefaultCheckStaleAfter)
	}
	if tcpclient.DefaultTLSHandshakeTimeout != 10*time.Second {
		t.Fatalf("DefaultTLSHandshakeTimeout = %v, want 10s", tcpclient.DefaultTLSHandshakeTimeout)
	}
	if tcpclient.DefaultTLSMinVersion != tls.VersionTLS12 {
		t.Fatalf("DefaultTLSMinVersion = %d, want TLS 1.2", tcpclient.DefaultTLSMinVersion)
	}
}

func TestDefaultTLSConfigReturnsIndependentPolicy(t *testing.T) {
	first := tcpclient.DefaultTLSConfig("example.test")
	if first.ServerName != "example.test" || first.MinVersion != tls.VersionTLS12 || first.InsecureSkipVerify {
		t.Fatalf("DefaultTLSConfig() = %#v, want verifying TLS 1.2 policy", first)
	}

	first.ServerName = "changed.test"
	second := tcpclient.DefaultTLSConfig("example.test")
	if second == first || second.ServerName != "example.test" {
		t.Fatalf("second DefaultTLSConfig() = %#v, want independent policy", second)
	}
}
