package httpclient_test

import (
	"net/http"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func newHTTPTestClient(t *testing.T, roundTrip func(*http.Request) (*http.Response, error), cfg httpclient.Config) *httpclient.Client {
	t.Helper()
	if cfg.Config.Name == "" {
		cfg.Config.Name = "test-client"
	}
	if cfg.Config.Observer == nil {
		cfg.Config.Observer = clientkit.NopObserver{}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://example.test"
	}
	if cfg.Propagator == nil {
		cfg.Propagator = httpclient.NopHeaderPropagator{}
	}
	if roundTrip != nil {
		if cfg.HTTPClient == nil {
			cfg.HTTPClient = &http.Client{}
		} else {
			copy := *cfg.HTTPClient
			cfg.HTTPClient = &copy
		}
		cfg.HTTPClient.Transport = testRoundTripperFunc(roundTrip)
	}
	client, err := httpclient.New(cfg)
	if err != nil {
		t.Fatalf("httpclient.New() error = %v", err)
	}
	return client
}
