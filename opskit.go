package clientkit

import (
	"context"
	"sync"
	"time"

	"github.com/jaredjakacky/opskit"
)

const (
	defaultComponentName        = "clients"
	defaultComponentKind        = "client_registry"
	defaultComponentDescription = "Clientkit outbound client registry"
)

var (
	_ opskit.Component            = (*Registry)(nil)
	_ opskit.ReadinessContributor = (*Registry)(nil)
	_ opskit.Inspector            = (*Registry)(nil)
	_ opskit.CheckGroup           = (*Registry)(nil)
)

type clientCheckSlot struct {
	result    opskit.NamedCheck
	completed bool
}

// CheckAll concurrently executes enabled client checks with bounded concurrency
// and returns results in deterministic name order. The configured concurrency
// bound applies across overlapping CheckAll calls, and checks for the same
// registered client are serialized. Checkers must honor context cancellation
// cooperatively; a checker panic becomes a stable unhealthy result rather than
// escaping the worker goroutine.
func (r *Registry) CheckAll(ctx context.Context) opskit.CheckSummary {
	startedAt := time.Now().UTC()
	if r == nil {
		return unavailableClientChecks("client registry is missing", startedAt)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	checkers := r.healthCheckersSnapshot()
	if len(checkers) == 0 {
		return unavailableClientChecks("no enabled client checks", startedAt)
	}

	workerCount := r.normalizedMaxConcurrentChecks()
	if workerCount > len(checkers) {
		workerCount = len(checkers)
	}

	jobs := make(chan int)
	slots := make([]clientCheckSlot, len(checkers))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					continue
				}
				entry := checkers[index]
				health := r.sanitizeHealth(entry.name, r.checkHealthSafely(ctx, entry.checker, entry.checkSlot))
				slots[index] = clientCheckSlot{
					result:    opskitNamedClientCheck(entry.name, entry.protocol, entry.policy, health),
					completed: true,
				}
			}
		}()
	}

scheduling:
	for index := range checkers {
		select {
		case <-ctx.Done():
			break scheduling
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()

	results := make([]opskit.NamedCheck, 0, len(checkers))
	for _, slot := range slots {
		if slot.completed {
			results = append(results, slot.result)
		}
	}

	summary := opskit.SummarizeChecks("", startedAt, results)
	if ctx.Err() != nil || len(results) < len(checkers) {
		summary.State = opskit.StateFailed
		summary.Ready = false
		summary.Message = "client checks incomplete"
	}

	return summary
}

func (r *Registry) checkHealthSafely(ctx context.Context, checker HealthChecker, checkSlot chan struct{}) (health Health) {
	if checkSlot != nil {
		select {
		case <-ctx.Done():
			return Health{
				State:        HealthUnknown,
				FailureClass: FailureCanceled,
				CheckedAt:    time.Now().UTC(),
				Message:      "client health check canceled before execution",
			}
		case <-checkSlot:
			defer func() { checkSlot <- struct{}{} }()
		}
	}
	if !r.acquireCheckPermit(ctx) {
		return Health{
			State:        HealthUnknown,
			FailureClass: FailureCanceled,
			CheckedAt:    time.Now().UTC(),
			Message:      "client health check canceled before execution",
		}
	}
	defer r.releaseCheckPermit()
	defer func() {
		if recover() != nil {
			health = Health{
				State:        HealthUnhealthy,
				CheckedAt:    time.Now().UTC(),
				Message:      "client health check panicked",
				FailureClass: FailurePolicy,
			}
		}
	}()
	return checker.Check(ctx)
}

func unavailableClientChecks(message string, startedAt time.Time) opskit.CheckSummary {
	checkedAt := time.Now().UTC()
	return opskit.CheckSummary{
		State:     opskit.StateUnknown,
		Ready:     false,
		Message:   message,
		CheckedAt: &checkedAt,
		Duration:  opskit.NewDuration(checkedAt.Sub(startedAt)),
	}
}

func opskitNamedClientCheck(name, protocol string, policy ReadinessPolicy, health Health) opskit.NamedCheck {
	result := opskit.CheckResult{
		Message:  health.Message,
		Duration: opskit.NewDuration(health.Duration),
		Attributes: []opskit.Attribute{
			opskit.Attr("readiness", string(policy)),
			opskit.Attr("health_state", string(health.State)),
		},
	}
	if !health.CheckedAt.IsZero() {
		checkedAt := health.CheckedAt.UTC()
		result.CheckedAt = &checkedAt
	}
	if health.FailureClass != FailureNone {
		result.Attributes = append(result.Attributes, opskit.Attr("failure_class", string(health.FailureClass)))
	}

	ready := readinessSatisfied(policy, health.State)
	if health.State == HealthHealthy {
		result.State = opskit.StateReady
		result.Ready = true
	} else if health.State == HealthDegraded {
		result.State = opskit.StateDegraded
		result.Ready = ready
	} else if !policy.BlocksReadiness() {
		result.State = opskit.StateDegraded
		result.Ready = true
	} else if health.State == HealthUnhealthy {
		result.State = opskit.StateNotReady
	} else {
		result.State = opskit.StateUnknown
	}

	return opskit.NamedCheck{Name: name, Kind: protocol, Result: result}
}

// ComponentInfo returns the registry's immutable Opskit identity. The returned
// value is cloned so callers cannot mutate registry configuration.
func (r *Registry) ComponentInfo() opskit.ComponentInfo {
	if r == nil {
		return defaultRegistryComponentInfo()
	}
	info := r.componentInfo
	if info.Name == "" {
		info = defaultRegistryComponentInfo()
	}
	return cloneComponentInfo(info)
}

// Status projects passive cached client health into aggregate Opskit status.
// It performs no network I/O.
func (r *Registry) Status(context.Context) opskit.Status {
	snapshot := r.Snapshot()
	if len(snapshot.Clients) == 0 {
		return opskit.UnknownStatus("no clients registered")
	}

	blockingUnhealthy := false
	blockingUnknown := false
	degraded := false
	for _, client := range snapshot.Clients {
		if readinessSatisfied(client.ReadinessPolicy, client.Health.State) {
			if client.Health.State != HealthHealthy {
				degraded = true
			}
			continue
		}
		if client.Health.State == HealthUnknown {
			blockingUnknown = true
		} else {
			blockingUnhealthy = true
		}
	}

	switch {
	case blockingUnhealthy:
		return opskit.NotReadyStatus("one or more readiness-blocking clients are not ready")
	case blockingUnknown:
		return opskit.UnknownStatus("one or more readiness-blocking clients have unknown health")
	case degraded:
		return opskit.DegradedStatus("clients degraded but ready")
	default:
		return opskit.ReadyStatus("clients healthy")
	}
}

// Readiness projects passive cached health and Clientkit readiness policies
// into Opskit readiness items. Informational clients are omitted.
func (r *Registry) Readiness(context.Context) opskit.Readiness {
	snapshot := r.Snapshot()
	if len(snapshot.Clients) == 0 {
		return opskit.Readiness{
			Ready:  false,
			Reason: "no clients registered",
		}
	}

	readiness := opskit.Readiness{
		Ready: true,
		Items: make([]opskit.ReadinessItem, 0, len(snapshot.Clients)),
	}
	blocking := 0

	for _, client := range snapshot.Clients {
		if !participatesInReadiness(client.ReadinessPolicy) {
			continue
		}

		ready := readinessSatisfied(client.ReadinessPolicy, client.Health.State)
		state := opskitReadinessState(client.Health.State)
		impact := opskit.ReadinessImpactBlocking
		if !client.ReadinessPolicy.BlocksReadiness() {
			impact = opskit.ReadinessImpactNonBlocking
			ready = client.Health.State == HealthHealthy
		}
		readiness.Items = append(readiness.Items, opskit.ReadinessItem{
			Name:    client.Name,
			Kind:    client.Protocol,
			Impact:  impact,
			Ready:   ready,
			State:   state,
			Reason:  string(client.ReadinessPolicy),
			Message: client.Health.Message,
		})

		if client.ReadinessPolicy.BlocksReadiness() {
			blocking++
			if !ready {
				readiness.Ready = false
			}
		}
	}

	if blocking == 0 {
		readiness.Ready = true
		readiness.Reason = "no required clients"
		return readiness
	}

	if readiness.Ready {
		readiness.Reason = "all readiness-blocking client policies are satisfied"
	} else {
		readiness.Reason = "one or more readiness-blocking client policies are not satisfied"
	}

	return readiness
}

func opskitReadinessState(state HealthState) opskit.State {
	switch state {
	case HealthHealthy:
		return opskit.StateReady
	case HealthDegraded:
		return opskit.StateDegraded
	case HealthUnhealthy:
		return opskit.StateNotReady
	default:
		return opskit.StateUnknown
	}
}

// Inspect returns a passive registry snapshot and honors cancellation before
// and after collecting external client health.
func (r *Registry) Inspect(ctx context.Context) (opskit.Inspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return opskit.Inspection{}, err
	}

	snapshot := r.Snapshot()
	if err := ctx.Err(); err != nil {
		return opskit.Inspection{}, err
	}

	return opskit.Inspection{
		Details: snapshot,
	}, nil
}
