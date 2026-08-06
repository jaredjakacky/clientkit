package clientkit_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
)

type publicRegisteredClient struct {
	name   string
	policy clientkit.ReadinessPolicy
	health clientkit.Health
}

func (c *publicRegisteredClient) Name() string {
	return c.name
}

func (c *publicRegisteredClient) ReadinessPolicy() clientkit.ReadinessPolicy {
	return c.policy
}

func (c *publicRegisteredClient) Health() clientkit.Health {
	return c.health
}

// Embedding the passive client verifies that HealthChecker extends the complete
// RegisteredClient contract without requiring package-private methods.
type publicHealthChecker struct {
	*publicRegisteredClient
}

func (c *publicHealthChecker) Check(context.Context) clientkit.Health {
	return c.health
}

// These capability-only fixtures intentionally omit RegisteredClient methods.
// They guard the optional interfaces from becoming coupled to registry identity.
type publicHealthCheckConfig struct {
}

func (publicHealthCheckConfig) HealthCheckEnabled() bool {
	return true
}

type publicIdleConnectionCloser struct{}

func (*publicIdleConnectionCloser) CloseIdleConnections() {}

func TestRegistrySnapshotJSONContract(t *testing.T) {
	checkedAt := time.Date(2026, time.August, 5, 14, 30, 0, 123_000_000, time.UTC)
	want := clientkit.RegistrySnapshot{
		Clients: []clientkit.ClientSnapshot{
			{
				Name:            "payments",
				ReadinessPolicy: clientkit.ReadinessDegradedAllowed,
				Health: clientkit.Health{
					State:        clientkit.HealthDegraded,
					FailureClass: clientkit.FailureRemoteResponse,
					CheckedAt:    checkedAt,
					Duration:     1500 * time.Millisecond,
					Message:      "fallback available",
				},
			},
		},
	}

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	const wantJSON = `{"clients":[{"name":"payments","readiness_policy":"degraded_allowed","health":{"state":"degraded","failure_class":"remote_response","checked_at":"2026-08-05T14:30:00.123Z","duration":1500000000,"message":"fallback available"}}]}`
	if string(encoded) != wantJSON {
		t.Fatalf("json.Marshal() = %s, want %s", encoded, wantJSON)
	}

	var decoded clientkit.RegistrySnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("JSON round trip = %#v, want %#v", decoded, want)
	}
}

var _ clientkit.RegisteredClient = (*publicRegisteredClient)(nil)
var _ clientkit.HealthChecker = (*publicHealthChecker)(nil)
var _ clientkit.HealthCheckConfigurable = publicHealthCheckConfig{}
var _ clientkit.IdleConnectionCloser = (*publicIdleConnectionCloser)(nil)
