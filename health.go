package clientkit

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// DefaultMaxHealthMessageBytes is the byte limit applied to health text by
// DefaultHealthSanitizer.
const DefaultMaxHealthMessageBytes = 256

// HealthState is the bounded operational state of an outbound dependency.
type HealthState string

const (
	// HealthUnknown means no current, trustworthy assessment is available.
	HealthUnknown HealthState = "unknown"
	// HealthHealthy means the dependency satisfies its configured health policy.
	HealthHealthy HealthState = "healthy"
	// HealthDegraded means the dependency is usable with reduced capability.
	HealthDegraded HealthState = "degraded"
	// HealthUnhealthy means the dependency does not satisfy its health policy.
	HealthUnhealthy HealthState = "unhealthy"
)

// Health is one dependency-health observation suitable for caching and
// operational inspection.
type Health struct {
	// State is the bounded health decision.
	State HealthState `json:"state"`
	// FailureClass is the stable classified cause of a non-healthy result. It
	// supplements health state and never contains a raw error.
	FailureClass FailureClass `json:"failure_class,omitempty"`
	// CheckedAt is the UTC completion time of the active assessment.
	CheckedAt time.Time `json:"checked_at,omitempty"`
	// Duration is the complete assessment duration.
	Duration time.Duration `json:"duration,omitempty"`
	// Message is bounded operational context and must not contain secrets.
	Message string `json:"message,omitempty"`
}

// HealthAssessment is a protocol-level health decision without lifecycle
// metadata. Clientkit owns check timestamps, durations, sanitization, caching,
// and telemetry around the assessment.
type HealthAssessment struct {
	// State is the protocol-level health decision.
	State HealthState `json:"state"`
	// FailureClass is the stable cause when the assessment represents failure.
	FailureClass FailureClass `json:"failure_class,omitempty"`
	// Message is protocol-supplied operational context and must not contain
	// secrets.
	Message string `json:"message,omitempty"`
}

// HealthSanitizer transforms client health before it reaches telemetry,
// registry checks, readiness, status, or inspection surfaces. Custom
// implementations are synchronous: they must be concurrency-safe, return
// quickly without performing I/O, and produce bounded, non-sensitive output.
// Clientkit contains sanitizer panics as unknown policy failures.
type HealthSanitizer func(clientName string, health Health) Health

// DefaultHealthSanitizer enforces valid states, UTC timestamps, non-negative
// durations, and bounded single-line operational text. Control and Unicode
// formatting characters are removed. It cannot detect secrets; custom clients
// remain responsible for not returning sensitive text.
func DefaultHealthSanitizer(_ string, health Health) Health {
	switch health.State {
	case HealthUnknown, HealthHealthy, HealthDegraded, HealthUnhealthy:
	default:
		health.State = HealthUnknown
		health.FailureClass = FailurePolicy
		health.Message = "client health state unavailable"
	}
	if health.State == HealthHealthy {
		health.FailureClass = FailureNone
	} else if !validFailureClass(health.FailureClass) {
		health.FailureClass = FailurePolicy
	}
	if !health.CheckedAt.IsZero() {
		health.CheckedAt = health.CheckedAt.UTC()
	}
	if health.Duration < 0 {
		health.Duration = 0
	}
	health.Message = boundedHealthMessage(health.Message)
	return health
}

func validFailureClass(class FailureClass) bool {
	switch class {
	case FailureNone,
		FailureConfiguration,
		FailurePolicy,
		FailureRequest,
		FailureCanceled,
		FailureTimeout,
		FailureNameResolution,
		FailureConnectionRefused,
		FailureConnectionReset,
		FailureConnectionClosed,
		FailureTLS,
		FailureRemoteResponse,
		FailureTransport:
		return true
	default:
		return false
	}
}

func sanitizeHealthSafely(name string, health Health, sanitizer HealthSanitizer, disabled bool) (sanitized Health) {
	sanitized, _ = sanitizeHealthSafelyWithPanic(name, health, sanitizer, disabled)
	return sanitized
}

func sanitizeHealthSafelyWithPanic(name string, health Health, sanitizer HealthSanitizer, disabled bool) (sanitized Health, panicked bool) {
	if sanitizer == nil && !disabled {
		sanitizer = DefaultHealthSanitizer
	}
	if sanitizer == nil {
		return health, false
	}
	defer func() {
		if recover() != nil {
			sanitized = Health{
				State:        HealthUnknown,
				FailureClass: FailurePolicy,
				Message:      "client health sanitizer failed",
			}
			panicked = true
		}
	}()
	return sanitizer(name, health), false
}

func boundedHealthMessage(message string) string {
	if len(message) <= DefaultMaxHealthMessageBytes {
		message = strings.Map(sanitizeHealthMessageRune, message)
		for len(message) > DefaultMaxHealthMessageBytes {
			_, size := utf8.DecodeLastRuneInString(message)
			if size <= 0 {
				return ""
			}
			message = message[:len(message)-size]
		}
		return message
	}

	var bounded strings.Builder
	bounded.Grow(DefaultMaxHealthMessageBytes)
	for _, value := range message {
		value = sanitizeHealthMessageRune(value)
		if value < 0 {
			continue
		}
		size := utf8.RuneLen(value)
		if size <= 0 || bounded.Len()+size > DefaultMaxHealthMessageBytes {
			break
		}
		bounded.WriteRune(value)
	}
	return bounded.String()
}

func sanitizeHealthMessageRune(value rune) rune {
	if unicode.IsControl(value) || unicode.Is(unicode.Cf, value) || unicode.Is(unicode.Zl, value) || unicode.Is(unicode.Zp, value) {
		return -1
	}
	return value
}

// IsHealthy reports whether the state is exactly HealthHealthy.
func (h Health) IsHealthy() bool {
	return h.State == HealthHealthy
}

// IsReady reports whether this health state satisfies policy. Informational
// policy is always satisfied; Registry omits informational clients from
// readiness entirely.
func (h Health) IsReady(policy ReadinessPolicy) bool {
	return readinessSatisfied(policy, h.State)
}
