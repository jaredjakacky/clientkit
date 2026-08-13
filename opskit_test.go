package clientkit_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/opskit"
)

type operationalClient struct {
	name     string
	protocol string
	policy   clientkit.ReadinessPolicy
	enabled  bool
	check    func(context.Context) clientkit.Health

	mu     sync.RWMutex
	health clientkit.Health
}

func (c *operationalClient) Name() string {
	return c.name
}

func (c *operationalClient) Protocol() string {
	if c.protocol == "" {
		return "test"
	}
	return c.protocol
}

func (c *operationalClient) ReadinessPolicy() clientkit.ReadinessPolicy {
	return c.policy
}

func (c *operationalClient) Health() clientkit.Health {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.health
}

func (c *operationalClient) Check(ctx context.Context) clientkit.Health {
	health := c.Health()
	if c.check != nil {
		health = c.check(ctx)
	}
	c.mu.Lock()
	c.health = health
	c.mu.Unlock()
	return health
}

func (c *operationalClient) HealthCheckEnabled() bool {
	return c.enabled
}

func TestRegistryComponentInfo(t *testing.T) {
	var nilRegistry *clientkit.Registry
	defaultInfo := nilRegistry.ComponentInfo()
	if defaultInfo.Name != "clients" || defaultInfo.Kind != "client_registry" {
		t.Fatalf("nil ComponentInfo() = %#v, want Clientkit defaults", defaultInfo)
	}
}

func TestRegistryStatusProjection(t *testing.T) {
	tests := []struct {
		name      string
		clients   []*operationalClient
		wantState opskit.State
		wantReady bool
	}{
		{name: "empty", wantState: opskit.StateUnknown},
		{
			name:      "required healthy",
			clients:   []*operationalClient{{name: "required", policy: clientkit.ReadinessRequired, health: clientkit.Health{State: clientkit.HealthHealthy}, enabled: true}},
			wantState: opskit.StateReady,
			wantReady: true,
		},
		{
			name:      "required unhealthy",
			clients:   []*operationalClient{{name: "required", policy: clientkit.ReadinessRequired, health: clientkit.Health{State: clientkit.HealthUnhealthy}, enabled: true}},
			wantState: opskit.StateNotReady,
		},
		{
			name:      "required unknown",
			clients:   []*operationalClient{{name: "required", policy: clientkit.ReadinessRequired, health: clientkit.Health{State: clientkit.HealthUnknown}, enabled: true}},
			wantState: opskit.StateUnknown,
		},
		{
			name:      "degraded allowed",
			clients:   []*operationalClient{{name: "degraded", policy: clientkit.ReadinessDegradedAllowed, health: clientkit.Health{State: clientkit.HealthDegraded}, enabled: true}},
			wantState: opskit.StateDegraded,
			wantReady: true,
		},
		{
			name:      "optional unhealthy",
			clients:   []*operationalClient{{name: "optional", policy: clientkit.ReadinessOptional, health: clientkit.Health{State: clientkit.HealthUnhealthy}}},
			wantState: opskit.StateDegraded,
			wantReady: true,
		},
		{
			name:      "informational unhealthy",
			clients:   []*operationalClient{{name: "info", policy: clientkit.ReadinessInformational, health: clientkit.Health{State: clientkit.HealthUnhealthy}}},
			wantState: opskit.StateDegraded,
			wantReady: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := clientkit.NewRegistry()
			for _, client := range test.clients {
				if err := registry.Register(client); err != nil {
					t.Fatalf("Register(%q) error = %v", client.name, err)
				}
			}
			status := registry.Status(context.Background())
			if status.State != test.wantState || status.Ready != test.wantReady {
				t.Fatalf("Status() = %#v, want state %q ready %t", status, test.wantState, test.wantReady)
			}
		})
	}
}

func TestRegistryReadinessProjection(t *testing.T) {
	registry := clientkit.NewRegistry()
	clients := []*operationalClient{
		{name: "degraded", protocol: "tcp", policy: clientkit.ReadinessDegradedAllowed, health: clientkit.Health{State: clientkit.HealthDegraded, Message: "reduced"}, enabled: true},
		{name: "informational", policy: clientkit.ReadinessInformational, health: clientkit.Health{State: clientkit.HealthUnhealthy}},
		{name: "optional", protocol: "http", policy: clientkit.ReadinessOptional, health: clientkit.Health{State: clientkit.HealthUnhealthy, Message: "offline"}},
		{name: "required", protocol: "tcp", policy: clientkit.ReadinessRequired, health: clientkit.Health{State: clientkit.HealthHealthy}, enabled: true},
	}
	for _, client := range clients {
		if err := registry.Register(client); err != nil {
			t.Fatalf("Register(%q) error = %v", client.name, err)
		}
	}
	for _, client := range clients {
		client.protocol = "changed"
	}

	readiness := registry.Readiness(context.Background())
	if !readiness.Ready {
		t.Fatalf("Readiness().Ready = false, want true: %#v", readiness)
	}
	if len(readiness.Items) != 3 {
		t.Fatalf("Readiness items = %d, want 3 with informational omitted", len(readiness.Items))
	}
	wantNames := []string{"degraded", "optional", "required"}
	gotNames := make([]string, 0, len(readiness.Items))
	for _, item := range readiness.Items {
		gotNames = append(gotNames, item.Name)
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("Readiness item names = %v, want %v", gotNames, wantNames)
	}
	if !readiness.Items[0].Ready || readiness.Items[0].State != opskit.StateDegraded || readiness.Items[0].Impact != opskit.ReadinessImpactBlocking {
		t.Fatalf("degraded item = %#v, want satisfied blocking item", readiness.Items[0])
	}
	if readiness.Items[0].Kind != "tcp" || readiness.Items[1].Kind != "http" || readiness.Items[2].Kind != "tcp" {
		t.Fatalf("readiness kinds = (%q, %q, %q), want captured protocols", readiness.Items[0].Kind, readiness.Items[1].Kind, readiness.Items[2].Kind)
	}
	if readiness.Items[1].Ready || readiness.Items[1].Impact != opskit.ReadinessImpactNonBlocking {
		t.Fatalf("optional item = %#v, want non-blocking unhealthy detail", readiness.Items[1])
	}

	clients[3].mu.Lock()
	clients[3].health = clientkit.Health{State: clientkit.HealthUnhealthy}
	clients[3].mu.Unlock()
	if got := registry.Readiness(context.Background()); got.Ready {
		t.Fatalf("Readiness().Ready = true with required unhealthy client: %#v", got)
	}
}

func TestRegistryReadinessWithoutBlockingClients(t *testing.T) {
	empty := clientkit.NewRegistry().Readiness(context.Background())
	if empty.Ready || empty.Reason != "no clients registered" {
		t.Fatalf("empty Readiness() = %#v, want not ready", empty)
	}

	registry := clientkit.NewRegistry()
	if err := registry.Register(&operationalClient{name: "optional", policy: clientkit.ReadinessOptional, health: clientkit.Health{State: clientkit.HealthUnhealthy}}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	readiness := registry.Readiness(context.Background())
	if !readiness.Ready || readiness.Reason != "no required clients" {
		t.Fatalf("optional-only Readiness() = %#v, want aggregate ready", readiness)
	}
}

func TestRegistryInspect(t *testing.T) {
	registry := clientkit.NewRegistry()
	checkCalls := 0
	client := &operationalClient{
		name:     "payments",
		protocol: "http",
		enabled:  true,
		health:   clientkit.Health{State: clientkit.HealthHealthy},
		check: func(context.Context) clientkit.Health {
			checkCalls++
			return clientkit.Health{State: clientkit.HealthHealthy}
		},
	}
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	inspection, err := registry.Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	snapshot, ok := inspection.Details.(clientkit.RegistrySnapshot)
	if !ok || len(snapshot.Clients) != 1 || snapshot.Clients[0].Name != "payments" || snapshot.Clients[0].Protocol != "http" {
		t.Fatalf("Inspect().Details = %#v, want RegistrySnapshot", inspection.Details)
	}
	if checkCalls != 0 {
		t.Fatalf("Inspect() ran %d active health checks, want none", checkCalls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.Inspect(ctx); err != context.Canceled {
		t.Fatalf("Inspect(canceled) error = %v, want context.Canceled", err)
	}
}

func TestRegistryCheckAll(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		var nilRegistry *clientkit.Registry
		tests := []struct {
			name    string
			summary opskit.CheckSummary
		}{
			{name: "nil", summary: nilRegistry.CheckAll(context.Background())},
			{name: "empty", summary: clientkit.NewRegistry().CheckAll(context.Background())},
			{name: "disabled", summary: checkAllWithDisabledClient(t)},
		}
		for _, test := range tests {
			if test.summary.Ready || test.summary.State != opskit.StateUnknown || test.summary.Message == "" {
				t.Errorf("%s CheckAll() = %#v, want unavailable", test.name, test.summary)
			}
		}
	})

	t.Run("results are sorted and policy aware", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		checkedAt := time.Now().UTC()
		clients := []*operationalClient{
			{
				name: "zulu", protocol: "tcp", policy: clientkit.ReadinessOptional, enabled: true,
				check: func(context.Context) clientkit.Health {
					return clientkit.Health{State: clientkit.HealthUnhealthy, FailureClass: clientkit.FailureRemoteResponse, CheckedAt: checkedAt, Message: "offline"}
				},
			},
			{
				name: "alpha", protocol: "http", policy: clientkit.ReadinessRequired, enabled: true,
				check: func(context.Context) clientkit.Health {
					return clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: checkedAt, Message: "ready"}
				},
			},
		}
		for _, client := range clients {
			if err := registry.Register(client); err != nil {
				t.Fatalf("Register(%q) error = %v", client.name, err)
			}
		}
		for _, client := range clients {
			client.protocol = "changed"
		}

		summary := registry.CheckAll(context.Background())
		if !summary.Ready || summary.State != opskit.StateDegraded {
			t.Fatalf("CheckAll() = %#v, want ready degraded summary", summary)
		}
		if len(summary.Results) != 2 || summary.Results[0].Name != "alpha" || summary.Results[1].Name != "zulu" {
			t.Fatalf("CheckAll results = %#v, want alpha then zulu", summary.Results)
		}
		if summary.Results[0].Kind != "http" || summary.Results[1].Kind != "tcp" {
			t.Fatalf("CheckAll result kinds = (%q, %q), want captured protocols", summary.Results[0].Kind, summary.Results[1].Kind)
		}
		if summary.Results[1].Result.Ready != true || summary.Results[1].Result.State != opskit.StateDegraded {
			t.Fatalf("optional unhealthy result = %#v, want ready degraded", summary.Results[1])
		}
		if got := operationalAttribute(summary.Results[1].Result.Attributes, "failure_class"); got != string(clientkit.FailureRemoteResponse) {
			t.Fatalf("failure_class = %q, want %q", got, clientkit.FailureRemoteResponse)
		}
	})

	t.Run("checker panic is contained", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		checkCalls := 0
		client := &operationalClient{
			name: "payments", policy: clientkit.ReadinessRequired, enabled: true,
			health: clientkit.Health{
				State:     clientkit.HealthHealthy,
				CheckedAt: time.Now().UTC().Add(-time.Minute),
				Message:   "previously healthy",
			},
			check: func(context.Context) clientkit.Health {
				checkCalls++
				panic("check")
			},
		}
		if err := registry.Register(client); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		summary := registry.CheckAll(context.Background())
		if summary.Ready || len(summary.Results) != 1 || summary.Results[0].Result.State != opskit.StateNotReady {
			t.Fatalf("CheckAll() = %#v, want contained not-ready failure", summary)
		}
		if summary.Results[0].Result.Message != "client health check panicked" {
			t.Fatalf("panic message = %q, want stable message", summary.Results[0].Result.Message)
		}
		assertRegistryPassiveHealth(t, registry, clientkit.HealthUnhealthy, clientkit.FailurePolicy, "client health check panicked", opskit.StateNotReady, false)
		if checkCalls != 1 {
			t.Fatalf("active checks after passive projections = %d, want 1", checkCalls)
		}
	})

	t.Run("canceled before scheduling", func(t *testing.T) {
		registry := clientkit.NewRegistry()
		called := false
		client := &operationalClient{
			name: "payments", policy: clientkit.ReadinessRequired, enabled: true,
			check: func(context.Context) clientkit.Health {
				called = true
				return clientkit.Health{State: clientkit.HealthHealthy}
			},
		}
		if err := registry.Register(client); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		summary := registry.CheckAll(ctx)
		if summary.State != opskit.StateFailed || summary.Ready || summary.Message != "client checks incomplete" {
			t.Fatalf("CheckAll(canceled) = %#v, want incomplete failure", summary)
		}
		if called {
			t.Fatal("checker ran after cancellation")
		}
	})
}

func TestRegistryCheckAllProjectsSanitizedHealth(t *testing.T) {
	checkedAt := time.Date(2026, time.August, 6, 10, 30, 0, 0, time.FixedZone("offset", 2*60*60))
	const duration = 125 * time.Millisecond
	tests := []struct {
		name            string
		policy          clientkit.ReadinessPolicy
		state           clientkit.HealthState
		failure         clientkit.FailureClass
		wantState       opskit.State
		wantReady       bool
		wantHealthState clientkit.HealthState
		wantFailure     clientkit.FailureClass
		wantMessage     string
	}{
		{
			name: "required healthy clears failure", policy: clientkit.ReadinessRequired,
			state: clientkit.HealthHealthy, failure: clientkit.FailureTransport,
			wantState: opskit.StateReady, wantReady: true, wantHealthState: clientkit.HealthHealthy,
			wantMessage: "assessment",
		},
		{
			name: "required degraded is unsatisfied", policy: clientkit.ReadinessRequired,
			state:     clientkit.HealthDegraded,
			wantState: opskit.StateDegraded, wantHealthState: clientkit.HealthDegraded,
			wantMessage: "assessment",
		},
		{
			name: "degraded allowed is satisfied", policy: clientkit.ReadinessDegradedAllowed,
			state:     clientkit.HealthDegraded,
			wantState: opskit.StateDegraded, wantReady: true, wantHealthState: clientkit.HealthDegraded,
			wantMessage: "assessment",
		},
		{
			name: "required unknown", policy: clientkit.ReadinessRequired,
			state:     clientkit.HealthUnknown,
			wantState: opskit.StateUnknown, wantHealthState: clientkit.HealthUnknown,
			wantMessage: "assessment",
		},
		{
			name: "required unhealthy", policy: clientkit.ReadinessRequired,
			state: clientkit.HealthUnhealthy, failure: clientkit.FailureRemoteResponse,
			wantState: opskit.StateNotReady, wantHealthState: clientkit.HealthUnhealthy,
			wantFailure: clientkit.FailureRemoteResponse, wantMessage: "assessment",
		},
		{
			name: "optional unhealthy remains ready", policy: clientkit.ReadinessOptional,
			state: clientkit.HealthUnhealthy, failure: clientkit.FailureRemoteResponse,
			wantState: opskit.StateDegraded, wantReady: true, wantHealthState: clientkit.HealthUnhealthy,
			wantFailure: clientkit.FailureRemoteResponse, wantMessage: "assessment",
		},
		{
			name: "informational unhealthy remains ready", policy: clientkit.ReadinessInformational,
			state: clientkit.HealthUnhealthy, failure: clientkit.FailureRemoteResponse,
			wantState: opskit.StateDegraded, wantReady: true, wantHealthState: clientkit.HealthUnhealthy,
			wantFailure: clientkit.FailureRemoteResponse, wantMessage: "assessment",
		},
		{
			name: "invalid health is sanitized", policy: clientkit.ReadinessOptional,
			state: "custom", failure: "custom",
			wantState: opskit.StateDegraded, wantReady: true, wantHealthState: clientkit.HealthUnknown,
			wantFailure: clientkit.FailurePolicy, wantMessage: "client health state unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := clientkit.NewRegistry()
			client := &operationalClient{
				name: "payments", policy: test.policy, enabled: true,
				check: func(context.Context) clientkit.Health {
					return clientkit.Health{
						State: test.state, FailureClass: test.failure, CheckedAt: checkedAt,
						Duration: duration, Message: "assessment",
					}
				},
			}
			if err := registry.Register(client); err != nil {
				t.Fatalf("Register() error = %v", err)
			}

			summary := registry.CheckAll(context.Background())
			if len(summary.Results) != 1 {
				t.Fatalf("CheckAll results = %d, want 1", len(summary.Results))
			}
			result := summary.Results[0].Result
			if result.State != test.wantState || result.Ready != test.wantReady || result.Message != test.wantMessage {
				t.Fatalf("check result = %#v, want state %q ready %t message %q", result, test.wantState, test.wantReady, test.wantMessage)
			}
			if result.CheckedAt == nil || !result.CheckedAt.Equal(checkedAt) || result.CheckedAt.Location() != time.UTC {
				t.Fatalf("CheckedAt = %v, want same instant in UTC", result.CheckedAt)
			}
			if got := result.Duration.TimeDuration(); got != duration {
				t.Fatalf("Duration = %v, want %v", got, duration)
			}
			if got := operationalAttribute(result.Attributes, "readiness"); got != string(test.policy) {
				t.Fatalf("readiness attribute = %q, want %q", got, test.policy)
			}
			if got := operationalAttribute(result.Attributes, "health_state"); got != string(test.wantHealthState) {
				t.Fatalf("health_state attribute = %q, want %q", got, test.wantHealthState)
			}
			failure, hasFailure := findOperationalAttribute(result.Attributes, "failure_class")
			if test.wantFailure == clientkit.FailureNone {
				if hasFailure {
					t.Fatalf("failure_class attribute = %q, want omitted", failure)
				}
			} else if !hasFailure || failure != string(test.wantFailure) {
				t.Fatalf("failure_class attribute = (%q, %t), want %q", failure, hasFailure, test.wantFailure)
			}
		})
	}
}

func TestRegistryCheckAllHonorsConcurrencyBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry, err := clientkit.NewRegistryWithConfig(clientkit.RegistryConfig{MaxConcurrentChecks: 2})
		if err != nil {
			t.Fatalf("NewRegistryWithConfig() error = %v", err)
		}

		entered := make(chan struct{}, 4)
		release := make(chan struct{})
		for index := range 4 {
			client := &operationalClient{
				name:    "client-" + string(rune('a'+index)),
				policy:  clientkit.ReadinessRequired,
				enabled: true,
				check: func(context.Context) clientkit.Health {
					entered <- struct{}{}
					<-release
					return clientkit.Health{State: clientkit.HealthHealthy}
				},
			}
			if err := registry.Register(client); err != nil {
				t.Fatalf("Register(%q) error = %v", client.name, err)
			}
		}

		done := make(chan opskit.CheckSummary, 1)
		go func() { done <- registry.CheckAll(context.Background()) }()
		synctest.Wait()
		if got := len(entered); got != 2 {
			t.Fatalf("active health checks = %d, want configured bound 2", got)
		}

		close(release)
		synctest.Wait()
		summary := <-done
		if !summary.Ready || len(summary.Results) != 4 {
			t.Fatalf("CheckAll() = %#v, want four ready results", summary)
		}
	})
}

func TestRegistryCheckAllSerializesOverlappingChecksPerClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := clientkit.NewRegistry()
		entered := make(chan int, 2)
		releaseFirst := make(chan struct{})
		var releaseOnce sync.Once
		release := func() {
			releaseOnce.Do(func() { close(releaseFirst) })
		}
		defer release()

		var stateMu sync.Mutex
		checkCalls := 0
		activeChecks := 0
		maxActiveChecks := 0
		client := &operationalClient{
			name:    "payments",
			policy:  clientkit.ReadinessOptional,
			enabled: true,
			check: func(context.Context) clientkit.Health {
				stateMu.Lock()
				checkCalls++
				call := checkCalls
				activeChecks++
				if activeChecks > maxActiveChecks {
					maxActiveChecks = activeChecks
				}
				stateMu.Unlock()

				entered <- call
				if call == 1 {
					<-releaseFirst
				}

				stateMu.Lock()
				activeChecks--
				stateMu.Unlock()
				return clientkit.Health{State: clientkit.HealthHealthy}
			},
		}
		if err := registry.Register(client); err != nil {
			t.Fatalf("Register() error = %v", err)
		}

		summaries := make(chan opskit.CheckSummary, 2)
		go func() { summaries <- registry.CheckAll(context.Background()) }()
		synctest.Wait()
		if call := <-entered; call != 1 {
			t.Fatalf("first entered check = %d, want 1", call)
		}

		go func() { summaries <- registry.CheckAll(context.Background()) }()
		synctest.Wait()
		if got := len(entered); got != 0 {
			t.Fatalf("second active check entered before first completed: %d entries", got)
		}

		release()
		synctest.Wait()
		if call := <-entered; call != 2 {
			t.Fatalf("second entered check = %d, want 2", call)
		}

		for range 2 {
			summary := <-summaries
			if !summary.Ready || len(summary.Results) != 1 {
				t.Fatalf("CheckAll() = %#v, want one ready result", summary)
			}
		}

		stateMu.Lock()
		defer stateMu.Unlock()
		if checkCalls != 2 || maxActiveChecks != 1 {
			t.Fatalf("check calls = %d, maximum active = %d; want 2 calls serialized", checkCalls, maxActiveChecks)
		}
	})
}

func TestRegistryCheckAllDoesNotClearNewerSyntheticHealthWithOlderAcceptedResult(t *testing.T) {
	sanitizerEntered := make(chan struct{}, 1)
	releaseSanitizer := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseSanitizer) })
	}
	defer release()

	registry, err := clientkit.NewRegistryWithConfig(clientkit.RegistryConfig{
		HealthSanitizer: func(_ string, health clientkit.Health) clientkit.Health {
			sanitizerEntered <- struct{}{}
			<-releaseSanitizer
			return health
		},
	})
	if err != nil {
		t.Fatalf("NewRegistryWithConfig() error = %v", err)
	}
	client := &operationalClient{
		name: "payments", policy: clientkit.ReadinessRequired, enabled: true,
		health: clientkit.Health{
			State:     clientkit.HealthHealthy,
			CheckedAt: time.Now().UTC().Add(-time.Minute),
			Message:   "previously healthy",
		},
		check: func(context.Context) clientkit.Health {
			return clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC(), Message: "checker completed"}
		},
	}
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	firstDone := make(chan opskit.CheckSummary, 1)
	go func() { firstDone <- registry.CheckAll(context.Background()) }()
	select {
	case <-sanitizerEntered:
	case <-time.After(time.Second):
		t.Fatal("first CheckAll did not enter registry sanitizer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	secondDone := make(chan opskit.CheckSummary, 1)
	go func() { secondDone <- registry.CheckAll(ctx) }()
	select {
	case summary := <-secondDone:
		if summary.State != opskit.StateFailed || len(summary.Results) != 1 {
			t.Fatalf("second CheckAll() = %#v, want failed serialized-wait result", summary)
		}
		if got := operationalAttribute(summary.Results[0].Result.Attributes, "failure_class"); got != string(clientkit.FailureTimeout) {
			t.Fatalf("second failure_class = %q, want %q", got, clientkit.FailureTimeout)
		}
	case <-time.After(time.Second):
		t.Fatal("second CheckAll did not honor its deadline while waiting for result publication")
	}

	release()
	select {
	case summary := <-firstDone:
		if !summary.Ready || len(summary.Results) != 1 {
			t.Fatalf("first CheckAll() = %#v, want accepted healthy result", summary)
		}
	case <-time.After(time.Second):
		t.Fatal("first CheckAll did not finish after releasing sanitizer")
	}

	projected := registry.Snapshot().Clients[0].Health
	if projected.State != clientkit.HealthUnknown || projected.FailureClass != clientkit.FailureTimeout {
		t.Fatalf("Snapshot() health = %#v, want newer wait-timeout override", projected)
	}
}

func TestRegistryCheckAllSharesConcurrencyBoundAcrossCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry, err := clientkit.NewRegistryWithConfig(clientkit.RegistryConfig{MaxConcurrentChecks: 1})
		if err != nil {
			t.Fatalf("NewRegistryWithConfig() error = %v", err)
		}

		entered := make(chan string, 4)
		release := make(chan struct{})
		first := &operationalClient{
			name: "first", policy: clientkit.ReadinessOptional, enabled: true,
			check: func(context.Context) clientkit.Health {
				entered <- "first"
				return clientkit.Health{State: clientkit.HealthHealthy}
			},
		}
		second := &operationalClient{
			name: "second", policy: clientkit.ReadinessOptional, enabled: true,
			check: func(context.Context) clientkit.Health {
				entered <- "second"
				<-release
				return clientkit.Health{State: clientkit.HealthHealthy}
			},
		}
		if err := registry.RegisterAll(first, second); err != nil {
			t.Fatalf("RegisterAll() error = %v", err)
		}

		done := make(chan opskit.CheckSummary, 2)
		go func() { done <- registry.CheckAll(context.Background()) }()
		synctest.Wait()
		if got := <-entered; got != "first" {
			t.Fatalf("first entered check = %q, want first", got)
		}
		if got := <-entered; got != "second" {
			t.Fatalf("second entered check = %q, want second", got)
		}

		// The first call has advanced to second and holds the global permit. Its
		// first-client slot is free, so only the registry-wide bound can prevent
		// the overlapping call from entering first concurrently.
		go func() { done <- registry.CheckAll(context.Background()) }()
		synctest.Wait()
		if got := len(entered); got != 0 {
			t.Fatalf("overlapping check bypassed global concurrency bound: %d entries", got)
		}

		close(release)
		synctest.Wait()
		if got := <-entered; got != "first" {
			t.Fatalf("overlapping first check = %q, want first", got)
		}
		if got := <-entered; got != "second" {
			t.Fatalf("overlapping second check = %q, want second", got)
		}
		for range 2 {
			if summary := <-done; !summary.Ready || len(summary.Results) != 2 {
				t.Fatalf("CheckAll() = %#v, want two ready results", summary)
			}
		}
	})
}

func TestRegistryCheckAllClassifiesDeadlineWhileWaitingForGlobalPermit(t *testing.T) {
	registry, err := clientkit.NewRegistryWithConfig(clientkit.RegistryConfig{MaxConcurrentChecks: 1})
	if err != nil {
		t.Fatalf("NewRegistryWithConfig() error = %v", err)
	}

	entered := make(chan string, 2)
	releaseSecond := make(chan struct{})
	first := &operationalClient{
		name: "first", policy: clientkit.ReadinessOptional, enabled: true,
		health: clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC().Add(-time.Minute)},
		check: func(context.Context) clientkit.Health {
			entered <- "first"
			return clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC(), Message: "first refreshed"}
		},
	}
	second := &operationalClient{
		name: "second", policy: clientkit.ReadinessOptional, enabled: true,
		health: clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC().Add(-time.Minute)},
		check: func(context.Context) clientkit.Health {
			entered <- "second"
			<-releaseSecond
			return clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC(), Message: "second refreshed"}
		},
	}
	if err := registry.RegisterAll(first, second); err != nil {
		t.Fatalf("RegisterAll() error = %v", err)
	}

	firstDone := make(chan opskit.CheckSummary, 1)
	go func() { firstDone <- registry.CheckAll(context.Background()) }()
	for _, want := range []string{"first", "second"} {
		select {
		case got := <-entered:
			if got != want {
				t.Fatalf("entered check = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s check did not start", want)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	secondDone := make(chan opskit.CheckSummary, 1)
	go func() { secondDone <- registry.CheckAll(ctx) }()

	select {
	case summary := <-secondDone:
		if summary.State != opskit.StateFailed || summary.Ready || len(summary.Results) != 1 {
			t.Fatalf("deadline CheckAll() = %#v, want one failed result", summary)
		}
		result := summary.Results[0]
		if result.Name != "first" || operationalAttribute(result.Result.Attributes, "failure_class") != string(clientkit.FailureTimeout) {
			t.Fatalf("deadline result = %#v, want first classified timeout", result)
		}
		if result.Result.Message != "client health check timed out before execution" {
			t.Fatalf("deadline message = %q, want pre-execution timeout", result.Result.Message)
		}
		projected := registry.Snapshot().Clients[0].Health
		if projected.State != clientkit.HealthUnknown || projected.FailureClass != clientkit.FailureTimeout {
			t.Fatalf("Snapshot() first-client health = %#v, want timeout override over prior healthy cache", projected)
		}
	case <-time.After(time.Second):
		t.Fatal("overlapping CheckAll did not honor its deadline while waiting for the global permit")
	}

	close(releaseSecond)
	select {
	case summary := <-firstDone:
		if !summary.Ready || len(summary.Results) != 2 {
			t.Fatalf("first CheckAll() = %#v, want two completed checks", summary)
		}
	case <-time.After(time.Second):
		t.Fatal("first CheckAll did not finish after release")
	}
}

func TestRegistryCheckAllRejectsLateCheckerResults(t *testing.T) {
	tests := []struct {
		name        string
		deadline    bool
		wantFailure clientkit.FailureClass
		wantMessage string
	}{
		{
			name:        "parent cancellation",
			wantFailure: clientkit.FailureCanceled,
			wantMessage: "client health check canceled before result acceptance",
		},
		{
			name:        "deadline",
			deadline:    true,
			wantFailure: clientkit.FailureTimeout,
			wantMessage: "client health check timed out before result acceptance",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := clientkit.NewRegistry()
			checkerContext := make(chan context.Context, 1)
			release := make(chan struct{})
			client := &operationalClient{
				name: "payments", policy: clientkit.ReadinessRequired, enabled: true,
				health: clientkit.Health{
					State:     clientkit.HealthHealthy,
					CheckedAt: time.Now().UTC().Add(-time.Minute),
					Message:   "previously healthy",
				},
				check: func(ctx context.Context) clientkit.Health {
					checkerContext <- ctx
					<-release
					return clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC(), Message: "late healthy result"}
				},
			}
			if err := registry.Register(client); err != nil {
				t.Fatalf("Register() error = %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			if test.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
			}
			defer cancel()
			results := make(chan opskit.CheckSummary, 1)
			go func() { results <- registry.CheckAll(ctx) }()

			var callbackContext context.Context
			select {
			case callbackContext = <-checkerContext:
			case <-time.After(time.Second):
				t.Fatal("health checker was not invoked")
			}
			if !test.deadline {
				cancel()
			}
			select {
			case <-callbackContext.Done():
			case <-time.After(time.Second):
				t.Fatal("health checker context did not complete")
			}
			close(release)

			var summary opskit.CheckSummary
			select {
			case summary = <-results:
			case <-time.After(time.Second):
				t.Fatal("CheckAll did not return after releasing checker")
			}
			if summary.State != opskit.StateFailed || summary.Ready || len(summary.Results) != 1 {
				t.Fatalf("CheckAll() = %#v, want failed late-result rejection", summary)
			}
			result := summary.Results[0].Result
			if result.State != opskit.StateUnknown || result.Ready || result.Message != test.wantMessage {
				t.Fatalf("late check result = %#v, want unknown not-ready %q", result, test.wantMessage)
			}
			if got := operationalAttribute(result.Attributes, "failure_class"); got != string(test.wantFailure) {
				t.Fatalf("failure_class = %q, want %q", got, test.wantFailure)
			}
			if cached := client.Health(); cached.State != clientkit.HealthHealthy || cached.Message != "late healthy result" {
				t.Fatalf("client Health() = %#v, want persisted late checker result", cached)
			}
			assertRegistryPassiveHealth(t, registry, clientkit.HealthUnknown, test.wantFailure, test.wantMessage, opskit.StateUnknown, false)
		})
	}
}

func TestRegistryCheckAllRejectsResultWhenSanitizerCrossesCancellation(t *testing.T) {
	sanitizerEntered := make(chan struct{}, 1)
	releaseSanitizer := make(chan struct{})
	registry, err := clientkit.NewRegistryWithConfig(clientkit.RegistryConfig{
		HealthSanitizer: func(_ string, health clientkit.Health) clientkit.Health {
			sanitizerEntered <- struct{}{}
			<-releaseSanitizer
			health.State = clientkit.HealthHealthy
			health.FailureClass = clientkit.FailureNone
			health.Message = "late sanitized success"
			return health
		},
	})
	if err != nil {
		t.Fatalf("NewRegistryWithConfig() error = %v", err)
	}
	client := &operationalClient{
		name: "payments", policy: clientkit.ReadinessRequired, enabled: true,
		check: func(context.Context) clientkit.Health {
			return clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC(), Message: "checker completed"}
		},
	}
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan opskit.CheckSummary, 1)
	go func() { results <- registry.CheckAll(ctx) }()
	select {
	case <-sanitizerEntered:
	case <-time.After(time.Second):
		t.Fatal("registry sanitizer was not invoked")
	}
	cancel()
	close(releaseSanitizer)

	select {
	case summary := <-results:
		if summary.State != opskit.StateFailed || summary.Ready || len(summary.Results) != 1 {
			t.Fatalf("CheckAll() = %#v, want failed sanitizer result rejection", summary)
		}
		result := summary.Results[0].Result
		if result.State != opskit.StateUnknown || result.Ready || result.Message != "client health check canceled before result acceptance" {
			t.Fatalf("sanitizer result = %#v, want canceled rejection", result)
		}
		if got := operationalAttribute(result.Attributes, "failure_class"); got != string(clientkit.FailureCanceled) {
			t.Fatalf("failure_class = %q, want %q", got, clientkit.FailureCanceled)
		}
	case <-time.After(time.Second):
		t.Fatal("CheckAll did not return after releasing sanitizer")
	}
	if cached := client.Health(); cached.State != clientkit.HealthHealthy || cached.Message != "checker completed" {
		t.Fatalf("client Health() = %#v, want persisted checker result", cached)
	}
	assertRegistryPassiveHealth(t, registry, clientkit.HealthUnknown, clientkit.FailureCanceled, "client health check canceled before result acceptance", opskit.StateUnknown, false)
}

func TestRegistryCheckAllRetainsTransientSanitizerFailure(t *testing.T) {
	sanitizerCalls := 0
	registry, err := clientkit.NewRegistryWithConfig(clientkit.RegistryConfig{
		HealthSanitizer: func(_ string, health clientkit.Health) clientkit.Health {
			sanitizerCalls++
			if sanitizerCalls == 1 {
				panic("sanitize")
			}
			return health
		},
	})
	if err != nil {
		t.Fatalf("NewRegistryWithConfig() error = %v", err)
	}
	client := &operationalClient{
		name: "payments", policy: clientkit.ReadinessRequired, enabled: true,
		health: clientkit.Health{
			State:     clientkit.HealthHealthy,
			CheckedAt: time.Now().UTC().Add(-time.Minute),
			Message:   "previously healthy",
		},
		check: func(context.Context) clientkit.Health {
			return clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC(), Message: "checker completed"}
		},
	}
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	summary := registry.CheckAll(context.Background())
	if summary.Ready || len(summary.Results) != 1 {
		t.Fatalf("CheckAll() = %#v, want one failed sanitizer result", summary)
	}
	result := summary.Results[0].Result
	if result.State != opskit.StateUnknown || result.Message != "client health sanitizer failed" {
		t.Fatalf("sanitizer result = %#v, want unknown policy failure", result)
	}
	if cached := client.Health(); cached.State != clientkit.HealthHealthy || cached.Message != "checker completed" {
		t.Fatalf("client Health() = %#v, want persisted checker result", cached)
	}
	assertRegistryPassiveHealth(t, registry, clientkit.HealthUnknown, clientkit.FailurePolicy, "client health sanitizer failed", opskit.StateUnknown, false)
	if sanitizerCalls != 1 {
		t.Fatalf("sanitizer calls after passive projections = %d, want stored sanitized override without re-sanitization", sanitizerCalls)
	}
}

func TestRegistrySyntheticHealthIsSupersededByLaterDirectCheck(t *testing.T) {
	registry := clientkit.NewRegistry()
	panicCheck := true
	client := &operationalClient{
		name: "payments", policy: clientkit.ReadinessRequired, enabled: true,
		health: clientkit.Health{
			State:     clientkit.HealthHealthy,
			CheckedAt: time.Now().UTC().Add(-time.Minute),
			Message:   "previously healthy",
		},
		check: func(context.Context) clientkit.Health {
			if panicCheck {
				panic("check")
			}
			return clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC(), Message: "direct refresh succeeded"}
		},
	}
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	summary := registry.CheckAll(context.Background())
	if summary.Ready || len(summary.Results) != 1 || summary.Results[0].Result.CheckedAt == nil {
		t.Fatalf("CheckAll() = %#v, want timestamped synthetic failure", summary)
	}
	assertRegistryPassiveHealth(t, registry, clientkit.HealthUnhealthy, clientkit.FailurePolicy, "client health check panicked", opskit.StateNotReady, false)

	panicCheck = false
	refreshed := client.Check(context.Background())
	if !refreshed.CheckedAt.After(*summary.Results[0].Result.CheckedAt) {
		t.Fatalf("direct check timestamp = %v, want after override %v", refreshed.CheckedAt, *summary.Results[0].Result.CheckedAt)
	}
	assertRegistryPassiveHealth(t, registry, clientkit.HealthHealthy, clientkit.FailureNone, "direct refresh succeeded", opskit.StateReady, true)
}

func TestRegistrySyntheticHealthIsClearedByLaterAcceptedCheckAll(t *testing.T) {
	registry := clientkit.NewRegistry()
	panicCheck := true
	client := &operationalClient{
		name: "payments", policy: clientkit.ReadinessRequired, enabled: true,
		health: clientkit.Health{
			State:     clientkit.HealthHealthy,
			CheckedAt: time.Now().UTC().Add(-time.Minute),
			Message:   "previously healthy",
		},
		check: func(context.Context) clientkit.Health {
			if panicCheck {
				panic("check")
			}
			return clientkit.Health{State: clientkit.HealthHealthy, Message: "registry refresh succeeded"}
		},
	}
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if summary := registry.CheckAll(context.Background()); summary.Ready {
		t.Fatalf("first CheckAll() = %#v, want synthetic failure", summary)
	}
	panicCheck = false
	if summary := registry.CheckAll(context.Background()); !summary.Ready {
		t.Fatalf("second CheckAll() = %#v, want accepted healthy result", summary)
	}

	// The zero CheckedAt cannot supersede by timestamp, so healthy passive state
	// proves the accepted Registry check explicitly cleared the older override.
	assertRegistryPassiveHealth(t, registry, clientkit.HealthHealthy, clientkit.FailureNone, "registry refresh succeeded", opskit.StateReady, true)
}

func TestRegistryCheckAllCompletionWinsBeforeLaterCancellation(t *testing.T) {
	registry := clientkit.NewRegistry()
	client := &operationalClient{
		name: "payments", policy: clientkit.ReadinessRequired, enabled: true,
		check: func(context.Context) clientkit.Health {
			return clientkit.Health{State: clientkit.HealthHealthy, Message: "completed"}
		},
	}
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	summary := registry.CheckAll(ctx)
	if !summary.Ready || summary.State != opskit.StateReady || len(summary.Results) != 1 {
		t.Fatalf("CheckAll() = %#v, want completed ready result", summary)
	}
	cancel()
	if result := summary.Results[0].Result; !result.Ready || result.State != opskit.StateReady || result.Message != "completed" {
		t.Fatalf("completed result after later cancellation = %#v", result)
	}
}

func TestRegistryCheckAllCancelsWhileWaitingForClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := clientkit.NewRegistry()
		releaseFirst := make(chan struct{})
		var releaseOnce sync.Once
		release := func() {
			releaseOnce.Do(func() { close(releaseFirst) })
		}
		defer release()

		var callsMu sync.Mutex
		checkCalls := 0
		client := &operationalClient{
			name:    "payments",
			policy:  clientkit.ReadinessOptional,
			enabled: true,
			health: clientkit.Health{
				State:     clientkit.HealthHealthy,
				CheckedAt: time.Now().UTC().Add(-time.Minute),
				Message:   "previously healthy",
			},
			check: func(context.Context) clientkit.Health {
				callsMu.Lock()
				checkCalls++
				callsMu.Unlock()
				<-releaseFirst
				return clientkit.Health{State: clientkit.HealthHealthy, CheckedAt: time.Now().UTC(), Message: "in-flight refresh succeeded"}
			},
		}
		if err := registry.Register(client); err != nil {
			t.Fatalf("Register() error = %v", err)
		}

		firstDone := make(chan opskit.CheckSummary, 1)
		go func() { firstDone <- registry.CheckAll(context.Background()) }()
		synctest.Wait()

		ctx, cancel := context.WithCancel(context.Background())
		secondDone := make(chan opskit.CheckSummary, 1)
		go func() { secondDone <- registry.CheckAll(ctx) }()
		synctest.Wait()
		callsMu.Lock()
		gotCalls := checkCalls
		callsMu.Unlock()
		if gotCalls != 1 {
			t.Fatalf("check calls before cancellation = %d, want 1", gotCalls)
		}

		cancel()
		synctest.Wait()
		canceled := <-secondDone
		if canceled.Ready || canceled.State != opskit.StateFailed || len(canceled.Results) != 1 {
			t.Fatalf("canceled CheckAll() = %#v, want one incomplete failed result", canceled)
		}
		if got := operationalAttribute(canceled.Results[0].Result.Attributes, "failure_class"); got != string(clientkit.FailureCanceled) {
			t.Fatalf("failure_class = %q, want %q", got, clientkit.FailureCanceled)
		}
		projected := registry.Snapshot().Clients[0].Health
		if projected.State != clientkit.HealthUnknown || projected.FailureClass != clientkit.FailureCanceled {
			t.Fatalf("Snapshot() health while first check is active = %#v, want canceled synthetic override", projected)
		}

		release()
		synctest.Wait()
		if summary := <-firstDone; !summary.Ready {
			t.Fatalf("first CheckAll() = %#v, want ready after release", summary)
		}
		projected = registry.Snapshot().Clients[0].Health
		if projected.State != clientkit.HealthHealthy || projected.Message != "in-flight refresh succeeded" {
			t.Fatalf("Snapshot() health after later completed check = %#v, want client-owned success", projected)
		}
	})
}

func assertRegistryPassiveHealth(
	t *testing.T,
	registry *clientkit.Registry,
	wantHealthState clientkit.HealthState,
	wantFailure clientkit.FailureClass,
	wantMessage string,
	wantStatusState opskit.State,
	wantReady bool,
) {
	t.Helper()

	assertHealth := func(surface string, health clientkit.Health) {
		t.Helper()
		if health.State != wantHealthState || health.FailureClass != wantFailure || health.Message != wantMessage {
			t.Fatalf("%s health = %#v, want state %q failure %q message %q", surface, health, wantHealthState, wantFailure, wantMessage)
		}
	}

	snapshot := registry.Snapshot()
	if len(snapshot.Clients) != 1 {
		t.Fatalf("Snapshot clients = %d, want 1", len(snapshot.Clients))
	}
	assertHealth("Snapshot", snapshot.Clients[0].Health)

	status := registry.Status(context.Background())
	if status.State != wantStatusState || status.Ready != wantReady {
		t.Fatalf("Status() = %#v, want state %q ready %t", status, wantStatusState, wantReady)
	}

	readiness := registry.Readiness(context.Background())
	if readiness.Ready != wantReady || len(readiness.Items) != 1 {
		t.Fatalf("Readiness() = %#v, want ready %t with one item", readiness, wantReady)
	}
	if readiness.Items[0].Ready != wantReady || readiness.Items[0].State != wantStatusState {
		t.Fatalf("Readiness item = %#v, want state %q ready %t", readiness.Items[0], wantStatusState, wantReady)
	}

	inspection, err := registry.Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	inspected, ok := inspection.Details.(clientkit.RegistrySnapshot)
	if !ok || len(inspected.Clients) != 1 {
		t.Fatalf("Inspect().Details = %#v, want one-client RegistrySnapshot", inspection.Details)
	}
	assertHealth("Inspect", inspected.Clients[0].Health)
}

func checkAllWithDisabledClient(t *testing.T) opskit.CheckSummary {
	t.Helper()
	registry := clientkit.NewRegistry()
	client := &operationalClient{name: "disabled", enabled: false}
	if err := registry.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return registry.CheckAll(context.Background())
}

func operationalAttribute(attributes []opskit.Attribute, key string) string {
	value, _ := findOperationalAttribute(attributes, key)
	return value
}

func findOperationalAttribute(attributes []opskit.Attribute, key string) (string, bool) {
	for _, attribute := range attributes {
		if attribute.Key == key {
			return attribute.Value, true
		}
	}
	return "", false
}

var _ clientkit.HealthChecker = (*operationalClient)(nil)
var _ clientkit.HealthCheckConfigurable = (*operationalClient)(nil)
