package httpclient

import (
	"context"
	"io"
	"net/http"
	"time"
)

const maxDiscardedResponseBodyBytes int64 = 64 << 10

// drainAndCloseResponse performs best-effort HTTP/1.x connection hygiene for a
// response that Clientkit definitively owns and discards. The extra byte lets a
// body exactly at the payload threshold expose EOF without permitting an
// unbounded read.
func drainAndCloseResponse(ctx context.Context, response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}

	body := response.Body
	defer func() { _ = body.Close() }()

	drainCtx, ok := discardedResponseDrainContext(ctx, response)
	if !ok || !shouldDrainDiscardedResponse(response) {
		return
	}

	_, _ = io.CopyN(io.Discard, contextBoundReader{
		ctx:    drainCtx,
		reader: body,
	}, maxDiscardedResponseBodyBytes+1)
}

func shouldDrainDiscardedResponse(response *http.Response) bool {
	if response.ProtoMajor != 1 || response.StatusCode < http.StatusOK ||
		response.Body == http.NoBody || response.ContentLength == 0 ||
		response.ContentLength > maxDiscardedResponseBodyBytes || response.Close {
		return false
	}
	if _, upgraded := response.Body.(io.Writer); upgraded {
		return false
	}
	if response.Request == nil {
		return true
	}
	return response.Request.Method != http.MethodHead && !response.Request.Close
}

func discardedResponseDrainContext(ctx context.Context, response *http.Response) (context.Context, bool) {
	requestCtx := context.Context(nil)
	if response.Request != nil {
		requestCtx = response.Request.Context()
	}

	if (ctx != nil && ctx.Err() != nil) || (requestCtx != nil && requestCtx.Err() != nil) {
		return nil, false
	}

	ctxDeadline, ctxHasDeadline := contextDeadline(ctx)
	requestDeadline, requestHasDeadline := contextDeadline(requestCtx)
	switch {
	case !ctxHasDeadline && !requestHasDeadline:
		return nil, false
	case !ctxHasDeadline:
		return requestCtx, true
	case !requestHasDeadline:
		return ctx, true
	case requestDeadline.Before(ctxDeadline):
		return requestCtx, true
	default:
		return ctx, true
	}
}

func contextDeadline(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	return ctx.Deadline()
}

type contextBoundReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextBoundReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
