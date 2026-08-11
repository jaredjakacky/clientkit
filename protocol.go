package clientkit

import (
	"errors"
	"fmt"
	"strings"
)

// MaxClientProtocolBytes is the maximum byte length accepted by
// ValidateClientProtocol.
const MaxClientProtocolBytes = 32

// ValidateClientProtocol verifies a stable, low-cardinality, telemetry-safe
// client-family category. Protocols must contain 1 through
// MaxClientProtocolBytes lowercase ASCII letters, digits, periods,
// underscores, or hyphens; must begin and end with a letter or digit; and must
// not contain consecutive periods.
//
// A protocol identifies the concrete client family, such as "http" or "tcp".
// It must not contain an endpoint, URL, address, connection string, tenant, or
// other sensitive or high-cardinality configuration.
func ValidateClientProtocol(protocol string) error {
	if protocol == "" {
		return errors.New("clientkit: protocol is required")
	}
	if strings.TrimSpace(protocol) != protocol {
		return errors.New("clientkit: protocol must not include surrounding whitespace")
	}
	if len(protocol) > MaxClientProtocolBytes {
		return fmt.Errorf("clientkit: protocol exceeds %d bytes", MaxClientProtocolBytes)
	}
	if strings.Contains(protocol, "..") || !clientNameAlphanumeric(protocol[0]) || !clientNameAlphanumeric(protocol[len(protocol)-1]) {
		return fmt.Errorf("clientkit: invalid protocol %q", protocol)
	}
	for index := 0; index < len(protocol); index++ {
		value := protocol[index]
		if clientNameAlphanumeric(value) || value == '-' || value == '_' || value == '.' {
			continue
		}
		return fmt.Errorf("clientkit: invalid protocol %q", protocol)
	}
	return nil
}
