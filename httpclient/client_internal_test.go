package httpclient

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/internal/httpotel"
)

func TestHTTPTransportInstrumentationRespectsOwnershipAndExplicitObservation(t *testing.T) {
	t.Run("owned defaults", func(t *testing.T) {
		client, err := New(Config{
			Config:  clientkit.Config{Name: "owned-defaults"},
			BaseURL: "https://example.test",
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if !client.transportInjects {
			t.Fatal("owned default client does not mark its transport as instrumented")
		}
		if _, ok := client.httpClient.Transport.(*httpotel.Transport); !ok {
			t.Fatalf("owned default transport = %T, want Clientkit HTTP OTel transport", client.httpClient.Transport)
		}
	})

	t.Run("explicit observer disables automatic attempt spans", func(t *testing.T) {
		client, err := New(Config{
			Config: clientkit.Config{
				Name:     "explicit-observer",
				Observer: clientkit.NopObserver{},
			},
			BaseURL: "https://example.test",
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if client.transportInjects {
			t.Fatal("explicit observer unexpectedly enabled automatic attempt spans")
		}
		if _, ok := client.httpClient.Transport.(*http.Transport); !ok {
			t.Fatalf("explicit-observer transport = %T, want ordinary default HTTP transport", client.httpClient.Transport)
		}
	})

	t.Run("caller-owned HTTP client is not mutated", func(t *testing.T) {
		transport := http.DefaultTransport
		httpClient := &http.Client{Transport: transport}
		client, err := New(Config{
			Config:     clientkit.Config{Name: "caller-owned"},
			BaseURL:    "https://example.test",
			HTTPClient: httpClient,
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if client.transportInjects {
			t.Fatal("caller-owned HTTP client unexpectedly marked transport as Clientkit-instrumented")
		}
		if client.httpClient != httpClient || httpClient.Transport != transport {
			t.Fatal("New() replaced or mutated the caller-owned HTTP client")
		}
	})
}

func TestHTTPClientClosesIdleConnectionsThroughInstrumentation(t *testing.T) {
	base := &internalIdleClosingTransport{}
	instrumented, err := httpotel.NewTransport(base, httpotel.Config{})
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	client, err := New(Config{
		Config:     clientkit.Config{Name: "idle-cleanup", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test",
		HTTPClient: &http.Client{Transport: instrumented},
		Propagator: NopHeaderPropagator{},
		Retry:      NoRetryConfig(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	client.CloseIdleConnections()
	if base.closeCalls != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", base.closeCalls)
	}
}

type internalIdleClosingTransport struct {
	closeCalls int
}

func (*internalIdleClosingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.Canceled
}

func (transport *internalIdleClosingTransport) CloseIdleConnections() {
	transport.closeCalls++
}

func TestHTTPDefaultCheckStalenessPreservesRequiredReadinessAcrossNominalCadence(t *testing.T) {
	client := newHTTPHealthProjectionClient(t, clientkit.ReadinessRequired, DefaultCheckConfig("/healthz"))
	registry := clientkit.NewRegistry()
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	fresh := client.core.UpdateHealth(clientkit.Health{
		State:     clientkit.HealthHealthy,
		CheckedAt: time.Now().UTC().Add(-45 * time.Second),
		Message:   "cached healthy",
	})
	if got := client.Health(); got != fresh {
		t.Fatalf("Health() = %#v, want fresh cached health %#v", got, fresh)
	}
	if readiness := registry.Readiness(context.Background()); !readiness.Ready {
		t.Fatalf("Readiness() = %#v, want ready with fresh required health", readiness)
	}

	stale := client.core.UpdateHealth(clientkit.Health{
		State:     clientkit.HealthHealthy,
		CheckedAt: time.Now().UTC().Add(-2 * time.Minute),
		Message:   "cached healthy",
	})
	if got := client.Health(); got.State != clientkit.HealthUnknown || !strings.Contains(got.Message, "stale") {
		t.Fatalf("Health() = %#v, want stale unknown projection", got)
	}
	if readiness := registry.Readiness(context.Background()); readiness.Ready {
		t.Fatalf("Readiness() = %#v, want not ready with stale required health", readiness)
	}
	if got := client.core.Health(); got != stale {
		t.Fatalf("cached Health() = %#v, want unchanged %#v", got, stale)
	}
}

func TestHTTPHealthStalenessProjection(t *testing.T) {
	for _, test := range []struct {
		name      string
		check     CheckConfig
		wantState clientkit.HealthState
	}{
		{
			name:      "stale health is projected unknown",
			check:     CheckConfig{Enabled: true, Path: "/healthz", StaleAfter: time.Second},
			wantState: clientkit.HealthUnknown,
		},
		{
			name:      "disabled staleness preserves health",
			check:     CheckConfig{Enabled: true, Path: "/healthz", DisableStaleAfter: true},
			wantState: clientkit.HealthHealthy,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newHTTPHealthProjectionClient(t, clientkit.ReadinessOptional, test.check)
			client.core.UpdateHealth(clientkit.Health{
				State:     clientkit.HealthHealthy,
				CheckedAt: time.Now().Add(-time.Hour).UTC(),
				Message:   "cached",
			})
			projected := client.Health()
			if projected.State != test.wantState {
				t.Fatalf("Health() = %#v, want state %q", projected, test.wantState)
			}
			if test.wantState == clientkit.HealthUnknown && !strings.Contains(projected.Message, "stale") {
				t.Fatalf("Health() message = %q, want stale projection", projected.Message)
			}
		})
	}
}

func newHTTPHealthProjectionClient(t *testing.T, policy clientkit.ReadinessPolicy, check CheckConfig) *Client {
	t.Helper()
	client, err := New(Config{
		Config: clientkit.Config{
			Name:            "health-projection",
			ReadinessPolicy: policy,
			Observer:        clientkit.NopObserver{},
		},
		BaseURL:    "https://example.test",
		Propagator: NopHeaderPropagator{},
		Check:      check,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}
