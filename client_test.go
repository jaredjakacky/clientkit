package clientkit_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
)

func TestValidateClientName(t *testing.T) {
	if clientkit.MaxClientNameBytes != 64 {
		t.Fatalf("MaxClientNameBytes = %d, want 64", clientkit.MaxClientNameBytes)
	}

	valid := []struct {
		name  string
		value string
	}{
		{name: "letter", value: "a"},
		{name: "digit", value: "1"},
		{name: "numeric boundaries", value: "1payments2"},
		{name: "letters", value: "payments"},
		{name: "hyphen", value: "payments-v2"},
		{name: "underscore", value: "payments_v2"},
		{name: "period", value: "payments.eu.v2"},
		{name: "mixed separators", value: "a-1_b.c"},
		{name: "repeated hyphens", value: "payments--v2"},
		{name: "repeated underscores", value: "payments__v2"},
		{name: "maximum length", value: strings.Repeat("a", clientkit.MaxClientNameBytes)},
	}
	for _, test := range valid {
		t.Run("valid "+test.name, func(t *testing.T) {
			if err := clientkit.ValidateClientName(test.value); err != nil {
				t.Fatalf("ValidateClientName(%q) error = %v", test.value, err)
			}
		})
	}

	invalid := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "leading whitespace", value: " payments"},
		{name: "trailing whitespace", value: "payments "},
		{name: "uppercase", value: "Payments"},
		{name: "leading hyphen", value: "-payments"},
		{name: "trailing hyphen", value: "payments-"},
		{name: "leading underscore", value: "_payments"},
		{name: "trailing underscore", value: "payments_"},
		{name: "leading period", value: ".payments"},
		{name: "trailing period", value: "payments."},
		{name: "consecutive periods", value: "payments..v2"},
		{name: "embedded whitespace", value: "pay ments"},
		{name: "slash", value: "payments/v2"},
		{name: "colon", value: "payments:v2"},
		{name: "non-ASCII", value: "pâyments"},
		{name: "over maximum length", value: strings.Repeat("a", clientkit.MaxClientNameBytes+1)},
	}
	for _, test := range invalid {
		t.Run("invalid "+test.name, func(t *testing.T) {
			if err := clientkit.ValidateClientName(test.value); err == nil {
				t.Fatalf("ValidateClientName(%q) error = nil, want validation failure", test.value)
			}
		})
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	client, err := clientkit.New(clientkit.Config{Name: "Payments"})
	if err == nil {
		t.Fatal("New() error = nil, want validation failure")
	}
	if client != nil {
		t.Fatalf("New() client = %#v, want nil after validation failure", client)
	}
}

func TestClientLifecycle(t *testing.T) {
	client, err := clientkit.New(clientkit.Config{Name: "payments"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client.Name() != "payments" {
		t.Fatalf("Name() = %q, want %q", client.Name(), "payments")
	}
	if client.ReadinessPolicy() != clientkit.ReadinessOptional {
		t.Fatalf("ReadinessPolicy() = %q, want %q", client.ReadinessPolicy(), clientkit.ReadinessOptional)
	}
	if health := client.Health(); health.State != clientkit.HealthUnknown {
		t.Fatalf("initial Health() = %#v, want unknown", health)
	}

	updated := client.UpdateHealth(clientkit.Health{
		State:        clientkit.HealthHealthy,
		FailureClass: clientkit.FailureTransport,
		Message:      "available",
	})
	if updated.State != clientkit.HealthHealthy || updated.FailureClass != clientkit.FailureNone {
		t.Fatalf("UpdateHealth() = %#v, want healthy with no failure", updated)
	}
	if got := client.Health(); got != updated {
		t.Fatalf("Health() = %#v, want cached %#v", got, updated)
	}
	snapshot := client.Snapshot()
	if snapshot.Name != "payments" || snapshot.ReadinessPolicy != clientkit.ReadinessOptional || snapshot.Health != updated {
		t.Fatalf("Snapshot() = %#v, want current identity and health", snapshot)
	}
	// A snapshot represents one instant and must not follow later cache updates.
	client.UpdateHealth(clientkit.Health{State: clientkit.HealthUnhealthy, Message: "offline"})
	if snapshot.Health != updated {
		t.Fatalf("earlier snapshot health = %#v after update, want %#v", snapshot.Health, updated)
	}

	operationCtx := context.Background()
	ctx, observation := client.Observer().StartOperation(operationCtx, clientkit.OperationStartEvent{})
	if ctx != operationCtx {
		t.Fatal("default observer changed context")
	}
	observation.End(ctx, clientkit.OperationEndEvent{})
}

func TestClientPreservesConfiguredPolicyAndObserver(t *testing.T) {
	observed := false
	observer := observerCallbacks{
		health: func(context.Context, clientkit.HealthEvent) {
			observed = true
		},
	}
	client, err := clientkit.New(clientkit.Config{
		Name:            "payments",
		ReadinessPolicy: clientkit.ReadinessRequired,
		Observer:        observer,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client.ReadinessPolicy() != clientkit.ReadinessRequired {
		t.Fatalf("ReadinessPolicy() = %q, want %q", client.ReadinessPolicy(), clientkit.ReadinessRequired)
	}

	client.Observer().ObserveHealth(context.Background(), clientkit.HealthEvent{Client: "payments"})
	if !observed {
		t.Fatal("configured observer did not receive health event")
	}
}

func TestNilClientObserverIsNoOp(t *testing.T) {
	var client *clientkit.Client
	ctx := context.Background()
	next, observation := client.Observer().StartOperation(ctx, clientkit.OperationStartEvent{})
	if next != ctx {
		t.Fatal("nil Client observer changed context")
	}
	observation.End(ctx, clientkit.OperationEndEvent{})
	client.Observer().ObserveAttempt(ctx, clientkit.AttemptEvent{})
	client.Observer().ObserveRetry(ctx, clientkit.RetryEvent{})
	client.Observer().ObserveHealth(ctx, clientkit.HealthEvent{})
}

func TestClientHealthSanitizerPolicy(t *testing.T) {
	t.Run("custom sanitizer", func(t *testing.T) {
		var gotName string
		client, err := clientkit.New(clientkit.Config{
			Name: "payments",
			HealthSanitizer: func(name string, health clientkit.Health) clientkit.Health {
				gotName = name
				health.Message = "custom"
				return health
			},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		health := client.UpdateHealth(clientkit.Health{State: clientkit.HealthHealthy, Message: "raw"})
		if gotName != "payments" || health.Message != "custom" {
			t.Fatalf("UpdateHealth() = %#v with name %q, want custom sanitizer result", health, gotName)
		}
	})

	t.Run("sanitizer panic", func(t *testing.T) {
		client, err := clientkit.New(clientkit.Config{
			Name: "payments",
			HealthSanitizer: func(string, clientkit.Health) clientkit.Health {
				panic("sanitize")
			},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		health := client.UpdateHealth(clientkit.Health{State: clientkit.HealthHealthy})
		if health.State != clientkit.HealthUnknown || health.FailureClass != clientkit.FailurePolicy || health.Message != "client health sanitizer failed" {
			t.Fatalf("UpdateHealth() = %#v, want contained sanitizer failure", health)
		}
	})

	t.Run("sanitizer disabled", func(t *testing.T) {
		client, err := clientkit.New(clientkit.Config{Name: "payments", DisableHealthSanitizer: true})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		raw := clientkit.Health{State: "custom", FailureClass: "custom", Message: "raw\nvalue"}
		if got := client.UpdateHealth(raw); got != raw {
			t.Fatalf("UpdateHealth() = %#v, want raw %#v", got, raw)
		}
	})
}

func TestClientHealthIsSafeForConcurrentUse(t *testing.T) {
	client, err := clientkit.New(clientkit.Config{Name: "payments"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const workers = 16
	var group sync.WaitGroup
	group.Add(workers * 2)
	for index := range workers {
		go func() {
			defer group.Done()
			client.UpdateHealth(clientkit.Health{State: clientkit.HealthHealthy, Message: string(rune('a' + index))})
		}()
		go func() {
			defer group.Done()
			_ = client.Health()
			_ = client.Snapshot()
		}()
	}
	group.Wait()

	if client.Health().State != clientkit.HealthHealthy {
		t.Fatalf("final Health() = %#v, want healthy", client.Health())
	}
}
