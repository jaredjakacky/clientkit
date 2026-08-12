package clientkit_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/opskit"
)

type registryClient struct {
	name           string
	protocol       string
	policy         clientkit.ReadinessPolicy
	health         clientkit.Health
	namePanics     bool
	protocolPanics bool
	protocolEmpty  bool
	policyPanics   bool
	healthPanics   bool
	nameCalls      int
	protocolCalls  int
	policyCalls    int
	healthCalls    int
}

func (c *registryClient) Name() string {
	c.nameCalls++
	if c.namePanics {
		panic("name panic")
	}
	return c.name
}

func (c *registryClient) Protocol() string {
	c.protocolCalls++
	if c.protocolPanics {
		panic("protocol panic")
	}
	if c.protocolEmpty {
		return ""
	}
	if c.protocol == "" {
		return "test"
	}
	return c.protocol
}

func (c *registryClient) ReadinessPolicy() clientkit.ReadinessPolicy {
	c.policyCalls++
	if c.policyPanics {
		panic("policy panic")
	}
	return c.policy
}

func (c *registryClient) Health() clientkit.Health {
	c.healthCalls++
	if c.healthPanics {
		panic("health panic")
	}
	return c.health
}

type configurableRegistryChecker struct {
	*registryClient
	enabled       bool
	enabledPanics bool
	enabledCalls  int
}

func (c *configurableRegistryChecker) Check(context.Context) clientkit.Health {
	return c.Health()
}

func (c *configurableRegistryChecker) HealthCheckEnabled() bool {
	c.enabledCalls++
	if c.enabledPanics {
		panic("enabled panic")
	}
	return c.enabled
}

type registryChecker struct {
	*registryClient
}

func (c *registryChecker) Check(context.Context) clientkit.Health {
	return c.Health()
}

type closingRegistryClient struct {
	*registryClient
	close func()
}

func (c *closingRegistryClient) CloseIdleConnections() {
	if c.close != nil {
		c.close()
	}
}

func TestDefaultRegistryConfig(t *testing.T) {
	first := clientkit.DefaultRegistryConfig()
	if first.MaxConcurrentChecks != clientkit.DefaultMaxConcurrentChecks {
		t.Fatalf("MaxConcurrentChecks = %d, want %d", first.MaxConcurrentChecks, clientkit.DefaultMaxConcurrentChecks)
	}
	if first.ComponentInfo.Name != "clients" {
		t.Errorf("ComponentInfo.Name = %q, want %q", first.ComponentInfo.Name, "clients")
	}
	if first.ComponentInfo.Kind != "client_registry" {
		t.Errorf("ComponentInfo.Kind = %q, want %q", first.ComponentInfo.Kind, "client_registry")
	}
	if first.ComponentInfo.Description == "" {
		t.Error("ComponentInfo.Description is empty")
	}
	if !reflect.DeepEqual(first.ComponentInfo.Labels, []opskit.Attribute{opskit.Attr("kit", "clientkit")}) {
		t.Errorf("ComponentInfo.Labels = %#v, want stable Clientkit label", first.ComponentInfo.Labels)
	}

	first.ComponentInfo.Labels[0].Value = "changed"
	second := clientkit.DefaultRegistryConfig()
	if second.ComponentInfo.Labels[0].Value != "clientkit" {
		t.Fatal("DefaultRegistryConfig returned shared label storage")
	}
}

func TestRegistryConstruction(t *testing.T) {
	t.Run("default constructor", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		if registry == nil {
			t.Fatal("NewRegistry returned nil")
		}
		if clients := registry.Snapshot().Clients; len(clients) != 0 {
			t.Fatalf("Snapshot contains %d clients, want 0", len(clients))
		}
		if client, ok := registry.Get("missing"); ok || client != nil {
			t.Fatalf("Get(missing) = (%v, %t), want (nil, false)", client, ok)
		}
	})

	t.Run("zero value", func(t *testing.T) {
		var registry clientkit.Registry
		client := &registryChecker{registryClient: &registryClient{
			name:   "zero-value",
			health: clientkit.Health{State: clientkit.HealthHealthy, FailureClass: clientkit.FailureTransport},
		}}
		if err := registry.Register(client); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		got, ok := registry.Get(client.name)
		if !ok || got != client {
			t.Fatalf("Get(%q) = (%v, %t), want original client", client.name, got, ok)
		}
		if info := registry.ComponentInfo(); info.Name != "clients" || info.Kind != "client_registry" {
			t.Fatalf("ComponentInfo() = %#v, want defaults", info)
		}
		snapshot := registry.Snapshot()
		if len(snapshot.Clients) != 1 || snapshot.Clients[0].Health.FailureClass != clientkit.FailureNone {
			t.Fatalf("Snapshot() = %#v, want sanitized healthy client", snapshot)
		}
		if summary := registry.CheckAll(context.Background()); !summary.Ready || len(summary.Results) != 1 {
			t.Fatalf("CheckAll() = %#v, want one ready result", summary)
		}
	})

	t.Run("nil receiver reads", func(t *testing.T) {
		var registry *clientkit.Registry
		if client, ok := registry.Get("missing"); ok || client != nil {
			t.Fatalf("Get(missing) = (%v, %t), want (nil, false)", client, ok)
		}
		if clients := registry.Snapshot().Clients; len(clients) != 0 {
			t.Fatalf("Snapshot contains %d clients, want 0", len(clients))
		}
	})

	tests := []struct {
		name    string
		config  clientkit.RegistryConfig
		wantErr string
	}{
		{
			name:    "negative concurrency",
			config:  clientkit.RegistryConfig{MaxConcurrentChecks: -1},
			wantErr: "max concurrent checks must not be negative",
		},
		{
			name:    "invalid component name",
			config:  clientkit.RegistryConfig{ComponentInfo: opskit.ComponentInfo{Name: "invalid/name"}},
			wantErr: "invalid registry component name",
		},
		{
			name: "sanitizer set and disabled",
			config: clientkit.RegistryConfig{
				HealthSanitizer:        func(_ string, health clientkit.Health) clientkit.Health { return health },
				DisableHealthSanitizer: true,
			},
			wantErr: "health sanitizer cannot be set and disabled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := clientkit.NewRegistryWithConfig(test.config)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("NewRegistryWithConfig() error = %v, want containing %q", err, test.wantErr)
			}
			if registry != nil {
				t.Fatalf("NewRegistryWithConfig() registry = %v, want nil", registry)
			}
		})
	}

	t.Run("custom component info is normalized and immutable", func(t *testing.T) {
		config := clientkit.RegistryConfig{
			MaxConcurrentChecks: 1,
			ComponentInfo: opskit.ComponentInfo{
				Name:        "outbound-clients",
				Kind:        "dependency_registry",
				Description: "Outbound dependencies",
				Labels: []opskit.Attribute{
					opskit.Attr("environment", "test"),
					opskit.Attr("kit", "caller-value"),
				},
			},
		}
		registry, err := clientkit.NewRegistryWithConfig(config)
		if err != nil {
			t.Fatalf("NewRegistryWithConfig() error = %v", err)
		}
		config.ComponentInfo.Labels[0].Value = "changed"

		info := registry.ComponentInfo()
		if info.Name != "outbound-clients" || info.Kind != "dependency_registry" || info.Description != "Outbound dependencies" {
			t.Fatalf("ComponentInfo() = %#v, want configured identity", info)
		}
		wantLabels := []opskit.Attribute{
			opskit.Attr("environment", "test"),
			opskit.Attr("kit", "clientkit"),
		}
		if !reflect.DeepEqual(info.Labels, wantLabels) {
			t.Fatalf("ComponentInfo().Labels = %#v, want %#v", info.Labels, wantLabels)
		}

		info.Labels[0].Value = "mutated"
		if got := registry.ComponentInfo().Labels[0].Value; got != "test" {
			t.Fatalf("ComponentInfo label after caller mutation = %q, want %q", got, "test")
		}
	})
}

func TestRegistryRegisterValidation(t *testing.T) {
	valid := func(name string) *registryClient {
		return &registryClient{name: name, health: clientkit.Health{State: clientkit.HealthHealthy}}
	}

	t.Run("nil registry", func(t *testing.T) {
		var registry *clientkit.Registry
		requireErrorContains(t, registry.Register(valid("client")), "registry is required")
	})

	t.Run("nil clients", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		requireErrorContains(t, registry.Register(nil), "cannot register nil client")

		var typedNil *registryClient
		requireErrorContains(t, registry.Register(typedNil), "cannot register nil client")
	})

	tests := []struct {
		name    string
		client  clientkit.RegisteredClient
		wantErr string
	}{
		{name: "invalid name", client: valid("invalid/name"), wantErr: "invalid client name"},
		{name: "name panic", client: &registryClient{namePanics: true}, wantErr: "client name panicked"},
		{name: "missing protocol", client: &registryClient{name: "client", protocolEmpty: true}, wantErr: "protocol is required"},
		{name: "invalid protocol", client: &registryClient{name: "client", protocol: "HTTP/API"}, wantErr: "invalid protocol"},
		{name: "protocol panic", client: &registryClient{name: "client", protocolPanics: true}, wantErr: "client protocol panicked"},
		{name: "invalid readiness policy", client: &registryClient{name: "client", policy: "invalid"}, wantErr: "invalid readiness policy"},
		{name: "readiness policy panic", client: &registryClient{name: "client", policyPanics: true}, wantErr: "client readiness policy panicked"},
		{
			name: "blocking checker disabled",
			client: &configurableRegistryChecker{
				registryClient: &registryClient{name: "client", policy: clientkit.ReadinessRequired},
			},
			wantErr: "requires an enabled health check",
		},
		{
			name: "blocking checker enablement panics",
			client: &configurableRegistryChecker{
				registryClient: &registryClient{name: "client", policy: clientkit.ReadinessDegradedAllowed},
				enabledPanics:  true,
			},
			wantErr: "health check enablement panicked",
		},
		{
			name: "optional checker enablement panics",
			client: &configurableRegistryChecker{
				registryClient: &registryClient{name: "client", policy: clientkit.ReadinessOptional},
				enabledPanics:  true,
			},
			wantErr: "health check enablement panicked",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := clientkit.NewRegistry()
			requireErrorContains(t, registry.Register(test.client), test.wantErr)
			if got := len(registry.Snapshot().Clients); got != 0 {
				t.Fatalf("registry contains %d clients after failed registration, want 0", got)
			}
		})
	}

	t.Run("checker without enablement interface is accepted", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		checker := &registryChecker{registryClient: &registryClient{
			name:   "required-client",
			policy: clientkit.ReadinessRequired,
		}}
		if err := registry.Register(checker); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	})

	t.Run("enabled blocking checker is accepted", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		checker := &configurableRegistryChecker{
			registryClient: &registryClient{name: "required-client", policy: clientkit.ReadinessRequired},
			enabled:        true,
		}
		if err := registry.Register(checker); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		if checker.enabledCalls != 1 {
			t.Fatalf("HealthCheckEnabled() calls = %d, want 1", checker.enabledCalls)
		}
	})

	t.Run("checker enablement is captured once", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		checker := &configurableRegistryChecker{
			registryClient: &registryClient{
				name:   "optional-client",
				policy: clientkit.ReadinessOptional,
				health: clientkit.Health{State: clientkit.HealthHealthy},
			},
			enabled: true,
		}
		if err := registry.Register(checker); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		checker.enabled = false

		summary := registry.CheckAll(context.Background())
		if !summary.Ready || len(summary.Results) != 1 {
			t.Fatalf("CheckAll() = %#v, want captured enabled checker", summary)
		}
		if checker.enabledCalls != 1 {
			t.Fatalf("HealthCheckEnabled() calls = %d after CheckAll, want registration call only", checker.enabledCalls)
		}
	})

	t.Run("captured disabled checker remains excluded", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		checker := &configurableRegistryChecker{
			registryClient: &registryClient{
				name:   "optional-client",
				policy: clientkit.ReadinessOptional,
				health: clientkit.Health{State: clientkit.HealthHealthy},
			},
		}
		if err := registry.Register(checker); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		checker.enabled = true

		summary := registry.CheckAll(context.Background())
		if summary.Ready || summary.State != opskit.StateUnknown || len(summary.Results) != 0 {
			t.Fatalf("CheckAll() = %#v, want no captured disabled checks", summary)
		}
		if checker.enabledCalls != 1 {
			t.Fatalf("HealthCheckEnabled() calls = %d after CheckAll, want registration call only", checker.enabledCalls)
		}
	})

	t.Run("blocking passive client is accepted", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		client := &registryClient{
			name:   "externally-refreshed",
			policy: clientkit.ReadinessRequired,
			health: clientkit.Health{State: clientkit.HealthHealthy},
		}
		if err := registry.Register(client); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	})
}

func TestRegistryRegisterAllIsAtomic(t *testing.T) {
	registry := clientkit.NewRegistry()
	seed := &registryClient{name: "seed"}
	if err := registry.Register(seed); err != nil {
		t.Fatalf("Register(seed) error = %v", err)
	}
	if err := registry.RegisterAll(); err != nil {
		t.Fatalf("RegisterAll() error = %v", err)
	}

	tests := []struct {
		name    string
		clients []clientkit.RegisteredClient
		wantErr string
		absent  string
	}{
		{
			name:    "duplicate within batch",
			clients: []clientkit.RegisteredClient{&registryClient{name: "alpha"}, &registryClient{name: "alpha"}},
			wantErr: "duplicate client",
			absent:  "alpha",
		},
		{
			name:    "conflict with registry",
			clients: []clientkit.RegisteredClient{&registryClient{name: "beta"}, &registryClient{name: "seed"}},
			wantErr: "already registered",
			absent:  "beta",
		},
		{
			name:    "invalid client in batch",
			clients: []clientkit.RegisteredClient{&registryClient{name: "gamma"}, &registryClient{name: "invalid/name"}},
			wantErr: "invalid client name",
			absent:  "gamma",
		},
		{
			name:    "invalid protocol in batch",
			clients: []clientkit.RegisteredClient{&registryClient{name: "delta"}, &registryClient{name: "epsilon", protocolEmpty: true}},
			wantErr: "protocol is required",
			absent:  "delta",
		},
		{
			name: "enablement panic in batch",
			clients: []clientkit.RegisteredClient{
				&registryClient{name: "eta"},
				&configurableRegistryChecker{
					registryClient: &registryClient{name: "theta", policy: clientkit.ReadinessOptional},
					enabledPanics:  true,
				},
			},
			wantErr: "health check enablement panicked",
			absent:  "eta",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireErrorContains(t, registry.RegisterAll(test.clients...), test.wantErr)
			if client, ok := registry.Get(test.absent); ok || client != nil {
				t.Fatalf("Get(%q) = (%v, %t), want absent after atomic failure", test.absent, client, ok)
			}
		})
	}

	alpha := &registryClient{name: "alpha"}
	zulu := &registryClient{name: "zulu"}
	if err := registry.RegisterAll(zulu, alpha); err != nil {
		t.Fatalf("RegisterAll(valid) error = %v", err)
	}
	wantNames := []string{"alpha", "seed", "zulu"}
	gotNames := snapshotNames(registry.Snapshot())
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("Snapshot names = %v, want %v", gotNames, wantNames)
	}
}

func TestRegistryConcurrentRegistration(t *testing.T) {
	registry := clientkit.NewRegistry()
	const clientCount = 32

	errors := make(chan error, clientCount)
	var registrations sync.WaitGroup
	registrations.Add(clientCount)
	for index := range clientCount {
		go func() {
			defer registrations.Done()
			name := fmt.Sprintf("client-%02d", index)
			if err := registry.Register(&registryClient{name: name}); err != nil {
				errors <- err
			}
		}()
	}
	registrations.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("Register() error = %v", err)
	}

	snapshot := registry.Snapshot()
	if len(snapshot.Clients) != clientCount {
		t.Fatalf("Snapshot contains %d clients, want %d", len(snapshot.Clients), clientCount)
	}
	for index, client := range snapshot.Clients {
		wantName := fmt.Sprintf("client-%02d", index)
		if client.Name != wantName {
			t.Fatalf("Snapshot client %d name = %q, want %q", index, client.Name, wantName)
		}
		if client.Protocol != "test" {
			t.Fatalf("Snapshot client %d protocol = %q, want test", index, client.Protocol)
		}
		if got, ok := registry.Get(wantName); !ok || got == nil {
			t.Fatalf("Get(%q) = (%v, %t), want registered client", wantName, got, ok)
		}
	}
}

func TestRegistryCapturesRegistrationMetadata(t *testing.T) {
	registry := clientkit.NewRegistry()
	client := &registryClient{
		name:     "payments",
		protocol: "http",
		policy:   clientkit.ReadinessRequired,
		health:   clientkit.Health{State: clientkit.HealthHealthy, Message: "initial"},
	}
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if client.nameCalls != 1 || client.protocolCalls != 1 || client.policyCalls != 1 {
		t.Fatalf("registration calls = (name %d, protocol %d, policy %d), want (1, 1, 1)", client.nameCalls, client.protocolCalls, client.policyCalls)
	}

	client.name = "changed"
	client.protocol = "tcp"
	client.policy = clientkit.ReadinessOptional
	client.health = clientkit.Health{State: clientkit.HealthDegraded, Message: "current"}

	got, ok := registry.Get("payments")
	if !ok || got != client {
		t.Fatalf("Get(payments) = (%v, %t), want original client", got, ok)
	}
	if _, ok := registry.Get("changed"); ok {
		t.Fatal("Get(changed) found client after mutable implementation changed its name")
	}

	snapshot := registry.Snapshot()
	if len(snapshot.Clients) != 1 {
		t.Fatalf("Snapshot contains %d clients, want 1", len(snapshot.Clients))
	}
	entry := snapshot.Clients[0]
	if entry.Name != "payments" || entry.Protocol != "http" || entry.ReadinessPolicy != clientkit.ReadinessRequired {
		t.Fatalf("captured metadata = (%q, %q, %q), want (%q, %q, %q)", entry.Name, entry.Protocol, entry.ReadinessPolicy, "payments", "http", clientkit.ReadinessRequired)
	}
	if entry.Health.State != clientkit.HealthDegraded || entry.Health.Message != "current" {
		t.Fatalf("Snapshot health = %#v, want current passive health", entry.Health)
	}
	if client.nameCalls != 1 || client.protocolCalls != 1 || client.policyCalls != 1 || client.healthCalls != 1 {
		t.Fatalf("calls after Snapshot = (name %d, protocol %d, policy %d, health %d), want (1, 1, 1, 1)", client.nameCalls, client.protocolCalls, client.policyCalls, client.healthCalls)
	}
}

func TestRegistrySnapshotHealthSafety(t *testing.T) {
	t.Run("health panic is contained", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		client := &registryClient{name: "payments", healthPanics: true}
		if err := registry.Register(client); err != nil {
			t.Fatalf("Register() error = %v", err)
		}

		health := registry.Snapshot().Clients[0].Health
		if health.State != clientkit.HealthUnknown || health.FailureClass != clientkit.FailurePolicy {
			t.Fatalf("health after panic = %#v, want unknown policy failure", health)
		}
		if health.Message != "client health evaluation panicked" {
			t.Fatalf("health message = %q, want stable panic message", health.Message)
		}
	})

	t.Run("custom sanitizer receives captured name", func(t *testing.T) {
		var receivedName string
		registry, err := clientkit.NewRegistryWithConfig(clientkit.RegistryConfig{
			HealthSanitizer: func(name string, health clientkit.Health) clientkit.Health {
				receivedName = name
				health.Message = "sanitized"
				return health
			},
		})
		if err != nil {
			t.Fatalf("NewRegistryWithConfig() error = %v", err)
		}
		client := &registryClient{name: "payments", health: clientkit.Health{State: clientkit.HealthHealthy, Message: "raw"}}
		if err := registry.Register(client); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		client.name = "changed"

		health := registry.Snapshot().Clients[0].Health
		if receivedName != "payments" {
			t.Fatalf("sanitizer name = %q, want captured name %q", receivedName, "payments")
		}
		if health.Message != "sanitized" {
			t.Fatalf("sanitized health message = %q, want %q", health.Message, "sanitized")
		}
	})

	t.Run("sanitizer panic is contained", func(t *testing.T) {
		registry, err := clientkit.NewRegistryWithConfig(clientkit.RegistryConfig{
			HealthSanitizer: func(string, clientkit.Health) clientkit.Health {
				panic("sanitize panic")
			},
		})
		if err != nil {
			t.Fatalf("NewRegistryWithConfig() error = %v", err)
		}
		if err := registry.Register(&registryClient{name: "payments"}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}

		health := registry.Snapshot().Clients[0].Health
		if health.State != clientkit.HealthUnknown || health.FailureClass != clientkit.FailurePolicy {
			t.Fatalf("health after sanitizer panic = %#v, want unknown policy failure", health)
		}
		if health.Message != "client health sanitizer failed" {
			t.Fatalf("health message = %q, want stable sanitizer failure", health.Message)
		}
	})

	t.Run("sanitization can be disabled", func(t *testing.T) {
		registry, err := clientkit.NewRegistryWithConfig(clientkit.RegistryConfig{DisableHealthSanitizer: true})
		if err != nil {
			t.Fatalf("NewRegistryWithConfig() error = %v", err)
		}
		raw := clientkit.Health{
			State:        clientkit.HealthState("custom"),
			FailureClass: clientkit.FailureClass("custom"),
			Duration:     -time.Second,
			Message:      "raw\nmessage",
		}
		if err := registry.Register(&registryClient{name: "payments", health: raw}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		if got := registry.Snapshot().Clients[0].Health; !reflect.DeepEqual(got, raw) {
			t.Fatalf("Snapshot health = %#v, want unsanitized %#v", got, raw)
		}
	})
}

func TestRegistryMustRegister(t *testing.T) {
	registry := clientkit.NewRegistry()
	client := &registryClient{name: "payments"}
	registry.MustRegister(client)

	value := capturePanic(func() {
		registry.MustRegister(client)
	})
	err, ok := value.(error)
	if !ok || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("MustRegister panic = %#v, want duplicate-registration error", value)
	}

	other := clientkit.NewRegistry()
	other.MustRegisterAll(&registryClient{name: "alpha"}, &registryClient{name: "beta"})
	if got := snapshotNames(other.Snapshot()); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("Snapshot names after MustRegisterAll = %v, want [alpha beta]", got)
	}

	invalid := clientkit.NewRegistry()
	value = capturePanic(func() {
		invalid.MustRegisterAll(&registryClient{name: "alpha"}, &registryClient{name: "alpha"})
	})
	err, ok = value.(error)
	if !ok || !strings.Contains(err.Error(), "duplicate client") {
		t.Fatalf("MustRegisterAll panic = %#v, want duplicate-batch error", value)
	}
	if got := len(invalid.Snapshot().Clients); got != 0 {
		t.Fatalf("registry contains %d clients after MustRegisterAll panic, want 0", got)
	}
}

func TestRegistryCloseIdleConnections(t *testing.T) {
	var nilRegistry *clientkit.Registry
	nilRegistry.CloseIdleConnections()

	registry := clientkit.NewRegistry()
	order := make([]string, 0, 3)
	clients := []clientkit.RegisteredClient{
		&closingRegistryClient{
			registryClient: &registryClient{name: "zulu"},
			close:          func() { order = append(order, "zulu") },
		},
		&registryClient{name: "not-closable"},
		&closingRegistryClient{
			registryClient: &registryClient{name: "alpha"},
			close: func() {
				order = append(order, "alpha")
				panic("close panic")
			},
		},
		&closingRegistryClient{
			registryClient: &registryClient{name: "beta"},
			close:          func() { order = append(order, "beta") },
		},
	}
	if err := registry.RegisterAll(clients...); err != nil {
		t.Fatalf("RegisterAll() error = %v", err)
	}

	registry.CloseIdleConnections()
	if want := []string{"alpha", "beta", "zulu"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("close order = %v, want %v", order, want)
	}
	for _, name := range []string{"alpha", "beta", "not-closable", "zulu"} {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("Get(%q) did not find client after idle cleanup", name)
		}
	}
}

func TestRegistryCloseIdleConnectionsUsesMembershipSnapshot(t *testing.T) {
	registry := clientkit.NewRegistry()
	order := make([]string, 0, 3)
	late := &closingRegistryClient{
		registryClient: &registryClient{name: "beta"},
		close:          func() { order = append(order, "beta") },
	}
	var registerOnce sync.Once
	var registerErr error
	first := &closingRegistryClient{
		registryClient: &registryClient{name: "alpha"},
		close: func() {
			order = append(order, "alpha")
			// Reentrant registration proves callbacks run without the registry lock;
			// snapshotting means the new client is not visited until the next call.
			registerOnce.Do(func() {
				registerErr = registry.Register(late)
			})
		},
	}
	if err := registry.Register(first); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	closeIdleConnectionsWithTimeout(t, registry)
	if registerErr != nil {
		t.Fatalf("reentrant Register() error = %v", registerErr)
	}
	if !reflect.DeepEqual(order, []string{"alpha"}) {
		t.Fatalf("first close order = %v, want [alpha]", order)
	}
	if got, ok := registry.Get("beta"); !ok || got != late {
		t.Fatalf("Get(beta) = (%v, %t), want newly registered client", got, ok)
	}

	closeIdleConnectionsWithTimeout(t, registry)
	if want := []string{"alpha", "alpha", "beta"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("second close order = %v, want %v", order, want)
	}
}

func requireErrorContains(t *testing.T, err error, substring string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substring) {
		t.Fatalf("error = %v, want containing %q", err, substring)
	}
}

func snapshotNames(snapshot clientkit.RegistrySnapshot) []string {
	names := make([]string, 0, len(snapshot.Clients))
	for _, client := range snapshot.Clients {
		names = append(names, client.Name)
	}
	return names
}

func capturePanic(fn func()) (value any) {
	defer func() {
		value = recover()
	}()
	fn()
	return nil
}

func closeIdleConnectionsWithTimeout(t *testing.T, registry *clientkit.Registry) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		registry.CloseIdleConnections()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("CloseIdleConnections did not return")
	}
}

var _ clientkit.RegisteredClient = (*registryClient)(nil)
var _ clientkit.HealthChecker = (*registryChecker)(nil)
var _ clientkit.HealthChecker = (*configurableRegistryChecker)(nil)
var _ clientkit.HealthCheckConfigurable = (*configurableRegistryChecker)(nil)
var _ clientkit.IdleConnectionCloser = (*closingRegistryClient)(nil)
