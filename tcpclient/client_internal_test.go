package tcpclient

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jaredjakacky/clientkit"
)

func TestTCPHealthProjectsStalenessWithoutMutatingCache(t *testing.T) {
	client := newTCPHealthProjectionClient(t, clientkit.ReadinessOptional, CheckConfig{Enabled: true, StaleAfter: time.Second}, nil)

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
			raw := client.core.UpdateHealth(clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: test.checkedAt, Message: "raw"})
			projected := client.Health()
			if projected.State != clientkit.HealthUnknown || !strings.Contains(projected.Message, test.wantPhrase) {
				t.Fatalf("Health() = %#v, want unknown %q projection", projected, test.wantPhrase)
			}
			if cached := client.core.Health(); cached != raw {
				t.Fatalf("cached Health() = %#v, want unchanged %#v", cached, raw)
			}
		})
	}

	fresh := client.core.UpdateHealth(clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC(), Message: "fresh"})
	if got := client.Snapshot().Health; got != fresh {
		t.Fatalf("Snapshot().Health = %#v, want fresh %#v", got, fresh)
	}
}

func TestTCPDefaultCheckStalenessPreservesDegradedAllowedReadinessAcrossNominalCadence(t *testing.T) {
	dialCalls := 0
	client := newTCPHealthProjectionClient(t, clientkit.ReadinessDegradedAllowed, CheckConfig{Enabled: true}, func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		return nil, context.Canceled
	})
	registry := clientkit.NewRegistry()
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	fresh := client.core.UpdateHealth(clientkit.Health{
		State:        clientkit.HealthDegraded,
		FailureClass: clientkit.FailureRemoteResponse,
		CheckedAt:    time.Now().UTC().Add(-45 * time.Second),
		Message:      "fallback available",
	})
	if got := client.Health(); got != fresh {
		t.Fatalf("Health() = %#v, want fresh cached health %#v", got, fresh)
	}
	if readiness := registry.Readiness(context.Background()); !readiness.Ready {
		t.Fatalf("Readiness() = %#v, want ready with fresh degraded-allowed health", readiness)
	}

	stale := client.core.UpdateHealth(clientkit.Health{
		State:        clientkit.HealthDegraded,
		FailureClass: clientkit.FailureRemoteResponse,
		CheckedAt:    time.Now().UTC().Add(-2 * time.Minute),
		Message:      "fallback available",
	})
	if got := client.Health(); got.State != clientkit.HealthUnknown || !strings.Contains(got.Message, "stale") {
		t.Fatalf("Health() = %#v, want stale unknown projection", got)
	}
	if readiness := registry.Readiness(context.Background()); readiness.Ready {
		t.Fatalf("Readiness() = %#v, want not ready with stale degraded-allowed health", readiness)
	}
	if got := client.core.Health(); got != stale {
		t.Fatalf("cached Health() = %#v, want unchanged %#v", got, stale)
	}
	if dialCalls != 0 {
		t.Fatalf("passive health/readiness performed %d dials, want 0", dialCalls)
	}
}

func TestTCPHealthCanDisableStalenessProjection(t *testing.T) {
	client := newTCPHealthProjectionClient(t, clientkit.ReadinessOptional, CheckConfig{Enabled: true, DisableStaleAfter: true}, nil)
	cached := client.core.UpdateHealth(clientkit.Health{
		State:     clientkit.HealthHealthy,
		CheckedAt: time.Now().UTC().Add(-24 * time.Hour),
		Message:   "old but authoritative",
	})

	if got := client.Health(); got != cached {
		t.Fatalf("Health() = %#v, want unprojected cached health %#v", got, cached)
	}
}

func newTCPHealthProjectionClient(t *testing.T, policy clientkit.ReadinessPolicy, check CheckConfig, dial DialContextFunc) *Client {
	t.Helper()
	if dial == nil {
		dial = func(context.Context, string, string) (net.Conn, error) {
			return nil, context.Canceled
		}
	}
	client, err := New(Config{
		Config: clientkit.Config{
			Name:            "health-projection",
			ReadinessPolicy: policy,
			Observer:        clientkit.NopObserver{},
		},
		Network:     "custom",
		Address:     "test-address",
		DialContext: dial,
		Check:       check,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}
