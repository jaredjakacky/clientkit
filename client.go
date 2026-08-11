package clientkit

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// MaxClientNameBytes is the maximum byte length accepted by ValidateClientName.
const MaxClientNameBytes = 64

// Client stores immutable shared client policy and concurrency-safe cached
// health. It must be constructed with New. Protocol implementations may compose
// it to implement RegisteredClient and manage health-recording lifecycle. It is
// protocol-neutral and therefore is not itself a complete RegisteredClient.
type Client struct {
	name                   string
	readinessPolicy        ReadinessPolicy
	observer               Observer
	healthSanitizer        HealthSanitizer
	disableHealthSanitizer bool

	healthMu sync.RWMutex
	health   Health
}

// New validates and constructs a protocol-neutral Client without performing
// network I/O. Protocol users will normally call a protocol package's New
// function instead.
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	name := cfg.Name

	policy := normalizeReadinessPolicy(cfg.ReadinessPolicy)
	healthSanitizer := cfg.HealthSanitizer
	if !cfg.DisableHealthSanitizer && healthSanitizer == nil {
		healthSanitizer = DefaultHealthSanitizer
	}

	return &Client{
		name:                   name,
		readinessPolicy:        policy,
		observer:               SafeObserver(cfg.Observer),
		healthSanitizer:        healthSanitizer,
		disableHealthSanitizer: cfg.DisableHealthSanitizer,
		health:                 Health{State: HealthUnknown},
	}, nil
}

// Name returns the immutable logical client name.
func (c *Client) Name() string {
	return c.name
}

// ReadinessPolicy returns the immutable normalized readiness policy.
func (c *Client) ReadinessPolicy() ReadinessPolicy {
	return c.readinessPolicy
}

// Observer returns the client's safe backend-neutral observer.
func (c *Client) Observer() Observer {
	if c == nil || c.observer == nil {
		return NopObserver{}
	}
	return c.observer
}

// ValidateClientName verifies a stable, path-safe, telemetry-safe logical
// client identifier. Names must contain 1 through MaxClientNameBytes lowercase
// ASCII letters, digits, periods, underscores, or hyphens; must begin and end
// with a letter or digit; and must not contain consecutive periods.
func ValidateClientName(name string) error {
	if name == "" {
		return errors.New("clientkit: name is required")
	}
	if strings.TrimSpace(name) != name {
		return errors.New("clientkit: name must not include surrounding whitespace")
	}
	if len(name) > MaxClientNameBytes {
		return fmt.Errorf("clientkit: name exceeds %d bytes", MaxClientNameBytes)
	}
	if strings.Contains(name, "..") || !clientNameAlphanumeric(name[0]) || !clientNameAlphanumeric(name[len(name)-1]) {
		return fmt.Errorf("clientkit: invalid client name %q", name)
	}
	for index := 0; index < len(name); index++ {
		value := name[index]
		if clientNameAlphanumeric(value) || value == '-' || value == '_' || value == '.' {
			continue
		}
		return fmt.Errorf("clientkit: invalid client name %q", name)
	}
	return nil
}

func clientNameAlphanumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

// Health returns the most recently cached health value without performing I/O.
func (c *Client) Health() Health {
	c.healthMu.RLock()
	defer c.healthMu.RUnlock()

	return c.health
}

// UpdateHealth applies the configured sanitization policy, caches the resulting
// health, and returns the cached value. Callers emitting health through
// telemetry should use the returned value.
func (c *Client) UpdateHealth(health Health) Health {
	health = sanitizeHealthSafely(c.name, health, c.healthSanitizer, c.disableHealthSanitizer)
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	c.health = health
	return health
}
