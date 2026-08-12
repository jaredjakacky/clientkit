package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// NewRequest resolves a relative URL reference against the configured BaseURL
// using RFC 3986 semantics and returns a request bound to ctx. Root-relative and
// parent references may replace or escape the BaseURL path, which is not a
// confinement boundary. Absolute references and fragments are rejected so
// endpoint-origin policy remains explicit.
func (c *Client) NewRequest(ctx context.Context, method string, path string, body io.Reader) (*http.Request, error) {
	if c == nil || c.baseURL == nil {
		return nil, errors.New("clientkit: HTTP client is not configured")
	}
	if ctx == nil {
		return nil, errors.New("clientkit: context is required")
	}

	reference, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("clientkit: parse request path: %w", err)
	}
	if reference.IsAbs() || reference.Host != "" {
		return nil, errors.New("clientkit: request path must be relative")
	}
	if reference.Fragment != "" || reference.RawFragment != "" {
		return nil, errors.New("clientkit: request path must not include a fragment")
	}

	target := c.baseURL.ResolveReference(reference)

	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("clientkit: create request: %w", err)
	}

	return request, nil
}
