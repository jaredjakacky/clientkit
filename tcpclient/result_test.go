package tcpclient_test

import (
	"testing"

	"github.com/jaredjakacky/clientkit/tcpclient"
)

func TestTCPOutcomeVocabulary(t *testing.T) {
	tests := []struct {
		outcome tcpclient.Outcome
		want    string
	}{
		{outcome: tcpclient.OutcomeSuccess, want: "success"},
		{outcome: tcpclient.OutcomeTimeout, want: "timeout"},
		{outcome: tcpclient.OutcomeCanceled, want: "canceled"},
		{outcome: tcpclient.OutcomeDialError, want: "dial_error"},
		{outcome: tcpclient.OutcomeTLSError, want: "tls_error"},
	}

	for _, test := range tests {
		if got := string(test.outcome); got != test.want {
			t.Errorf("outcome = %q, want %q", got, test.want)
		}
	}
}
