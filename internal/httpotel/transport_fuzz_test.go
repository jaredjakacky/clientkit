package httpotel

import (
	"net/http"
	"net/url"
	"testing"
)

func FuzzSanitizedURLNeverIncludesUserinfoQueryOrFragment(f *testing.F) {
	for _, rawURL := range []string{
		"https://user:password@example.test/path?token=secret#fragment",
		"https://example.test/path?secret-name", "https://example.test/path?",
		"https://example.test/a%2Fb?x=y", "https:opaque?secret",
	} {
		f.Add(rawURL)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return
		}
		sanitized := sanitizedURL(&http.Request{URL: parsed})
		if parsed.Opaque != "" {
			if sanitized != "" {
				t.Fatalf("opaque URL %q sanitized to %q, want omission", rawURL, sanitized)
			}
			return
		}
		if sanitized == "" {
			return
		}
		result, err := url.Parse(sanitized)
		if err != nil {
			t.Fatalf("sanitized URL %q is invalid: %v", sanitized, err)
		}
		if result.User != nil || result.RawQuery != "" || result.ForceQuery || result.Fragment != "" || result.RawFragment != "" {
			t.Fatalf("sanitized URL retained sensitive components: %#v", result)
		}
	})
}
