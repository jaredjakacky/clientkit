package clientkit_test

import (
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
)

func TestFailureClassVocabulary(t *testing.T) {
	tests := []struct {
		name  string
		class clientkit.FailureClass
		want  string
	}{
		{name: "none", class: clientkit.FailureNone, want: ""},
		{name: "configuration", class: clientkit.FailureConfiguration, want: "configuration"},
		{name: "policy", class: clientkit.FailurePolicy, want: "policy"},
		{name: "request", class: clientkit.FailureRequest, want: "request"},
		{name: "canceled", class: clientkit.FailureCanceled, want: "canceled"},
		{name: "timeout", class: clientkit.FailureTimeout, want: "timeout"},
		{name: "name resolution", class: clientkit.FailureNameResolution, want: "name_resolution"},
		{name: "connection refused", class: clientkit.FailureConnectionRefused, want: "connection_refused"},
		{name: "connection reset", class: clientkit.FailureConnectionReset, want: "connection_reset"},
		{name: "connection closed", class: clientkit.FailureConnectionClosed, want: "connection_closed"},
		{name: "TLS", class: clientkit.FailureTLS, want: "tls"},
		{name: "remote response", class: clientkit.FailureRemoteResponse, want: "remote_response"},
		{name: "transport", class: clientkit.FailureTransport, want: "transport"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := string(test.class); got != test.want {
				t.Fatalf("failure class = %q, want %q", got, test.want)
			}
		})
	}

	var zero clientkit.FailureClass
	if zero != clientkit.FailureNone {
		t.Fatalf("zero FailureClass = %q, want FailureNone", zero)
	}
}
