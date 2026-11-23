package service

import (
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
)

// PriorityAgingPerHourEnv configures queue aging (pantheon-v2#589).
const PriorityAgingPerHourEnv = "MULTICA_TASK_PRIORITY_AGING_PER_HOUR"

// DefaultPriorityAgingPerHour is the priority boost a queued task earns for
// every hour it waits. Priorities are 0 (none) to 4 (urgent), so at 1.0 a
// medium task that has waited an hour ties a fresh high task and passes it
// after that: lower-priority work is delayed, never starved.
const DefaultPriorityAgingPerHour = 1.0

// PriorityAgingPerHour is read once at startup. ClaimAgentTask orders an
// agent's queued tasks by priority + PriorityAgingPerHour * hours waited.
// 0 restores strict priority-then-age order.
var PriorityAgingPerHour = parsePriorityAgingPerHour(os.Getenv(PriorityAgingPerHourEnv))

func parsePriorityAgingPerHour(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultPriorityAgingPerHour
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		slog.Warn("invalid "+PriorityAgingPerHourEnv+"; using default",
			"value", raw, "default", DefaultPriorityAgingPerHour)
		return DefaultPriorityAgingPerHour
	}
	return v
}
