package tcpclient

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/internal/configvalue"
	"github.com/jaredjakacky/clientkit/internal/healthrecord"
)

func normalizeCheckConfig(cfg CheckConfig) (normalizedCheckConfig, error) {
	if !cfg.Enabled {
		if cfg.Timeout != 0 || cfg.DisableTimeout || cfg.StaleAfter != 0 || cfg.DisableStaleAfter || cfg.Probe != nil {
			return normalizedCheckConfig{}, errors.New("clientkit: TCP health check configuration requires Check.Enabled")
		}
		return normalizedCheckConfig{}, nil
	}
	timeout, err := configvalue.Duration("TCP check timeout", cfg.Timeout, cfg.DisableTimeout, DefaultCheckTimeout, 0)
	if err != nil {
		return normalizedCheckConfig{}, err
	}
	staleAfter, err := configvalue.Duration("TCP check stale-after duration", cfg.StaleAfter, cfg.DisableStaleAfter, DefaultCheckStaleAfter, 0)
	if err != nil {
		return normalizedCheckConfig{}, err
	}

	if fn, ok := cfg.Probe.(ConnectionProbeFunc); ok && fn == nil {
		return normalizedCheckConfig{}, errors.New("clientkit: TCP health probe must not be nil")
	}

	return normalizedCheckConfig{enabled: true, timeout: timeout, staleAfter: staleAfter, probe: cfg.Probe}, nil
}

// HealthCheckEnabled reports whether active connect-and-close checks are
// enabled for this client.
func (c *Client) HealthCheckEnabled() bool {
	return c != nil && c.Client != nil && c.check.enabled
}

// Check performs one enabled health check and updates cached health. It always
// closes a successfully established connection after the optional Probe.
// Disabled checks return unknown without dialing or changing the cache.
func (c *Client) Check(ctx context.Context) clientkit.Health {
	startedAt := time.Now()
	if c == nil || c.Client == nil || (c.dialContext == nil && c.dialer == nil) {
		return c.completeCheckHealth(ctx, clientkit.HealthUnhealthy, clientkit.FailureConfiguration, "TCP client is not configured", startedAt, "")
	}
	if !c.check.enabled {
		return clientkit.Health{State: clientkit.HealthUnknown, Message: "TCP health check is disabled"}
	}
	if ctx == nil {
		return c.completeCheckHealth(ctx, clientkit.HealthUnhealthy, clientkit.FailureRequest, "TCP health check context is required", startedAt, "")
	}

	checkContext := ctx
	cancel := func() {}
	if c.check.timeout > 0 {
		checkContext, cancel = context.WithTimeout(checkContext, c.check.timeout)
	}
	defer cancel()
	result, metadata := c.dialObserved(checkContext)

	state := clientkit.HealthUnhealthy
	failureClass := result.FailureClass
	message := "TCP connection failed"
	switch {
	case result.Outcome == OutcomeSuccess && c.tls.enabled:
		state = clientkit.HealthHealthy
		failureClass = clientkit.FailureNone
		message = "TLS connection established"
	case result.Outcome == OutcomeSuccess:
		state = clientkit.HealthHealthy
		failureClass = clientkit.FailureNone
		message = "TCP connection established"
	case result.Outcome == OutcomeTimeout && metadata.tlsHandshakeFailed:
		message = "TLS handshake timed out"
	case result.Outcome == OutcomeCanceled && metadata.tlsHandshakeFailed:
		message = "TLS handshake canceled"
	case result.Outcome == OutcomeTLSError:
		message = "TLS handshake failed"
	case result.Outcome == OutcomeTimeout:
		message = "TCP connection timed out"
	case result.Outcome == OutcomeCanceled:
		message = "TCP connection canceled"
	case errors.Is(result.Err, errNoConnection):
		failureClass = clientkit.FailureTransport
		message = "TCP dial returned no connection"
	}
	if state == clientkit.HealthHealthy && c.check.probe != nil {
		assessment, panicked := probeConnectionSafely(c.check.probe, checkContext, result.Conn)
		switch {
		case panicked:
			state = clientkit.HealthUnhealthy
			failureClass = clientkit.FailurePolicy
			message = "TCP health probe panicked"
		case assessment.State != clientkit.HealthHealthy &&
			assessment.State != clientkit.HealthDegraded &&
			assessment.State != clientkit.HealthUnhealthy:
			state = clientkit.HealthUnhealthy
			failureClass = clientkit.FailurePolicy
			message = "TCP health probe returned an invalid state"
		default:
			state = assessment.State
			failureClass = assessment.FailureClass
			message = assessment.Message
			if message == "" {
				message = "TCP health probe completed"
			}
			if state == clientkit.HealthHealthy {
				failureClass = clientkit.FailureNone
			} else if failureClass == clientkit.FailureNone {
				failureClass = clientkit.FailureRemoteResponse
			}
		}
	}
	if result.Conn != nil {
		_ = result.Conn.Close()
	}
	return c.completeCheckHealth(checkContext, state, failureClass, message, startedAt, metadata.tlsVersion)
}

func probeConnectionSafely(probe ConnectionProbe, ctx context.Context, connection net.Conn) (assessment clientkit.HealthAssessment, panicked bool) {
	defer func() {
		if recover() != nil {
			assessment = clientkit.HealthAssessment{}
			panicked = true
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		defer func() {
			_ = recover()
		}()
		_ = connection.Close()
	})
	defer stopCancellation()
	return probe.Probe(ctx, connection), false
}

// Health returns cached health with read-time staleness projection for enabled
// checks. It never mutates the cached value.
func (c *Client) Health() clientkit.Health {
	if c == nil || c.Client == nil {
		return clientkit.Health{State: clientkit.HealthUnknown, Message: "TCP client health is unavailable"}
	}

	health := c.Client.Health()
	if !c.check.enabled {
		return health
	}
	return healthrecord.ProjectStaleness(health, c.check.staleAfter, "TCP health check")
}

// Snapshot returns the client's identity, readiness policy, and effective
// health without exposing connection configuration.
func (c *Client) Snapshot() clientkit.ClientSnapshot {
	if c == nil || c.Client == nil {
		return clientkit.ClientSnapshot{Health: c.Health()}
	}
	return clientkit.ClientSnapshot{
		Name:            c.Name(),
		ReadinessPolicy: c.ReadinessPolicy(),
		Health:          c.Health(),
	}
}

func (c *Client) completeCheckHealth(ctx context.Context, state clientkit.HealthState, failureClass clientkit.FailureClass, message string, startedAt time.Time, tlsVersion string) clientkit.Health {
	var client *clientkit.Client
	if c != nil {
		client = c.Client
	}
	return healthrecord.Record(client, ctx, ProtocolTCP, clientkit.HealthAssessment{
		State:        state,
		FailureClass: failureClass,
		Message:      message,
	}, startedAt, c.healthEventAttributes(tlsVersion))
}
