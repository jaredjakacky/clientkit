package clientkit_test

import (
	"context"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
)

func TestConfigValidateAcceptsSupportedCombinations(t *testing.T) {
	identitySanitizer := func(_ string, health clientkit.Health) clientkit.Health {
		return health
	}
	tests := []struct {
		name   string
		config clientkit.Config
	}{
		{name: "minimal", config: clientkit.Config{Name: "payments"}},
		{
			name: "explicit readiness",
			config: clientkit.Config{
				Name:            "payments",
				ReadinessPolicy: clientkit.ReadinessRequired,
			},
		},
		{
			name: "observer replacement",
			config: clientkit.Config{
				Name:     "payments",
				Observer: clientkit.NopObserver{},
			},
		},
		{
			name: "custom health sanitizer",
			config: clientkit.Config{
				Name:            "payments",
				HealthSanitizer: identitySanitizer,
			},
		},
		{
			name: "health sanitizer disabled",
			config: clientkit.Config{
				Name:                   "payments",
				DisableHealthSanitizer: true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.config.Validate(); err != nil {
				t.Fatalf("Config.Validate() error = %v", err)
			}
		})
	}
}

func TestConfigValidateRejectsContradictions(t *testing.T) {
	identitySanitizer := func(_ string, health clientkit.Health) clientkit.Health {
		return health
	}
	tests := []struct {
		name   string
		config clientkit.Config
	}{
		{name: "missing name", config: clientkit.Config{}},
		{name: "invalid name", config: clientkit.Config{Name: "Payments"}},
		{
			name: "invalid readiness",
			config: clientkit.Config{
				Name:            "payments",
				ReadinessPolicy: "invalid",
			},
		},
		{
			name: "sanitizer set and disabled",
			config: clientkit.Config{
				Name:                   "payments",
				HealthSanitizer:        identitySanitizer,
				DisableHealthSanitizer: true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.config.Validate(); err == nil {
				t.Fatal("Config.Validate() error = nil, want validation failure")
			}
		})
	}
}

func TestConfigValidateDoesNotInvokeExtensions(t *testing.T) {
	sanitizerCalled := false
	observerCalled := false
	config := clientkit.Config{
		Name: "payments",
		HealthSanitizer: func(_ string, health clientkit.Health) clientkit.Health {
			sanitizerCalled = true
			return health
		},
		Observer: observerCallbacks{
			start: func(ctx context.Context, _ clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
				observerCalled = true
				return ctx, nil
			},
		},
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("Config.Validate() error = %v", err)
	}
	if sanitizerCalled {
		t.Fatal("Config.Validate() invoked the configured health sanitizer")
	}
	if observerCalled {
		t.Fatal("Config.Validate() invoked the configured observer")
	}
}
