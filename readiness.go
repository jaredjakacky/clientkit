package clientkit

import "fmt"

// ReadinessPolicy describes how one outbound client affects the Clientkit
// registry's aggregate readiness. It is deliberately separate from Opskit's
// component-registration policy: this policy belongs to the client domain.
type ReadinessPolicy string

const (
	// ReadinessRequired requires healthy client state.
	ReadinessRequired ReadinessPolicy = "required"
	// ReadinessOptional includes the client as an optional readiness component
	// without allowing it to block aggregate readiness. It is the production-safe
	// zero-value default.
	ReadinessOptional ReadinessPolicy = "optional"
	// ReadinessDegradedAllowed accepts healthy or degraded client state, but not
	// unknown or unhealthy state.
	ReadinessDegradedAllowed ReadinessPolicy = "degraded_allowed"
	// ReadinessInformational keeps client health visible through status, checks,
	// snapshots, and inspection, but omits the client from readiness components
	// and aggregate readiness decisions.
	ReadinessInformational ReadinessPolicy = "informational"
)

func normalizeReadinessPolicy(policy ReadinessPolicy) ReadinessPolicy {
	if policy == "" {
		return ReadinessOptional
	}
	return policy
}

func validateReadinessPolicy(policy ReadinessPolicy) error {
	switch normalizeReadinessPolicy(policy) {
	case ReadinessRequired, ReadinessOptional, ReadinessDegradedAllowed, ReadinessInformational:
		return nil
	default:
		return fmt.Errorf("clientkit: invalid readiness policy %q", policy)
	}
}

// BlocksReadiness reports whether unsatisfied client health prevents aggregate
// readiness and therefore requires an active health strategy when used by a
// built-in protocol client.
func (policy ReadinessPolicy) BlocksReadiness() bool {
	switch normalizeReadinessPolicy(policy) {
	case ReadinessRequired, ReadinessDegradedAllowed:
		return true
	default:
		return false
	}
}

func participatesInReadiness(policy ReadinessPolicy) bool {
	return normalizeReadinessPolicy(policy) != ReadinessInformational
}

func readinessSatisfied(policy ReadinessPolicy, state HealthState) bool {
	switch normalizeReadinessPolicy(policy) {
	case ReadinessOptional, ReadinessInformational:
		return true
	case ReadinessRequired:
		return state == HealthHealthy
	case ReadinessDegradedAllowed:
		return state == HealthHealthy || state == HealthDegraded
	default:
		return false
	}
}
