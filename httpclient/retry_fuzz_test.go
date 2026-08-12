package httpclient

import (
	"testing"
	"time"
)

func FuzzParseRetryAfterNeverReturnsNegativeDelay(f *testing.F) {
	for _, value := range []string{
		"0", "1", "30", "18446744073709551615", "-1", "1.5",
		"Wed, 12 Aug 2026 18:00:00 GMT", "invalid", "\r\n30",
	} {
		f.Add(value)
	}
	receivedAt := time.Date(2026, time.August, 12, 17, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, value string) {
		delay, ok := parseRetryAfter(value, receivedAt)
		if ok && delay < 0 {
			t.Fatalf("parseRetryAfter(%q) = (%v, true), want non-negative delay", value, delay)
		}
	})
}
