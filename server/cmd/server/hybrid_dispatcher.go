package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

const hybridDispatcherInterval = 2 * time.Second

func runHybridDispatcher(ctx context.Context, taskSvc *service.TaskService, cfg service.HybridDispatchConfig) {
	if taskSvc == nil {
		return
	}
	cfg.ClearStartupLocks = true
	runHybridDispatcherTick(ctx, taskSvc, cfg)
	cfg.ClearStartupLocks = false

	ticker := time.NewTicker(hybridDispatcherInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runHybridDispatcherTick(ctx, taskSvc, cfg)
		}
	}
}

func runHybridDispatcherTick(ctx context.Context, taskSvc *service.TaskService, cfg service.HybridDispatchConfig) {
	result, err := taskSvc.RunHybridDispatchTick(ctx, cfg)
	if err != nil {
		slog.Warn("hybrid dispatcher: tick failed", "error", err)
		return
	}
	if !result.LeaseAcquired {
		return
	}
	if len(result.StartupCleared) > 0 || len(result.Reaped) > 0 || len(result.Dispatched) > 0 {
		slog.Info("hybrid dispatcher: tick complete",
			"startup_cleared", len(result.StartupCleared),
			"reaped", len(result.Reaped),
			"dispatched", len(result.Dispatched),
		)
	}
}

func envHybridDispatchConfig() service.HybridDispatchConfig {
	return service.HybridDispatchConfig{
		PoolLimit:       envPositiveInt("MULTICA_HYBRID_DISPATCH_POOL_LIMIT", service.HybridDispatchDefaultPoolLimit),
		BatchLimit:      envPositiveInt("MULTICA_HYBRID_DISPATCH_BATCH_SIZE", service.HybridDispatchDefaultBatchSize),
		MaxFastFailures: envPositiveInt("MULTICA_HYBRID_CIRCUIT_MAX_FAST_FAILURES", service.HybridCircuitMaxFastFailures),
		StaleTTL:        envDuration("MULTICA_HYBRID_DISPATCH_STALE_TTL", service.HybridDispatchStaleTTL),
		CircuitWindow:   envDuration("MULTICA_HYBRID_CIRCUIT_WINDOW", service.HybridCircuitWindow),
	}
}
