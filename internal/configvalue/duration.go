// Package configvalue centralizes shared configuration-value normalization.
package configvalue

import (
	"fmt"
	"time"
)

// Duration resolves a non-negative duration whose zero value selects a default
// and whose explicit disabled state selects a caller-defined disabled value.
func Duration(name string, value time.Duration, disabled bool, defaultValue, disabledValue time.Duration) (time.Duration, error) {
	if value < 0 {
		return 0, fmt.Errorf("clientkit: %s must not be negative", name)
	}
	if disabled && value > 0 {
		return 0, fmt.Errorf("clientkit: %s cannot be set when disabled", name)
	}
	if disabled {
		return disabledValue, nil
	}
	if value == 0 {
		return defaultValue, nil
	}
	return value, nil
}
