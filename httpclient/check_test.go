package httpclient_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
	"github.com/jaredjakacky/opskit"
)

func TestDefaultHTTPCheckConfig(t *testing.T) {
	if httpclient.DefaultCheckStaleAfter != 90*time.Second {
		t.Fatalf("DefaultCheckStaleAfter = %v, want 90s", httpclient.DefaultCheckStaleAfter)
	}
	check := httpclient.DefaultCheckConfig("/healthz")
	if !check.Enabled || check.Path != "/healthz" {
		t.Fatalf("DefaultCheckConfig() = %#v, want enabled supplied path", check)
	}
	if !reflect.DeepEqual(check.Retry, httpclient.RetryConfig{}) {
		t.Fatalf("DefaultCheckConfig().Retry = %#v, want zero no-retry policy", check.Retry)
	}
}

func TestHTTPCheckConfigValidation(t *testing.T) {
	base := httpclient.Config{
		Config:     clientkit.Config{Name: "health", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test",
		Propagator: httpclient.NopHeaderPropagator{},
	}
	tests := []struct {
		name  string
		check httpclient.CheckConfig
	}{
		{name: "disabled with path", check: httpclient.CheckConfig{Path: "/healthz"}},
		{name: "missing path", check: httpclient.CheckConfig{Enabled: true}},
		{name: "absolute path", check: httpclient.CheckConfig{Enabled: true, Path: "https://other.test/healthz"}},
		{name: "network path", check: httpclient.CheckConfig{Enabled: true, Path: "//other.test/healthz"}},
		{name: "invalid path", check: httpclient.CheckConfig{Enabled: true, Path: "%"}},
		{name: "fragment", check: httpclient.CheckConfig{Enabled: true, Path: "/healthz#fragment"}},
		{name: "invalid method", check: httpclient.CheckConfig{Enabled: true, Path: "/healthz", Method: "BAD METHOD"}},
		{name: "blank method", check: httpclient.CheckConfig{Enabled: true, Path: "/healthz", Method: " "}},
		{name: "negative timeout", check: httpclient.CheckConfig{Enabled: true, Path: "/healthz", Timeout: -time.Second}},
		{name: "timeout set and disabled", check: httpclient.CheckConfig{Enabled: true, Path: "/healthz", Timeout: time.Second, DisableTimeout: true}},
		{name: "negative stale after", check: httpclient.CheckConfig{Enabled: true, Path: "/healthz", StaleAfter: -time.Second}},
		{name: "stale after set and disabled", check: httpclient.CheckConfig{Enabled: true, Path: "/healthz", StaleAfter: time.Second, DisableStaleAfter: true}},
		{name: "invalid retry", check: httpclient.CheckConfig{Enabled: true, Path: "/healthz", Retry: httpclient.RetryConfig{MaxAttempts: -1}}},
		{name: "invalid retry safety", check: httpclient.CheckConfig{Enabled: true, Path: "/healthz", RetrySafety: "invalid"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			cfg.Check = test.check
			if client, err := httpclient.New(cfg); err == nil || client != nil {
				t.Fatalf("New() = (%v, %v), want invalid check error", client, err)
			}
		})
	}
}

func TestHTTPCheckSuccessCachesHealthAndEmitsTelemetry(t *testing.T) {
	observer := &healthRecordingObserver{}
	body := &trackedReadCloser{Reader: strings.NewReader("healthy")}
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodHead || request.URL.Path != "/healthz" {
			t.Fatalf("health request = %s %s, want HEAD /healthz", request.Method, request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: body, Request: request}, nil
	}, httpclient.Config{
		Config: clientkit.Config{Name: "health", Observer: observer},
		Check: httpclient.CheckConfig{
			Enabled: true,
			Method:  http.MethodHead,
			Path:    "/healthz",
			ResponseClassifier: httpclient.ResponseClassifierFunc(func(response *http.Response) httpclient.ResponseDisposition {
				if response.StatusCode == http.StatusNoContent {
					return httpclient.ResponseAccepted
				}
				return httpclient.ResponseRejected
			}),
		},
	})

	health := client.Check(context.Background())
	if health.State != clientkit.HealthHealthy || health.FailureClass != clientkit.FailureNone || health.Message != "HTTP health check succeeded" {
		t.Fatalf("Check() = %#v, want healthy", health)
	}
	if health.CheckedAt.IsZero() || health.Duration < 0 || !body.closed {
		t.Fatalf("Check() lifecycle = %#v, response body closed %t", health, body.closed)
	}
	if cached := client.Health(); cached != health {
		t.Fatalf("Health() = %#v, want cached %#v", cached, health)
	}
	if !client.HealthCheckEnabled() {
		t.Fatal("HealthCheckEnabled() = false for enabled check")
	}
	if observer.health.State != clientkit.HealthHealthy || observer.health.Client != "health" || observer.health.Protocol != httpclient.ProtocolHTTP {
		t.Fatalf("health telemetry = %#v, want healthy HTTP event", observer.health)
	}
	if got := healthAttribute(observer.health.Attributes, "http.request.method"); got != http.MethodHead {
		t.Fatalf("http.request.method = %q, want HEAD", got)
	}
	if got := healthAttribute(observer.health.Attributes, "clientkit.http.status_class"); got != "2xx" {
		t.Fatalf("clientkit.http.status_class = %q, want 2xx", got)
	}
	for _, attribute := range observer.health.Attributes {
		if strings.Contains(attribute.Value, "/healthz") || strings.Contains(attribute.Value, "example.test") {
			t.Fatalf("health telemetry exposed endpoint: %#v", observer.health.Attributes)
		}
	}
	if want := []string{"operation-start", "attempt", "operation-end", "health"}; !reflect.DeepEqual(observer.events, want) {
		t.Fatalf("health lifecycle events = %v, want %v", observer.events, want)
	}
}

func TestHTTPCheckFailureAndRetryPolicy(t *testing.T) {
	t.Run("zero retry performs one attempt", func(t *testing.T) {
		calls := 0
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
		}, httpclient.Config{Check: httpclient.CheckConfig{Enabled: true, Path: "/healthz"}})
		health := client.Check(context.Background())
		if calls != 1 || health.State != clientkit.HealthUnhealthy || health.FailureClass != clientkit.FailureRemoteResponse {
			t.Fatalf("Check() = %#v with %d calls, want one rejected attempt", health, calls)
		}
	})

	t.Run("explicit retry", func(t *testing.T) {
		calls := 0
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			status := http.StatusServiceUnavailable
			if calls == 2 {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
		}, httpclient.Config{Check: httpclient.CheckConfig{
			Enabled: true,
			Path:    "/healthz",
			Retry: httpclient.RetryConfig{
				MaxAttempts: 2,
				StatusCodes: []int{http.StatusServiceUnavailable},
				Methods:     []string{http.MethodGet},
			},
		}})
		health := client.Check(context.Background())
		if calls != 2 || health.State != clientkit.HealthHealthy {
			t.Fatalf("Check() = %#v with %d calls, want retry success", health, calls)
		}
	})

	for _, test := range []struct {
		name        string
		retrySafety httpclient.RetrySafety
		wantCalls   int
		wantState   clientkit.HealthState
	}{
		{name: "unsafe POST is not retried", wantCalls: 1, wantState: clientkit.HealthUnhealthy},
		{name: "idempotent POST is retried", retrySafety: httpclient.RetrySafetyIdempotent, wantCalls: 2, wantState: clientkit.HealthHealthy},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				calls++
				status := http.StatusServiceUnavailable
				if calls == 2 {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
			}, httpclient.Config{Check: httpclient.CheckConfig{
				Enabled:     true,
				Method:      http.MethodPost,
				Path:        "/healthz",
				RetrySafety: test.retrySafety,
				Retry: httpclient.RetryConfig{
					MaxAttempts: 2,
					StatusCodes: []int{http.StatusServiceUnavailable},
					Methods:     []string{http.MethodPost},
				},
			}})
			health := client.Check(context.Background())
			if calls != test.wantCalls || health.State != test.wantState {
				t.Fatalf("Check() = %#v with %d calls, want %q after %d", health, calls, test.wantState, test.wantCalls)
			}
		})
	}

	t.Run("transport error", func(t *testing.T) {
		client := newHTTPTestClient(t, func(*http.Request) (*http.Response, error) {
			return nil, errors.New("transport")
		}, httpclient.Config{Check: httpclient.CheckConfig{Enabled: true, Path: "/healthz"}})
		health := client.Check(context.Background())
		if health.State != clientkit.HealthUnhealthy || health.FailureClass != clientkit.FailureTransport || health.Message != "HTTP health check request failed" {
			t.Fatalf("Check() = %#v, want transport failure", health)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}, httpclient.Config{Check: httpclient.CheckConfig{Enabled: true, Path: "/healthz", Timeout: 10 * time.Millisecond}})
		health := client.Check(context.Background())
		if health.FailureClass != clientkit.FailureTimeout || health.Message != "HTTP health check timed out" {
			t.Fatalf("Check() = %#v, want timeout health", health)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		client := newHTTPTestClient(t, nil, httpclient.Config{Check: httpclient.CheckConfig{Enabled: true, Path: "/healthz"}})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		health := client.Check(ctx)
		if health.FailureClass != clientkit.FailureCanceled || health.Message != "HTTP health check canceled" {
			t.Fatalf("Check() = %#v, want canceled health", health)
		}
	})

	t.Run("classifier policy failure", func(t *testing.T) {
		client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
		}, httpclient.Config{Check: httpclient.CheckConfig{
			Enabled: true,
			Path:    "/healthz",
			ResponseClassifier: httpclient.ResponseClassifierFunc(func(*http.Response) httpclient.ResponseDisposition {
				panic("classify")
			}),
		}})
		health := client.Check(context.Background())
		if health.FailureClass != clientkit.FailurePolicy || health.Message != "HTTP health response classification failed" {
			t.Fatalf("Check() = %#v, want contained classifier policy failure", health)
		}
	})
}

func TestHTTPCheckDisabledAndNil(t *testing.T) {
	calls := 0
	observer := &healthRecordingObserver{}
	disabled := newHTTPTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, nil
	}, httpclient.Config{Config: clientkit.Config{Name: "disabled", Observer: observer}})
	health := disabled.Check(context.Background())
	if health.State != clientkit.HealthUnknown || calls != 0 || observer.healthCount != 0 {
		t.Fatalf("disabled Check() = %#v, calls/events %d/%d", health, calls, observer.healthCount)
	}

	var nilClient *httpclient.Client
	if got := nilClient.Check(context.Background()); got.State != clientkit.HealthUnhealthy || got.FailureClass != clientkit.FailureConfiguration {
		t.Fatalf("nil Check() = %#v, want configuration failure", got)
	}
}

type healthRecordingObserver struct {
	clientkit.NopObserver
	health      clientkit.HealthEvent
	healthCount int
	events      []string
}

func (o *healthRecordingObserver) StartOperation(ctx context.Context, _ clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	o.events = append(o.events, "operation-start")
	return ctx, clientkit.OperationObservationFunc(func(context.Context, clientkit.OperationEndEvent) {
		o.events = append(o.events, "operation-end")
	})
}

func (o *healthRecordingObserver) ObserveAttempt(context.Context, clientkit.AttemptEvent) {
	o.events = append(o.events, "attempt")
}

func (o *healthRecordingObserver) ObserveHealth(_ context.Context, event clientkit.HealthEvent) {
	o.events = append(o.events, "health")
	o.health = event
	o.healthCount++
}

func healthAttribute(attributes []opskit.Attribute, key string) string {
	for _, attribute := range attributes {
		if attribute.Key == key {
			return attribute.Value
		}
	}
	return ""
}
