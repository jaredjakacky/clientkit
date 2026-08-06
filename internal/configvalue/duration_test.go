package configvalue_test

import (
	"testing"
	"time"

	"github.com/jaredjakacky/clientkit/internal/configvalue"
)

func TestDuration(t *testing.T) {
	t.Parallel()

	const (
		defaultValue  = 30 * time.Second
		disabledValue = -1 * time.Nanosecond
	)
	tests := []struct {
		name      string
		value     time.Duration
		disabled  bool
		want      time.Duration
		wantError string
	}{
		{
			name: "zero selects default",
			want: defaultValue,
		},
		{
			name:  "explicit positive value",
			value: 250 * time.Millisecond,
			want:  250 * time.Millisecond,
		},
		{
			name:     "disabled selects caller value",
			disabled: true,
			want:     disabledValue,
		},
		{
			name:      "negative value",
			value:     -time.Nanosecond,
			wantError: "clientkit: request timeout must not be negative",
		},
		{
			name:      "negative value takes precedence over disabled contradiction",
			value:     -time.Second,
			disabled:  true,
			wantError: "clientkit: request timeout must not be negative",
		},
		{
			name:      "positive value cannot also be disabled",
			value:     time.Second,
			disabled:  true,
			wantError: "clientkit: request timeout cannot be set when disabled",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := configvalue.Duration("request timeout", test.value, test.disabled, defaultValue, disabledValue)
			if test.wantError != "" {
				if err == nil || err.Error() != test.wantError {
					t.Fatalf("Duration() error = %v, want %q", err, test.wantError)
				}
				if got != 0 {
					t.Fatalf("Duration() value = %v after error, want 0", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Duration() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Duration() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDurationPreservesCallerDefinedSentinels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		disabled      bool
		defaultValue  time.Duration
		disabledValue time.Duration
		want          time.Duration
	}{
		{name: "zero default", defaultValue: 0},
		{name: "zero disabled sentinel", disabled: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := configvalue.Duration("duration", 0, test.disabled, test.defaultValue, test.disabledValue)
			if err != nil || got != test.want {
				t.Fatalf("Duration() = (%v, %v), want (%v, nil)", got, err, test.want)
			}
		})
	}
}
