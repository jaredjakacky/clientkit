package slogobserver

import "log/slog"

// LevelConfig defines the immutable level used for each Clientkit record type.
type LevelConfig struct {
	// OperationSuccess is used for successfully completed operations.
	OperationSuccess slog.Level
	// OperationFailure is used for operations that did not complete successfully.
	OperationFailure slog.Level
	// Attempt is used for every completed execution attempt.
	Attempt slog.Level
	// Retry is used when another attempt has been scheduled.
	Retry slog.Level
	// HealthHealthy is used for healthy check results.
	HealthHealthy slog.Level
	// HealthUnhealthy is used for degraded, unhealthy, and unknown check results.
	HealthUnhealthy slog.Level
}

// DefaultLevelConfig returns production-safe logging levels. Successful
// operations, attempts, and healthy checks use Debug; retries and degraded,
// unhealthy, or unknown checks use Warn; final operation failures use Error.
func DefaultLevelConfig() LevelConfig {
	return LevelConfig{
		OperationSuccess: slog.LevelDebug,
		OperationFailure: slog.LevelError,
		Attempt:          slog.LevelDebug,
		Retry:            slog.LevelWarn,
		HealthHealthy:    slog.LevelDebug,
		HealthUnhealthy:  slog.LevelWarn,
	}
}
