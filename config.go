package clientkit

import "errors"

// Config defines the protocol-neutral identity, readiness, observation, and
// health-sanitization policy embedded by Clientkit protocol configurations.
type Config struct {
	// Name is the stable, telemetry-safe logical name of the outbound client.
	Name string
	// ReadinessPolicy controls how the client contributes to aggregate
	// readiness. Its zero value is ReadinessOptional.
	ReadinessPolicy ReadinessPolicy
	// Observer completely replaces any protocol-client default observer when it
	// is non-nil. A nil observer lets built-in protocol clients install their
	// default OpenTelemetry observer; clientkit.New itself uses a no-op observer.
	// Use NopObserver to disable observation or MultiObserver to compose observers
	// explicitly.
	Observer Observer
	// HealthSanitizer completely replaces DefaultHealthSanitizer for cached
	// health and health telemetry emitted by built-in protocol clients.
	HealthSanitizer HealthSanitizer
	// DisableHealthSanitizer disables client-level health sanitization. It cannot
	// be combined with HealthSanitizer. The caller then owns validation,
	// cardinality, redaction, and message-size safety.
	DisableHealthSanitizer bool
}

// Validate checks shared client configuration without constructing observers or
// performing protocol-specific work.
func (cfg Config) Validate() error {
	if err := ValidateClientName(cfg.Name); err != nil {
		return err
	}
	if err := validateReadinessPolicy(cfg.ReadinessPolicy); err != nil {
		return err
	}
	if cfg.DisableHealthSanitizer && cfg.HealthSanitizer != nil {
		return errors.New("clientkit: health sanitizer cannot be set and disabled")
	}
	return nil
}
