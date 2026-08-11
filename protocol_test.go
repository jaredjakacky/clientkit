package clientkit_test

import (
	"strings"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
)

func TestValidateClientProtocol(t *testing.T) {
	if clientkit.MaxClientProtocolBytes != 32 {
		t.Fatalf("MaxClientProtocolBytes = %d, want 32", clientkit.MaxClientProtocolBytes)
	}

	valid := []string{
		"h",
		"http",
		"tcp",
		"http2",
		"grpc-web",
		"postgres_wire",
		"custom.v2",
		strings.Repeat("a", clientkit.MaxClientProtocolBytes),
	}
	for _, value := range valid {
		t.Run("valid "+value, func(t *testing.T) {
			if err := clientkit.ValidateClientProtocol(value); err != nil {
				t.Fatalf("ValidateClientProtocol(%q) error = %v", value, err)
			}
		})
	}

	invalid := []string{
		"",
		" http",
		"http ",
		"HTTP",
		"-http",
		"http-",
		"_tcp",
		"tcp_",
		".http",
		"http.",
		"http..v2",
		"http api",
		"http/api",
		"http:api",
		"hțțp",
		strings.Repeat("a", clientkit.MaxClientProtocolBytes+1),
	}
	for _, value := range invalid {
		t.Run("invalid "+value, func(t *testing.T) {
			if err := clientkit.ValidateClientProtocol(value); err == nil {
				t.Fatalf("ValidateClientProtocol(%q) error = nil, want validation failure", value)
			}
		})
	}
}
