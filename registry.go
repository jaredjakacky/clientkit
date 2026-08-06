package clientkit

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/jaredjakacky/opskit"
)

const (
	// DefaultMaxConcurrentChecks is the production concurrency bound used by
	// Registry.CheckAll.
	DefaultMaxConcurrentChecks = 4
)

// RegistryConfig configures immutable registry execution behavior.
type RegistryConfig struct {
	// MaxConcurrentChecks bounds simultaneous enabled health checks. Zero uses
	// DefaultMaxConcurrentChecks, and one restores sequential execution.
	MaxConcurrentChecks int
	// ComponentInfo overrides the registry's Opskit identity. Empty fields use
	// Clientkit defaults. The stable kit=clientkit label is always preserved.
	ComponentInfo opskit.ComponentInfo
	// HealthSanitizer completely replaces DefaultHealthSanitizer for registry
	// checks, snapshots, status, readiness, and inspection output.
	HealthSanitizer HealthSanitizer
	// DisableHealthSanitizer disables registry-level health sanitization. It
	// cannot be combined with HealthSanitizer. Registered clients must then
	// provide valid, bounded, non-sensitive health values.
	DisableHealthSanitizer bool
}

// Registry stores clients and executes enabled health checks with an immutable
// concurrency bound. Its zero value is ready to use and applies the production
// default bound.
type Registry struct {
	mu                     sync.RWMutex
	clients                map[string]registeredClientEntry
	maxConcurrentChecks    int
	checkPermitsOnce       sync.Once
	checkPermits           chan struct{}
	componentInfo          opskit.ComponentInfo
	healthSanitizer        HealthSanitizer
	disableHealthSanitizer bool
}

type registeredClientEntry struct {
	client RegisteredClient
	policy ReadinessPolicy
	// checkSlot serializes active checks for this client across overlapping
	// CheckAll calls without holding the registry mutex during user code.
	checkSlot chan struct{}
}

type validatedRegistration struct {
	name   string
	policy ReadinessPolicy
	client RegisteredClient
}

type namedHealthChecker struct {
	name      string
	checker   HealthChecker
	policy    ReadinessPolicy
	checkSlot chan struct{}
}

type namedIdleConnectionCloser struct {
	name   string
	closer IdleConnectionCloser
}

type namedRegisteredClient struct {
	name  string
	entry registeredClientEntry
}

// DefaultRegistryConfig returns the production registry configuration.
func DefaultRegistryConfig() RegistryConfig {
	return RegistryConfig{
		MaxConcurrentChecks: DefaultMaxConcurrentChecks,
		ComponentInfo:       defaultRegistryComponentInfo(),
	}
}

// NewRegistry constructs a registry using DefaultRegistryConfig.
func NewRegistry() *Registry {
	cfg := DefaultRegistryConfig()
	return &Registry{
		clients:                make(map[string]registeredClientEntry),
		maxConcurrentChecks:    cfg.MaxConcurrentChecks,
		componentInfo:          cloneComponentInfo(cfg.ComponentInfo),
		healthSanitizer:        DefaultHealthSanitizer,
		disableHealthSanitizer: cfg.DisableHealthSanitizer,
	}
}

// NewRegistryWithConfig validates cfg and constructs a registry with immutable
// bounded health-check concurrency. Zero uses the production default; one
// restores sequential checking.
func NewRegistryWithConfig(cfg RegistryConfig) (*Registry, error) {
	if cfg.MaxConcurrentChecks < 0 {
		return nil, errors.New("clientkit: registry max concurrent checks must not be negative")
	}

	maxConcurrentChecks := cfg.MaxConcurrentChecks
	if maxConcurrentChecks == 0 {
		maxConcurrentChecks = DefaultMaxConcurrentChecks
	}
	componentInfo := normalizeRegistryComponentInfo(cfg.ComponentInfo)
	if err := opskit.ValidateComponentName(componentInfo.Name); err != nil {
		return nil, fmt.Errorf("clientkit: invalid registry component name %q: %w", componentInfo.Name, err)
	}
	if cfg.DisableHealthSanitizer && cfg.HealthSanitizer != nil {
		return nil, errors.New("clientkit: registry health sanitizer cannot be set and disabled")
	}
	healthSanitizer := cfg.HealthSanitizer
	if !cfg.DisableHealthSanitizer && healthSanitizer == nil {
		healthSanitizer = DefaultHealthSanitizer
	}

	return &Registry{
		clients:                make(map[string]registeredClientEntry),
		maxConcurrentChecks:    maxConcurrentChecks,
		componentInfo:          componentInfo,
		healthSanitizer:        healthSanitizer,
		disableHealthSanitizer: cfg.DisableHealthSanitizer,
	}, nil
}

func (r *Registry) normalizedMaxConcurrentChecks() int {
	if r == nil || r.maxConcurrentChecks <= 0 {
		return DefaultMaxConcurrentChecks
	}
	return r.maxConcurrentChecks
}

func (r *Registry) acquireCheckPermit(ctx context.Context) bool {
	if r == nil {
		return false
	}
	r.checkPermitsOnce.Do(func() {
		r.checkPermits = make(chan struct{}, r.normalizedMaxConcurrentChecks())
	})
	select {
	case r.checkPermits <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *Registry) releaseCheckPermit() {
	<-r.checkPermits
}

func (r *Registry) healthCheckersSnapshot() []namedHealthChecker {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	checkers := make([]namedHealthChecker, 0, len(r.clients))
	for name, entry := range r.clients {
		checker, ok := entry.client.(HealthChecker)
		if !ok {
			continue
		}
		checkers = append(checkers, namedHealthChecker{
			name:      name,
			checker:   checker,
			policy:    entry.policy,
			checkSlot: entry.checkSlot,
		})
	}
	r.mu.RUnlock()

	enabled := checkers[:0]
	for _, entry := range checkers {
		if !healthCheckEnabledSafely(entry.checker) {
			continue
		}
		enabled = append(enabled, entry)
	}
	checkers = enabled

	sort.Slice(checkers, func(i, j int) bool {
		return checkers[i].name < checkers[j].name
	})

	return checkers
}

func (r *Registry) idleConnectionClosersSnapshot() []namedIdleConnectionCloser {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	closers := make([]namedIdleConnectionCloser, 0, len(r.clients))
	for name, entry := range r.clients {
		closer, ok := entry.client.(IdleConnectionCloser)
		if !ok {
			continue
		}
		closers = append(closers, namedIdleConnectionCloser{name: name, closer: closer})
	}
	r.mu.RUnlock()

	sort.Slice(closers, func(i, j int) bool {
		return closers[i].name < closers[j].name
	})

	return closers
}

// CloseIdleConnections synchronously asks every capable registered client to
// release its currently idle reusable connections in deterministic registered
// name order. It snapshots membership under the registry read lock, releases
// the lock before invoking clients, and contains panics from external
// implementations so later clients are still invoked. Active operations are
// not canceled or awaited, clients remain registered and reusable, and future
// requests may establish new connections.
//
// Applications remain responsible for stopping new work and draining active
// operations before cleanup when required. Typical shutdown composition is:
//
//	clients := clientkit.NewRegistry()
//	clients.MustRegister(payments)
//	clients.MustRegister(catalog)
//
//	// Stop accepting new work and wait for active operations first.
//	clients.CloseIdleConnections()
func (r *Registry) CloseIdleConnections() {
	for _, entry := range r.idleConnectionClosersSnapshot() {
		closeIdleConnectionsSafely(entry.closer)
	}
}

func closeIdleConnectionsSafely(closer IdleConnectionCloser) {
	if closer == nil {
		return
	}

	defer func() {
		_ = recover()
	}()
	closer.CloseIdleConnections()
}

// Register validates and adds one client to the registry. Nil interfaces and
// typed-nil pointers are rejected. Registration captures the client's stable
// name and readiness policy once and returns any validation or duplicate error.
// The registry supports static composition; clients cannot be replaced or
// unregistered.
func (r *Registry) Register(client RegisteredClient) error {
	return r.RegisterAll(client)
}

// MustRegister registers one client or panics with the exact registration error
// returned by Register. It is intended for deterministic application
// composition during startup.
func (r *Registry) MustRegister(client RegisteredClient) {
	if err := r.Register(client); err != nil {
		panic(err)
	}
}

// RegisterAll validates and atomically registers clients in argument order.
// Nil interfaces and typed-nil pointers are rejected. Registration metadata is
// captured once per client, and validation or duplicate errors register none
// of the batch. The registry supports static composition; clients cannot be
// replaced or unregistered.
func (r *Registry) RegisterAll(clients ...RegisteredClient) error {
	if r == nil {
		return errors.New("clientkit: registry is required")
	}
	if len(clients) == 0 {
		return nil
	}

	registrations := make([]validatedRegistration, 0, len(clients))
	batchNames := make(map[string]struct{}, len(clients))
	for _, client := range clients {
		registration, err := validateRegistration(client)
		if err != nil {
			return err
		}
		if _, exists := batchNames[registration.name]; exists {
			return fmt.Errorf("clientkit: duplicate client %q in registration batch", registration.name)
		}
		batchNames[registration.name] = struct{}{}
		registrations = append(registrations, registration)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, registration := range registrations {
		if _, exists := r.clients[registration.name]; exists {
			return fmt.Errorf("clientkit: client %q is already registered", registration.name)
		}
	}

	if r.clients == nil {
		r.clients = make(map[string]registeredClientEntry)
	}
	for _, registration := range registrations {
		checkSlot := make(chan struct{}, 1)
		checkSlot <- struct{}{}
		r.clients[registration.name] = registeredClientEntry{
			client:    registration.client,
			policy:    registration.policy,
			checkSlot: checkSlot,
		}
	}

	return nil
}

// MustRegisterAll atomically registers clients or panics with the exact
// registration error returned by RegisterAll. It is intended for static
// application composition during startup.
func (r *Registry) MustRegisterAll(clients ...RegisteredClient) {
	if err := r.RegisterAll(clients...); err != nil {
		panic(err)
	}
}

func validateRegistration(client RegisteredClient) (validatedRegistration, error) {
	if isNilRegisteredClient(client) {
		return validatedRegistration{}, errors.New("clientkit: cannot register nil client")
	}

	name, err := registeredClientName(client)
	if err != nil {
		return validatedRegistration{}, err
	}
	if err := ValidateClientName(name); err != nil {
		return validatedRegistration{}, err
	}

	policy, err := registeredClientReadinessPolicy(client)
	if err != nil {
		return validatedRegistration{}, err
	}
	if err := validateReadinessPolicy(policy); err != nil {
		return validatedRegistration{}, fmt.Errorf("clientkit: client %q: %w", name, err)
	}
	policy = normalizeReadinessPolicy(policy)
	if checker, ok := client.(HealthChecker); ok && policy.BlocksReadiness() && !healthCheckEnabledSafely(checker) {
		return validatedRegistration{}, fmt.Errorf("clientkit: client %q readiness policy requires an enabled health check", name)
	}

	return validatedRegistration{name: name, policy: policy, client: client}, nil
}

func registeredClientName(client RegisteredClient) (name string, err error) {
	defer func() {
		if recover() != nil {
			name = ""
			err = errors.New("clientkit: client name panicked")
		}
	}()

	return client.Name(), nil
}

func registeredClientReadinessPolicy(client RegisteredClient) (policy ReadinessPolicy, err error) {
	defer func() {
		if recover() != nil {
			policy = ""
			err = errors.New("clientkit: client readiness policy panicked")
		}
	}()

	return client.ReadinessPolicy(), nil
}

func healthCheckEnabledSafely(checker HealthChecker) (enabled bool) {
	configurable, ok := checker.(HealthCheckConfigurable)
	if !ok {
		return true
	}
	defer func() {
		if recover() != nil {
			enabled = false
		}
	}()
	return configurable.HealthCheckEnabled()
}

func isNilRegisteredClient(client RegisteredClient) bool {
	if client == nil {
		return true
	}

	value := reflect.ValueOf(client)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

// Get returns the registered client identified by name. The returned client
// remains owned by its creator and may be used concurrently according to its
// contract.
func (r *Registry) Get(name string) (RegisteredClient, bool) {
	if r == nil {
		return nil, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.clients[name]
	if !ok {
		return nil, false
	}
	return entry.client, true
}

// Snapshot returns registered clients in deterministic name order using
// passive health reads and the registry's sanitization policy. It never
// executes health checks.
func (r *Registry) Snapshot() RegistrySnapshot {
	if r == nil {
		return RegistrySnapshot{}
	}

	r.mu.RLock()
	entries := make([]namedRegisteredClient, 0, len(r.clients))
	for name, entry := range r.clients {
		entries = append(entries, namedRegisteredClient{name: name, entry: entry})
	}
	r.mu.RUnlock()

	clients := make([]ClientSnapshot, 0, len(entries))
	for _, namedClient := range entries {
		clients = append(clients, ClientSnapshot{
			Name:            namedClient.name,
			ReadinessPolicy: namedClient.entry.policy,
			Health:          r.sanitizeHealth(namedClient.name, registeredClientHealth(namedClient.entry.client)),
		})
	}

	sort.Slice(clients, func(i, j int) bool {
		return clients[i].Name < clients[j].Name
	})

	return RegistrySnapshot{
		Clients: clients,
	}
}

func (r *Registry) sanitizeHealth(name string, health Health) (sanitized Health) {
	if r == nil {
		return DefaultHealthSanitizer(name, health)
	}
	return sanitizeHealthSafely(name, health, r.healthSanitizer, r.disableHealthSanitizer)
}

func registeredClientHealth(client RegisteredClient) (health Health) {
	defer func() {
		if recover() != nil {
			health = Health{
				State:        HealthUnknown,
				FailureClass: FailurePolicy,
				Message:      "client health evaluation panicked",
			}
		}
	}()
	return client.Health()
}

func defaultRegistryComponentInfo() opskit.ComponentInfo {
	return opskit.ComponentInfo{
		Name:        defaultComponentName,
		Kind:        defaultComponentKind,
		Description: defaultComponentDescription,
		Labels:      []opskit.Attribute{opskit.Attr("kit", "clientkit")},
	}
}

func normalizeRegistryComponentInfo(info opskit.ComponentInfo) opskit.ComponentInfo {
	normalized := defaultRegistryComponentInfo()
	if info.Name != "" {
		normalized.Name = info.Name
	}
	if info.Kind != "" {
		normalized.Kind = info.Kind
	}
	if info.Description != "" {
		normalized.Description = info.Description
	}
	if len(info.Labels) > 0 {
		normalized.Labels = append([]opskit.Attribute(nil), info.Labels...)
		for index := len(normalized.Labels) - 1; index >= 0; index-- {
			if normalized.Labels[index].Key == "kit" {
				normalized.Labels = append(normalized.Labels[:index], normalized.Labels[index+1:]...)
			}
		}
		normalized.Labels = append(normalized.Labels, opskit.Attr("kit", "clientkit"))
	}
	return normalized
}

func cloneComponentInfo(info opskit.ComponentInfo) opskit.ComponentInfo {
	info.Labels = append([]opskit.Attribute(nil), info.Labels...)
	return info
}
