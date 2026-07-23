package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
)

const (
	runtimeAutonomySupervised = "supervised"
	runtimeAutonomyFullAccess = "full-access"
)

func decodeRuntimeAutonomyMode(raw json.RawMessage, logger *slog.Logger) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return runtimeAutonomySupervised
	}
	var cfg struct {
		AutonomyMode string `json:"autonomy_mode"`
	}
	if err := json.Unmarshal(trimmed, &cfg); err != nil {
		if logger != nil {
			logger.Warn("runtime autonomy: invalid runtime_config; defaulting to supervised", "error", err)
		}
		return runtimeAutonomySupervised
	}
	switch cfg.AutonomyMode {
	case "", runtimeAutonomySupervised:
		return runtimeAutonomySupervised
	case runtimeAutonomyFullAccess:
		return runtimeAutonomyFullAccess
	default:
		if logger != nil {
			logger.Warn("runtime autonomy: unknown mode; defaulting to supervised", "autonomy_mode", cfg.AutonomyMode)
		}
		return runtimeAutonomySupervised
	}
}
