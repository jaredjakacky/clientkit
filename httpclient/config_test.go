package httpclient_test

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestHTTPClientConfigUsesNamedClientkitConfig(t *testing.T) {
	field, ok := reflect.TypeOf(httpclient.Config{}).FieldByName("Config")
	if !ok {
		t.Fatal("Config field is missing")
	}
	if field.Anonymous {
		t.Fatal("Config field is anonymous, want named composition")
	}
	if want := reflect.TypeOf(clientkit.Config{}); field.Type != want {
		t.Fatalf("Config field type = %v, want %v", field.Type, want)
	}
}

func TestHTTPClientProductionDefaults(t *testing.T) {
	client, err := httpclient.New(httpclient.Config{
		Config:  clientkit.Config{Name: "production-defaults"},
		BaseURL: "https://example.test",
	})
	if err != nil || client == nil {
		t.Fatalf("New() = (%v, %v), want client with production defaults", client, err)
	}
	classifier := client.ResponseClassifier()
	if classifier.Classify(&http.Response{StatusCode: http.StatusNoContent}) != httpclient.ResponseAccepted ||
		classifier.Classify(&http.Response{StatusCode: http.StatusNotFound}) != httpclient.ResponseRejected {
		t.Fatal("production default response classification did not accept 2xx and reject 4xx")
	}
	if client.HealthCheckEnabled() {
		t.Fatal("production defaults unexpectedly enabled active health checking")
	}
}

func TestHTTPClientConfigValidation(t *testing.T) {
	base := httpclient.Config{
		Config:     clientkit.Config{Name: "payments", Observer: clientkit.NopObserver{}},
		BaseURL:    "https://example.test",
		Propagator: httpclient.NopHeaderPropagator{},
	}

	tests := []struct {
		name   string
		mutate func(*httpclient.Config)
	}{
		{name: "invalid client name", mutate: func(cfg *httpclient.Config) { cfg.Config.Name = "Payments" }},
		{name: "negative timeout", mutate: func(cfg *httpclient.Config) { cfg.Timeout = -time.Second }},
		{name: "timeout set and disabled", mutate: func(cfg *httpclient.Config) { cfg.Timeout = time.Second; cfg.DisableTimeout = true }},
		{name: "negative attempt timeout", mutate: func(cfg *httpclient.Config) { cfg.AttemptTimeout = -time.Second }},
		{name: "attempt timeout set and disabled", mutate: func(cfg *httpclient.Config) { cfg.AttemptTimeout = time.Second; cfg.DisableAttemptTimeout = true }},
		{name: "missing base URL", mutate: func(cfg *httpclient.Config) { cfg.BaseURL = "" }},
		{name: "invalid base URL", mutate: func(cfg *httpclient.Config) { cfg.BaseURL = "https://[::1" }},
		{name: "unsupported scheme", mutate: func(cfg *httpclient.Config) { cfg.BaseURL = "ftp://example.test" }},
		{name: "missing host", mutate: func(cfg *httpclient.Config) { cfg.BaseURL = "https:///path" }},
		{name: "zero port", mutate: func(cfg *httpclient.Config) { cfg.BaseURL = "https://example.test:0" }},
		{name: "invalid port", mutate: func(cfg *httpclient.Config) { cfg.BaseURL = "https://example.test:70000" }},
		{name: "user information", mutate: func(cfg *httpclient.Config) { cfg.BaseURL = "https://user@example.test" }},
		{name: "fragment", mutate: func(cfg *httpclient.Config) { cfg.BaseURL = "https://example.test/#fragment" }},
		{name: "query", mutate: func(cfg *httpclient.Config) { cfg.BaseURL = "https://example.test/?key=value" }},
		{name: "empty query", mutate: func(cfg *httpclient.Config) { cfg.BaseURL = "https://example.test/?" }},
		{name: "invalid retry", mutate: func(cfg *httpclient.Config) { cfg.Retry = httpclient.RetryConfig{MaxAttempts: -1} }},
		{name: "required without check", mutate: func(cfg *httpclient.Config) { cfg.Config.ReadinessPolicy = clientkit.ReadinessRequired }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.mutate(&cfg)
			if client, err := httpclient.New(cfg); err == nil || client != nil {
				t.Fatalf("New() = (%v, %v), want nil client and validation error", client, err)
			}
		})
	}
}
