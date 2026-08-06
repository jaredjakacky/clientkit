// Package healthrecord owns the shared protocol health-recording lifecycle.
package healthrecord

import (
	"context"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/opskit"
)

// Record turns one protocol assessment into cached health and emits its health
// event. A nil client still returns the completed value without side effects.
func Record(client *clientkit.Client, ctx context.Context, protocol string, assessment clientkit.HealthAssessment, startedAt time.Time, attributes []opskit.Attribute) clientkit.Health {
	endedAt := time.Now()
	checkedAt := endedAt.UTC()
	health := clientkit.Health{
		State:        assessment.State,
		FailureClass: assessment.FailureClass,
		CheckedAt:    checkedAt,
		Duration:     endedAt.Sub(startedAt),
		Message:      assessment.Message,
	}
	observer := clientkit.Observer(clientkit.NopObserver{})
	clientName := ""
	if client != nil {
		health = client.UpdateHealth(health)
		observer = client.Observer()
		clientName = client.Name()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	observer.ObserveHealth(ctx, clientkit.HealthEvent{
		Client:       clientName,
		Protocol:     protocol,
		State:        health.State,
		FailureClass: health.FailureClass,
		CheckedAt:    health.CheckedAt,
		Duration:     health.Duration,
		Message:      health.Message,
		Attributes:   attributes,
	})
	return health
}

// ProjectStaleness returns health unchanged while it is current and projects
// unusable timestamps or expired results to unknown without mutating the cache.
func ProjectStaleness(health clientkit.Health, staleAfter time.Duration, checkName string) clientkit.Health {
	if staleAfter <= 0 || health.State == clientkit.HealthUnknown {
		return health
	}

	now := time.Now().UTC()
	if health.CheckedAt.IsZero() {
		return clientkit.Health{
			State:    clientkit.HealthUnknown,
			Duration: health.Duration,
			Message:  checkName + " result has no timestamp",
		}
	}
	if health.CheckedAt.After(now) {
		return clientkit.Health{
			State:     clientkit.HealthUnknown,
			CheckedAt: health.CheckedAt,
			Duration:  health.Duration,
			Message:   checkName + " result timestamp is in the future",
		}
	}
	if now.Sub(health.CheckedAt) <= staleAfter {
		return health
	}
	return clientkit.Health{
		State:     clientkit.HealthUnknown,
		CheckedAt: health.CheckedAt,
		Duration:  health.Duration,
		Message:   checkName + " result is stale",
	}
}
