package tcpclient

import (
	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/opskit"
)

const (
	// ProtocolTCP identifies the TCP client family in registry inspection and
	// observer events.
	ProtocolTCP = "tcp"
	// OperationDial identifies a raw connection-establishment operation.
	OperationDial = "dial"
)

func (c *Client) clientObserver() clientkit.Observer {
	if c == nil || c.core == nil {
		return clientkit.NopObserver{}
	}
	return c.core.Observer()
}

func (c *Client) telemetryClientName() string {
	if c == nil || c.core == nil {
		return ""
	}
	return c.Name()
}

func telemetryNetwork(network string) string {
	// Dialing retains the configured network; telemetry exposes only the bounded
	// built-in vocabulary plus one value for custom dialers.
	switch network {
	case "tcp", "tcp4", "tcp6":
		return network
	default:
		return "custom"
	}
}

func (c *Client) eventAttributes(tlsVersion string) []opskit.Attribute {
	network := ""
	security := "plaintext"
	if c != nil {
		network = c.network
		if c.tls.enabled {
			security = "tls"
		} else if c.dialContext != nil {
			// A custom dialer may return an already-secured connection. Clientkit
			// cannot inspect that policy, so it must not label it plaintext.
			security = "custom"
		}
	}
	attributes := []opskit.Attribute{
		opskit.Attr("clientkit.network", telemetryNetwork(network)),
		opskit.Attr("client.security", security),
	}
	if tlsVersion != "" {
		attributes = append(attributes, opskit.Attr("tls.version", tlsVersion))
	}
	return attributes
}

func (c *Client) healthEventAttributes(tlsVersion string) []opskit.Attribute {
	return append(c.eventAttributes(tlsVersion), opskit.Attr("client.operation", "health_check"))
}
