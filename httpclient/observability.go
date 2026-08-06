package httpclient

import (
	"net/http"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/opskit"
)

// ProtocolHTTP identifies HTTP observer events.
const ProtocolHTTP = "http"

const (
	// OperationHTTPRequest identifies an outbound HTTP request operation.
	OperationHTTPRequest = "request"
)

func (c *Client) clientObserver() clientkit.Observer {
	if c == nil || c.Client == nil {
		return clientkit.NopObserver{}
	}
	return c.Client.Observer()
}

func (c *Client) telemetryClientName() string {
	if c == nil || c.Client == nil {
		return ""
	}
	return c.Name()
}

func normalizedHTTPMethod(method string) string {
	if method == "" {
		return http.MethodGet
	}
	return method
}

func telemetryHTTPMethod(method string) string {
	// Execution retains the exact method; telemetry collapses custom methods to
	// keep the neutral attribute safe for metrics as well as spans and logs.
	method = normalizedHTTPMethod(method)
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect,
		http.MethodOptions, http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

func httpExecutionAttributes(method string, statusCode int, additional []opskit.Attribute) []opskit.Attribute {
	attributes := make([]opskit.Attribute, 0, len(additional)+2)
	attributes = append(attributes, opskit.Attr("http.method", telemetryHTTPMethod(method)))
	if statusClass := httpStatusClass(statusCode); statusClass != "" {
		attributes = append(attributes, opskit.Attr("http.status_class", statusClass))
	}
	attributes = append(attributes, additional...)
	return attributes
}

func httpHealthOperationAttributes() []opskit.Attribute {
	return []opskit.Attribute{opskit.Attr("client.operation", "health_check")}
}

func httpHealthAttributes(method string, statusCode int) []opskit.Attribute {
	return httpExecutionAttributes(method, statusCode, httpHealthOperationAttributes())
}

func httpStatusClass(statusCode int) string {
	switch statusCode / 100 {
	case 1:
		return "1xx"
	case 2:
		return "2xx"
	case 3:
		return "3xx"
	case 4:
		return "4xx"
	case 5:
		return "5xx"
	default:
		return ""
	}
}
