package httpclient_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPClientSnapshotAndHealthCapability(t *testing.T) {
	const baseURL = "https://private-payments.example.test/api/"
	client := newHTTPTestClient(t, nil, httpclient.Config{
		Config:  clientkit.Config{Name: "payments"},
		BaseURL: baseURL,
	})
	if client.Name() != "payments" || client.Protocol() != httpclient.ProtocolHTTP || client.ReadinessPolicy() != clientkit.ReadinessOptional {
		t.Fatalf("client identity = (%q, %q, %q), want configured values", client.Name(), client.Protocol(), client.ReadinessPolicy())
	}
	snapshot := client.Snapshot()
	if snapshot.Name != "payments" || snapshot.Protocol != httpclient.ProtocolHTTP || snapshot.ReadinessPolicy != clientkit.ReadinessOptional || snapshot.Health.State != clientkit.HealthUnknown {
		t.Fatalf("Snapshot() = %#v, want configured identity and unknown health", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal(Snapshot()) error = %v", err)
	}
	if strings.Contains(string(encoded), "private-payments") || strings.Contains(string(encoded), "/api/") {
		t.Fatalf("Snapshot JSON = %s, want no endpoint configuration", encoded)
	}
	if client.HealthCheckEnabled() {
		t.Fatal("HealthCheckEnabled() = true for zero CheckConfig")
	}
}

func TestHTTPClientProtocolIsStableAcrossSchemes(t *testing.T) {
	for _, baseURL := range []string{"http://example.test", "https://example.test"} {
		t.Run(baseURL, func(t *testing.T) {
			client := newHTTPTestClient(t, nil, httpclient.Config{BaseURL: baseURL})
			if got := client.Protocol(); got != httpclient.ProtocolHTTP {
				t.Fatalf("Protocol() = %q, want %q", got, httpclient.ProtocolHTTP)
			}
		})
	}
}

func TestHTTPClientOriginPolicy(t *testing.T) {
	tests := []struct {
		name              string
		baseURL           string
		requestURL        string
		host              string
		allowCrossOrigin  bool
		allowHostOverride bool
		wantCalls         int
		wantOutcome       httpclient.Outcome
		wantFailure       clientkit.FailureClass
	}{
		{name: "cross origin rejected", baseURL: "https://example.test", requestURL: "https://other.test/resource", wantOutcome: httpclient.OutcomeTransportError, wantFailure: clientkit.FailurePolicy},
		{name: "cross origin allowed", baseURL: "https://example.test", requestURL: "https://other.test/resource", allowCrossOrigin: true, wantCalls: 1, wantOutcome: httpclient.OutcomeSuccess},
		{name: "host override rejected", baseURL: "https://example.test", requestURL: "https://example.test/resource", host: "override.test", wantOutcome: httpclient.OutcomeTransportError, wantFailure: clientkit.FailurePolicy},
		{name: "host override allowed", baseURL: "https://example.test", requestURL: "https://example.test/resource", host: "override.test", allowHostOverride: true, wantCalls: 1, wantOutcome: httpclient.OutcomeSuccess},
		{name: "HTTPS default port is same origin", baseURL: "https://example.test", requestURL: "https://example.test:443/resource", wantCalls: 1, wantOutcome: httpclient.OutcomeSuccess},
		{name: "HTTP default port is same origin", baseURL: "http://example.test", requestURL: "http://example.test:80/resource", wantCalls: 1, wantOutcome: httpclient.OutcomeSuccess},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
			}, httpclient.Config{
				BaseURL:           test.baseURL,
				AllowCrossOrigin:  test.allowCrossOrigin,
				AllowHostOverride: test.allowHostOverride,
				Retry:             httpclient.NoRetryConfig(),
			})
			request, _ := http.NewRequest(http.MethodGet, test.requestURL, nil)
			request.Host = test.host
			result := client.Execute(request)
			if calls != test.wantCalls || result.Outcome != test.wantOutcome || result.FailureClass != test.wantFailure {
				t.Fatalf("Execute() = %#v with %d calls, want outcome %q failure %q with %d calls", result, calls, test.wantOutcome, test.wantFailure, test.wantCalls)
			}
			if test.wantFailure == clientkit.FailurePolicy && result.Err == nil {
				t.Fatal("policy rejection returned a nil error")
			}
		})
	}
}

func TestHTTPClientAccessorsAndIdleCleanup(t *testing.T) {
	transport := &closingRoundTripper{}
	client, err := httpclient.New(httpclient.Config{
		Config:     clientkit.Config{Name: "payments", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test",
		HTTPClient: &http.Client{Transport: transport},
		Propagator: httpclient.NopHeaderPropagator{},
		ResponseClassifier: httpclient.ResponseClassifierFunc(func(*http.Response) httpclient.ResponseDisposition {
			return httpclient.ResponseAccepted
		}),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := client.Propagator().(httpclient.NopHeaderPropagator); !ok {
		t.Fatalf("Propagator() = %T, want NopHeaderPropagator", client.Propagator())
	}
	if got := client.ResponseClassifier().Classify(&http.Response{StatusCode: http.StatusInternalServerError}); got != httpclient.ResponseAccepted {
		t.Fatalf("ResponseClassifier().Classify() = %q, want accepted", got)
	}
	client.CloseIdleConnections()
	if transport.closeCalls != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", transport.closeCalls)
	}

	var nilClient *httpclient.Client
	nilClient.CloseIdleConnections()
	if nilClient.Name() != "" || nilClient.Protocol() != "" || nilClient.ReadinessPolicy() != clientkit.ReadinessOptional {
		t.Fatalf("nil client identity = (%q, %q, %q), want empty name and protocol with optional policy", nilClient.Name(), nilClient.Protocol(), nilClient.ReadinessPolicy())
	}
	if _, ok := nilClient.Propagator().(httpclient.NopHeaderPropagator); !ok {
		t.Fatalf("nil Propagator() = %T, want NopHeaderPropagator", nilClient.Propagator())
	}
	if got := nilClient.ResponseClassifier().Classify(&http.Response{StatusCode: http.StatusNoContent}); got != httpclient.ResponseAccepted {
		t.Fatalf("nil ResponseClassifier() = %q for 204, want accepted", got)
	}
	if snapshot := nilClient.Snapshot(); snapshot.Protocol != "" || snapshot.ReadinessPolicy != clientkit.ReadinessOptional || snapshot.Health.State != clientkit.HealthUnknown {
		t.Fatalf("nil Snapshot() = %#v, want unknown health", snapshot)
	}
}

type closingRoundTripper struct {
	closeCalls int
}

func (*closingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, io.EOF
}

func (t *closingRoundTripper) CloseIdleConnections() {
	t.closeCalls++
}
