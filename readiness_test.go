package clientkit_test

import (
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
)

func TestReadinessPolicyVocabulary(t *testing.T) {
	tests := []struct {
		policy clientkit.ReadinessPolicy
		want   string
	}{
		{policy: clientkit.ReadinessRequired, want: "required"},
		{policy: clientkit.ReadinessOptional, want: "optional"},
		{policy: clientkit.ReadinessDegradedAllowed, want: "degraded_allowed"},
		{policy: clientkit.ReadinessInformational, want: "informational"},
	}
	for _, test := range tests {
		if got := string(test.policy); got != test.want {
			t.Errorf("readiness policy = %q, want %q", got, test.want)
		}
	}
}

func TestReadinessPolicyValidation(t *testing.T) {
	valid := []struct {
		name   string
		policy clientkit.ReadinessPolicy
	}{
		{name: "zero value", policy: ""},
		{name: "required", policy: clientkit.ReadinessRequired},
		{name: "optional", policy: clientkit.ReadinessOptional},
		{name: "degraded allowed", policy: clientkit.ReadinessDegradedAllowed},
		{name: "informational", policy: clientkit.ReadinessInformational},
	}
	for _, test := range valid {
		t.Run("valid "+test.name, func(t *testing.T) {
			if err := (clientkit.Config{Name: "payments", ReadinessPolicy: test.policy}).Validate(); err != nil {
				t.Fatalf("Config.Validate() error = %v", err)
			}
		})
	}

	invalid := []struct {
		name   string
		policy clientkit.ReadinessPolicy
	}{
		{name: "unknown", policy: "invalid"},
		{name: "uppercase", policy: "Required"},
		{name: "wrong separator", policy: "degraded-allowed"},
		{name: "trailing whitespace", policy: "informational "},
	}
	for _, test := range invalid {
		t.Run("invalid "+test.name, func(t *testing.T) {
			if err := (clientkit.Config{Name: "payments", ReadinessPolicy: test.policy}).Validate(); err == nil {
				t.Fatal("Config.Validate() error = nil, want invalid readiness policy")
			}
		})
	}
}

func TestReadinessPolicyZeroValueNormalizesToOptional(t *testing.T) {
	client, err := clientkit.New(clientkit.Config{Name: "payments"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := client.ReadinessPolicy(); got != clientkit.ReadinessOptional {
		t.Fatalf("ReadinessPolicy() = %q, want %q", got, clientkit.ReadinessOptional)
	}
	if client.ReadinessPolicy().BlocksReadiness() {
		t.Fatal("zero-value readiness policy blocks readiness")
	}
}

func TestReadinessPolicyBlocksReadiness(t *testing.T) {
	tests := []struct {
		name   string
		policy clientkit.ReadinessPolicy
		want   bool
	}{
		{name: "required", policy: clientkit.ReadinessRequired, want: true},
		{name: "degraded allowed", policy: clientkit.ReadinessDegradedAllowed, want: true},
		{name: "optional", policy: clientkit.ReadinessOptional},
		{name: "informational", policy: clientkit.ReadinessInformational},
		{name: "zero value", policy: ""},
		{name: "invalid", policy: "invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.policy.BlocksReadiness(); got != test.want {
				t.Fatalf("BlocksReadiness() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestReadinessPolicyHealthMatrix(t *testing.T) {
	states := []clientkit.HealthState{
		clientkit.HealthHealthy,
		clientkit.HealthDegraded,
		clientkit.HealthUnknown,
		clientkit.HealthUnhealthy,
	}
	// Omitted map entries intentionally mean not ready, keeping each policy's
	// accepted states visible without repeating false values.
	tests := []struct {
		policy clientkit.ReadinessPolicy
		ready  map[clientkit.HealthState]bool
	}{
		{
			policy: clientkit.ReadinessRequired,
			ready:  map[clientkit.HealthState]bool{clientkit.HealthHealthy: true},
		},
		{
			policy: clientkit.ReadinessDegradedAllowed,
			ready: map[clientkit.HealthState]bool{
				clientkit.HealthHealthy:  true,
				clientkit.HealthDegraded: true,
			},
		},
		{
			policy: clientkit.ReadinessOptional,
			ready: map[clientkit.HealthState]bool{
				clientkit.HealthHealthy:   true,
				clientkit.HealthDegraded:  true,
				clientkit.HealthUnknown:   true,
				clientkit.HealthUnhealthy: true,
			},
		},
		{
			policy: clientkit.ReadinessInformational,
			ready: map[clientkit.HealthState]bool{
				clientkit.HealthHealthy:   true,
				clientkit.HealthDegraded:  true,
				clientkit.HealthUnknown:   true,
				clientkit.HealthUnhealthy: true,
			},
		},
	}

	for _, test := range tests {
		for _, state := range states {
			name := string(test.policy) + "_" + string(state)
			t.Run(name, func(t *testing.T) {
				want := test.ready[state]
				if got := (clientkit.Health{State: state}).IsReady(test.policy); got != want {
					t.Fatalf("Health{%q}.IsReady(%q) = %t, want %t", state, test.policy, got, want)
				}
			})
		}
	}

	// Custom states can reach this method from unsanitized external clients.
	// Non-blocking policies stay non-blocking; blocking and invalid policies fail closed.
	customState := []struct {
		policy clientkit.ReadinessPolicy
		ready  bool
	}{
		{policy: "", ready: true},
		{policy: clientkit.ReadinessOptional, ready: true},
		{policy: clientkit.ReadinessInformational, ready: true},
		{policy: clientkit.ReadinessRequired},
		{policy: clientkit.ReadinessDegradedAllowed},
		{policy: "invalid"},
	}
	for _, test := range customState {
		if got := (clientkit.Health{State: "custom"}).IsReady(test.policy); got != test.ready {
			t.Errorf("custom health IsReady(%q) = %t, want %t", test.policy, got, test.ready)
		}
	}
}
