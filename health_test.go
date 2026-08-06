package clientkit_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	clientkit "github.com/jaredjakacky/clientkit"
)

func TestHealthStateVocabulary(t *testing.T) {
	tests := []struct {
		state clientkit.HealthState
		want  string
	}{
		{state: clientkit.HealthUnknown, want: "unknown"},
		{state: clientkit.HealthHealthy, want: "healthy"},
		{state: clientkit.HealthDegraded, want: "degraded"},
		{state: clientkit.HealthUnhealthy, want: "unhealthy"},
	}

	for _, test := range tests {
		if got := string(test.state); got != test.want {
			t.Errorf("health state = %q, want %q", got, test.want)
		}
	}
	if clientkit.DefaultMaxHealthMessageBytes != 256 {
		t.Fatalf("DefaultMaxHealthMessageBytes = %d, want 256", clientkit.DefaultMaxHealthMessageBytes)
	}
}

func TestDefaultHealthSanitizer(t *testing.T) {
	t.Run("healthy clears failure", func(t *testing.T) {
		health := clientkit.DefaultHealthSanitizer("payments", clientkit.Health{
			State:        clientkit.HealthHealthy,
			FailureClass: clientkit.FailureTransport,
		})
		if health.FailureClass != clientkit.FailureNone {
			t.Fatalf("FailureClass = %q, want empty", health.FailureClass)
		}
	})

	t.Run("invalid state and failure are bounded", func(t *testing.T) {
		health := clientkit.DefaultHealthSanitizer("payments", clientkit.Health{
			State:        "custom",
			FailureClass: "custom",
			Message:      "raw details",
		})
		if health.State != clientkit.HealthUnknown || health.FailureClass != clientkit.FailurePolicy {
			t.Fatalf("sanitized health = %#v, want unknown policy failure", health)
		}
		if health.Message != "client health state unavailable" {
			t.Fatalf("Message = %q, want stable replacement", health.Message)
		}
	})

	t.Run("valid state with invalid failure", func(t *testing.T) {
		health := clientkit.DefaultHealthSanitizer("payments", clientkit.Health{
			State:        clientkit.HealthUnhealthy,
			FailureClass: "custom",
		})
		if health.FailureClass != clientkit.FailurePolicy {
			t.Fatalf("FailureClass = %q, want %q", health.FailureClass, clientkit.FailurePolicy)
		}
	})

	t.Run("time duration and message normalization", func(t *testing.T) {
		location := time.FixedZone("offset", 2*60*60)
		checkedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, location)
		message := "line one\nline two\t\u2028\u2029\u202e\u2066" + strings.Repeat("é", clientkit.DefaultMaxHealthMessageBytes)
		health := clientkit.DefaultHealthSanitizer("payments", clientkit.Health{
			State:     clientkit.HealthUnhealthy,
			CheckedAt: checkedAt,
			Duration:  -time.Second,
			Message:   message,
		})
		if health.CheckedAt.Location() != time.UTC || !health.CheckedAt.Equal(checkedAt) {
			t.Fatalf("CheckedAt = %v, want same instant in UTC", health.CheckedAt)
		}
		if health.Duration != 0 {
			t.Fatalf("Duration = %v, want 0", health.Duration)
		}
		if strings.ContainsAny(health.Message, "\n\t\u2028\u2029\u202e\u2066") {
			t.Fatalf("Message contains control or formatting characters: %q", health.Message)
		}
		if len(health.Message) > clientkit.DefaultMaxHealthMessageBytes || !utf8.ValidString(health.Message) {
			t.Fatalf("Message is not valid bounded UTF-8: %q", health.Message)
		}
	})

	t.Run("truncation preserves complete UTF-8 runes", func(t *testing.T) {
		// The final two-byte rune crosses the byte boundary and must be removed
		// whole rather than leaving invalid UTF-8 behind.
		message := strings.Repeat("a", clientkit.DefaultMaxHealthMessageBytes-1) + "é"
		health := clientkit.DefaultHealthSanitizer("payments", clientkit.Health{
			State:   clientkit.HealthDegraded,
			Message: message,
		})
		want := strings.Repeat("a", clientkit.DefaultMaxHealthMessageBytes-1)
		if health.Message != want || !utf8.ValidString(health.Message) {
			t.Fatalf("Message = %q, want %q as valid UTF-8", health.Message, want)
		}
	})

	t.Run("zero completion time remains unset", func(t *testing.T) {
		health := clientkit.DefaultHealthSanitizer("payments", clientkit.Health{State: clientkit.HealthUnknown})
		if !health.CheckedAt.IsZero() {
			t.Fatalf("CheckedAt = %v, want zero time", health.CheckedAt)
		}
	})
}

func TestDefaultHealthSanitizerPreservesSupportedFailureClasses(t *testing.T) {
	classes := []struct {
		name  string
		class clientkit.FailureClass
	}{
		{name: "none", class: clientkit.FailureNone},
		{name: "configuration", class: clientkit.FailureConfiguration},
		{name: "policy", class: clientkit.FailurePolicy},
		{name: "request", class: clientkit.FailureRequest},
		{name: "canceled", class: clientkit.FailureCanceled},
		{name: "timeout", class: clientkit.FailureTimeout},
		{name: "name resolution", class: clientkit.FailureNameResolution},
		{name: "connection refused", class: clientkit.FailureConnectionRefused},
		{name: "connection reset", class: clientkit.FailureConnectionReset},
		{name: "connection closed", class: clientkit.FailureConnectionClosed},
		{name: "TLS", class: clientkit.FailureTLS},
		{name: "remote response", class: clientkit.FailureRemoteResponse},
		{name: "transport", class: clientkit.FailureTransport},
	}

	for _, test := range classes {
		t.Run(test.name, func(t *testing.T) {
			health := clientkit.DefaultHealthSanitizer("payments", clientkit.Health{
				State:        clientkit.HealthUnhealthy,
				FailureClass: test.class,
			})
			if health.FailureClass != test.class {
				t.Fatalf("FailureClass = %q, want %q", health.FailureClass, test.class)
			}
		})
	}
}

func TestHealthAssessmentJSONContract(t *testing.T) {
	want := clientkit.HealthAssessment{
		State:        clientkit.HealthDegraded,
		FailureClass: clientkit.FailureRemoteResponse,
		Message:      "fallback available",
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	const wantJSON = `{"state":"degraded","failure_class":"remote_response","message":"fallback available"}`
	if string(encoded) != wantJSON {
		t.Fatalf("json.Marshal() = %s, want %s", encoded, wantJSON)
	}

	var decoded clientkit.HealthAssessment
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded != want {
		t.Fatalf("JSON round trip = %#v, want %#v", decoded, want)
	}
}

func TestHealthIsHealthy(t *testing.T) {
	states := []struct {
		state   clientkit.HealthState
		healthy bool
	}{
		{state: ""},
		{state: clientkit.HealthUnknown},
		{state: clientkit.HealthHealthy, healthy: true},
		{state: clientkit.HealthDegraded},
		{state: clientkit.HealthUnhealthy},
		{state: "custom"},
	}
	for _, test := range states {
		if got := (clientkit.Health{State: test.state}).IsHealthy(); got != test.healthy {
			t.Errorf("Health{%q}.IsHealthy() = %t, want %t", test.state, got, test.healthy)
		}
	}
}
