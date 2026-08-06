package httpclient

import "errors"

// MaxOperationNameBytes is the maximum encoded length of an OperationName.
const MaxOperationNameBytes = 64

// OperationName is a stable, low-cardinality application-defined identifier
// for one logical outbound HTTP operation, such as "payments.create",
// "payments.lookup", or "catalog.search". Its zero value uses the generic
// OperationHTTPRequest name.
//
// Custom names must use a fixed vocabulary declared in application source.
// They must start with a lowercase ASCII letter, end with a lowercase ASCII
// letter or digit, and contain only lowercase ASCII letters, digits, periods,
// underscores, and hyphens. URLs, paths, query parameters, user or tenant IDs,
// request or correlation IDs, random values, dynamic resource identifiers, and
// other user or unbounded input are prohibited even when they fit the syntax.
//
// The name appears in operation, attempt, and retry telemetry, including spans,
// metrics, and structured logs. It does not affect execution or retry behavior,
// is not sent to the remote service, does not mutate Client, and is safe to vary
// across concurrent operations.
type OperationName string

func validateOperationName(name OperationName) error {
	if name == "" {
		return nil
	}
	if len(name) > MaxOperationNameBytes {
		return errors.New("clientkit: HTTP operation name exceeds 64 bytes")
	}
	if name[0] < 'a' || name[0] > 'z' {
		return errors.New("clientkit: HTTP operation name must start with a lowercase letter")
	}

	for index := 0; index < len(name); index++ {
		value := name[index]
		if (value >= 'a' && value <= 'z') ||
			(value >= '0' && value <= '9') ||
			value == '.' || value == '_' || value == '-' {
			continue
		}
		return errors.New("clientkit: HTTP operation name contains an invalid character")
	}

	last := name[len(name)-1]
	if (last < 'a' || last > 'z') && (last < '0' || last > '9') {
		return errors.New("clientkit: HTTP operation name must end with a letter or digit")
	}

	return nil
}

func resolveOperationName(name OperationName) (string, error) {
	if err := validateOperationName(name); err != nil {
		return "", err
	}
	if name == "" {
		return OperationHTTPRequest, nil
	}
	return string(name), nil
}
