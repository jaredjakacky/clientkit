package tcpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/jaredjakacky/clientkit"
	internalfailure "github.com/jaredjakacky/clientkit/internal/failure"
)

var (
	errNoConnection         = errors.New("clientkit: TCP dial returned no connection")
	errObservedTLSHandshake = errors.New("clientkit: TLS handshake failed")
)

type connectionMetadata struct {
	tlsVersion         string
	tlsHandshakeFailed bool
	contextErr         error
}

// Dial establishes one configured plaintext or TLS connection using ordinary
// Go connection/error semantics. A successful connection is caller-owned.
func (c *Client) Dial(ctx context.Context) (net.Conn, error) {
	result := c.DialResult(ctx)
	return result.Conn, result.Err
}

// DialContext implements the common context dialer shape for integrations that
// pass the requested network and address. Clientkit remains endpoint-bound and
// rejects a target that differs from its immutable configuration.
func (c *Client) DialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	if c == nil || c.Client == nil {
		return nil, errors.New("clientkit: TCP client is not configured")
	}
	network = strings.ToLower(strings.TrimSpace(network))
	address = strings.TrimSpace(address)
	if network != c.network || address != c.address {
		return nil, errors.New("clientkit: TCP dial target does not match configured endpoint")
	}
	return c.Dial(ctx)
}

// DialResult establishes one plaintext or TLS connection and returns detailed
// Clientkit outcome metadata. A successful Conn is caller-owned.
func (c *Client) DialResult(ctx context.Context) Result {
	startedAt := time.Now()
	if c == nil || c.Client == nil || (c.dialContext == nil && c.dialer == nil) {
		return failedDialResult(startedAt, clientkit.FailureConfiguration, errors.New("clientkit: TCP client is not configured"))
	}
	if ctx == nil {
		return failedDialResult(startedAt, clientkit.FailureRequest, errors.New("clientkit: TCP dial context is required"))
	}
	result, _ := c.dialObserved(ctx)
	return result
}

func (c *Client) dialObserved(ctx context.Context) (Result, connectionMetadata) {
	operationStartedAt := time.Now()
	clientName := c.telemetryClientName()
	observer := c.clientObserver()
	operationContext, observation := observer.StartOperation(ctx, clientkit.OperationStartEvent{
		Client:     clientName,
		Protocol:   ProtocolTCP,
		Operation:  OperationDial,
		StartedAt:  operationStartedAt.UTC(),
		Attributes: c.eventAttributes(""),
	})

	attemptStartedAt := time.Now()
	connection, metadata, err := c.establishConnection(operationContext)
	attemptEndedAt := time.Now()
	failureClass := classifyFailure(connection, err, metadata)
	outcome := classifyOutcome(connection, err, metadata, failureClass)
	observedErr := observedConnectionError(err, outcome, metadata, failureClass)

	observer.ObserveAttempt(operationContext, clientkit.AttemptEvent{
		Client:       clientName,
		Protocol:     ProtocolTCP,
		Operation:    OperationDial,
		Number:       1,
		StartedAt:    attemptStartedAt.UTC(),
		EndedAt:      attemptEndedAt.UTC(),
		Duration:     attemptEndedAt.Sub(attemptStartedAt),
		Outcome:      string(outcome),
		Succeeded:    outcome == OutcomeSuccess,
		FailureClass: failureClass,
		Err:          observedErr,
		Attributes:   c.eventAttributes(metadata.tlsVersion),
	})

	operationEndedAt := time.Now()
	result := Result{
		Conn:         connection,
		Outcome:      outcome,
		FailureClass: failureClass,
		StartedAt:    operationStartedAt.UTC(),
		Duration:     operationEndedAt.Sub(operationStartedAt),
		Err:          err,
	}
	observation.End(operationContext, clientkit.OperationEndEvent{
		Client:       clientName,
		Protocol:     ProtocolTCP,
		Operation:    OperationDial,
		StartedAt:    operationStartedAt.UTC(),
		EndedAt:      operationEndedAt.UTC(),
		Duration:     result.Duration,
		Attempts:     1,
		Outcome:      string(result.Outcome),
		Succeeded:    result.Outcome == OutcomeSuccess,
		FailureClass: result.FailureClass,
		Err:          observedErr,
		Attributes:   c.eventAttributes(metadata.tlsVersion),
	})
	return result, metadata
}

func observedConnectionError(err error, outcome Outcome, metadata connectionMetadata, failureClass clientkit.FailureClass) error {
	if !metadata.tlsHandshakeFailed && failureClass != clientkit.FailureTLS {
		return err
	}
	switch outcome {
	case OutcomeCanceled:
		return context.Canceled
	case OutcomeTimeout:
		return context.DeadlineExceeded
	default:
		return errObservedTLSHandshake
	}
}

func (c *Client) establishConnection(ctx context.Context) (net.Conn, connectionMetadata, error) {
	dialContext := ctx
	dialCancel := func() {}
	if c.dialTimeout > 0 {
		dialContext, dialCancel = context.WithTimeout(dialContext, c.dialTimeout)
	}
	connection, err := c.dialConnection(dialContext)
	dialContextErr := dialContext.Err()
	dialCancel()
	if connection != nil && err != nil {
		_ = connection.Close()
		return nil, connectionMetadata{contextErr: dialContextErr}, err
	}
	if connection == nil {
		if err == nil {
			err = errNoConnection
		}
		return nil, connectionMetadata{contextErr: dialContextErr}, err
	}
	if !c.tls.enabled {
		return connection, connectionMetadata{}, nil
	}

	secureConnection := tls.Client(connection, c.tls.config.Clone())
	handshakeContext := ctx
	handshakeCancel := func() {}
	if c.tls.handshakeTimeout > 0 {
		handshakeContext, handshakeCancel = context.WithTimeout(handshakeContext, c.tls.handshakeTimeout)
	}
	err = secureConnection.HandshakeContext(handshakeContext)
	handshakeContextErr := handshakeContext.Err()
	handshakeCancel()
	if err != nil {
		_ = connection.Close()
		return nil, connectionMetadata{
			tlsHandshakeFailed: true,
			contextErr:         handshakeContextErr,
		}, err
	}

	return secureConnection, connectionMetadata{
		tlsVersion: tlsVersionName(secureConnection.ConnectionState().Version),
	}, nil
}

func (c *Client) dialConnection(ctx context.Context) (net.Conn, error) {
	if c.dialContext != nil {
		return c.dialContext(ctx, c.network, c.address)
	}
	return c.dialer.DialContext(ctx, c.network, c.address)
}

func failedDialResult(startedAt time.Time, failureClass clientkit.FailureClass, err error) Result {
	return Result{
		Outcome:      OutcomeDialError,
		FailureClass: failureClass,
		StartedAt:    startedAt.UTC(),
		Duration:     time.Since(startedAt),
		Err:          err,
	}
}

func classifyFailure(connection net.Conn, err error, metadata connectionMetadata) clientkit.FailureClass {
	if err == nil && connection != nil {
		return clientkit.FailureNone
	}
	if errors.Is(metadata.contextErr, context.Canceled) {
		return clientkit.FailureCanceled
	}
	if errors.Is(metadata.contextErr, context.DeadlineExceeded) {
		return clientkit.FailureTimeout
	}
	if err == nil {
		return clientkit.FailureTransport
	}
	return internalfailure.Network(err, metadata.tlsHandshakeFailed)
}

func classifyOutcome(connection net.Conn, err error, metadata connectionMetadata, failureClass clientkit.FailureClass) Outcome {
	if err == nil && connection != nil {
		return OutcomeSuccess
	}
	if errors.Is(metadata.contextErr, context.Canceled) {
		return OutcomeCanceled
	}
	if errors.Is(metadata.contextErr, context.DeadlineExceeded) {
		return OutcomeTimeout
	}
	if errors.Is(err, context.Canceled) {
		return OutcomeCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return OutcomeTimeout
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return OutcomeTimeout
	}
	if metadata.tlsHandshakeFailed || failureClass == clientkit.FailureTLS {
		return OutcomeTLSError
	}
	return OutcomeDialError
}
