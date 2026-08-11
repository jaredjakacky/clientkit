package httpotel

import (
	"context"
	"sync/atomic"
)

type operationContextKey struct{}

type operationState struct {
	client    string
	operation string
	resends   atomic.Int64
}

type operationMetadata struct {
	state   *operationState
	attempt int
}

// WithOperation starts per-logical-operation HTTP transport state.
func WithOperation(ctx context.Context, client, operation string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, operationContextKey{}, operationMetadata{
		state: &operationState{client: client, operation: operation},
	})
}

// WithExecutionAttempt records the current one-based Clientkit execution
// attempt while retaining the logical operation's resend counter.
func WithExecutionAttempt(ctx context.Context, attempt int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	metadata, _ := ctx.Value(operationContextKey{}).(operationMetadata)
	metadata.attempt = attempt
	return context.WithValue(ctx, operationContextKey{}, metadata)
}

func metadataFromContext(ctx context.Context) operationMetadata {
	if ctx == nil {
		return operationMetadata{}
	}
	metadata, _ := ctx.Value(operationContextKey{}).(operationMetadata)
	return metadata
}

func (m operationMetadata) nextResendCount() int {
	if m.state == nil {
		return 0
	}
	return int(m.state.resends.Add(1) - 1)
}
