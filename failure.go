package clientkit

// FailureClass is a stable, low-cardinality classification that supplements
// protocol outcomes and original errors. Consumers may use it for metrics,
// policy decisions, inspection, and alert grouping. Platform error wrapping
// can prevent some failures from receiving their most specific class;
// FailureTransport is the safe fallback. Raw errors must never be used as
// telemetry labels.
type FailureClass string

const (
	// FailureNone indicates that no classified failure occurred. It is the empty
	// string so successful and unclassified zero values require no special setup.
	FailureNone FailureClass = ""
	// FailureConfiguration indicates missing or unusable client configuration.
	FailureConfiguration FailureClass = "configuration"
	// FailurePolicy indicates rejection by a Clientkit execution policy.
	FailurePolicy FailureClass = "policy"
	// FailureRequest indicates invalid request or operation preparation.
	FailureRequest FailureClass = "request"
	// FailureCanceled indicates caller or parent-context cancellation.
	FailureCanceled FailureClass = "canceled"
	// FailureTimeout indicates deadline expiry or another recognized timeout.
	FailureTimeout FailureClass = "timeout"
	// FailureNameResolution indicates a DNS or name-resolution failure.
	FailureNameResolution FailureClass = "name_resolution"
	// FailureConnectionRefused indicates that the destination refused a connection.
	FailureConnectionRefused FailureClass = "connection_refused"
	// FailureConnectionReset indicates that a connection was reset.
	FailureConnectionReset FailureClass = "connection_reset"
	// FailureConnectionClosed indicates a recognized closed-connection condition.
	FailureConnectionClosed FailureClass = "connection_closed"
	// FailureTLS indicates a non-timeout TLS or certificate failure.
	FailureTLS FailureClass = "tls"
	// FailureRemoteResponse indicates an unacceptable completed remote response.
	FailureRemoteResponse FailureClass = "remote_response"
	// FailureTransport indicates another network or transport failure.
	FailureTransport FailureClass = "transport"
)
