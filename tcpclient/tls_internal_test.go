package tcpclient

import "testing"

func TestConfiguredAddressHost(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{name: "DNS", address: "example.test:443", want: "example.test"},
		{name: "IPv4", address: "127.0.0.1:443", want: "127.0.0.1"},
		{name: "IPv6", address: "[2001:db8::1]:443", want: "2001:db8::1"},
		{name: "scoped IPv6", address: "[fe80::1%eth0]:443", want: "fe80::1"},
		{name: "non-IP percent", address: "service%tenant:443", want: "service%tenant"},
		{name: "invalid", address: "example.test", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := configuredAddressHost(test.address); got != test.want {
				t.Fatalf("configuredAddressHost(%q) = %q, want %q", test.address, got, test.want)
			}
		})
	}
}
