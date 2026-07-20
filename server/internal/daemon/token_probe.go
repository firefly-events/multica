package daemon

import (
	"context"
	"os/exec"
	"time"
)

// Exit codes documented by bin/token-guard (DOS-1036, PR #67).
const (
	tokenProbeExitOK        = 0
	tokenProbeExitInvalid   = 2
	tokenProbeExitTimeout   = 124
	tokenProbeExitUsageErr  = 3
	tokenProbeExitAmbiguous = 5
)

// tokenProbeExecTimeout bounds a single token-guard invocation. token-guard
// already wraps itself in a hard wall-clock timeout (DOS-1036) well under
// this value; this is only a backstop against the script hanging outside
// that contract (e.g. a broken fork of the script).
const tokenProbeExecTimeout = 30 * time.Second

// tokenProbeVerdict is the outcome of one token-guard run, mapped to a
// dispatch-eligibility direction. Status is "" for anything that isn't a
// confirmed ok/invalid verdict — ambiguous (exit 5, Keychain-backed
// credential the script can't fully bypass) and usage/setup errors (exit 3,
// or any exit the probe never documented) are both "the probe couldn't tell
// us", not "the token is dead". Silently mapping either to offline would
// reintroduce the false-positive failure mode this feature exists to avoid;
// callers must leave the runtime's current status untouched instead.
type tokenProbeVerdict struct {
	status    string // "online", "offline", or "" (no verdict, leave unchanged)
	ambiguous bool
}

// runTokenProbe shells out to scriptPath (bin/token-guard) for the given
// provider ("claude" or "codex") and maps its exit code to a verdict. The
// caller's context should already carry a hard deadline — token-guard has
// its own internal timeout (exit 124) but a broken script could still hang
// indefinitely without one here.
func runTokenProbe(ctx context.Context, scriptPath, provider string) tokenProbeVerdict {
	cmd := exec.CommandContext(ctx, scriptPath, provider)
	err := cmd.Run()
	if err == nil {
		return tokenProbeVerdict{status: "online"}
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		// The probe never ran (missing binary, permissions, ctx already
		// cancelled) — inconclusive, not a confirmed failure.
		return tokenProbeVerdict{}
	}
	switch exitErr.ExitCode() {
	case tokenProbeExitInvalid, tokenProbeExitTimeout:
		return tokenProbeVerdict{status: "offline"}
	case tokenProbeExitAmbiguous:
		return tokenProbeVerdict{ambiguous: true}
	case tokenProbeExitUsageErr:
		return tokenProbeVerdict{}
	default:
		return tokenProbeVerdict{}
	}
}

// tokenProbeCacheEntry is the cached verdict for one provider, so the
// daemon doesn't re-exec the probe CLI on every heartbeat tick.
type tokenProbeCacheEntry struct {
	verdict   tokenProbeVerdict
	checkedAt time.Time
}

// currentTokenStatus returns the dispatch-eligibility status implied by the
// most recent token-guard verdict for provider, re-probing if the cached
// entry is stale (or absent). Returns "" when probing is disabled
// (TokenProbeScript unset — no behavior change, DOS-1037 AC7) or when the
// latest verdict is inconclusive; callers must treat "" as "leave the
// runtime's current status alone", never as "offline".
func (d *Daemon) currentTokenStatus(ctx context.Context, provider string) string {
	if d.cfg.TokenProbeScript == "" || provider == "" {
		return ""
	}

	interval := d.cfg.TokenProbeInterval
	if interval <= 0 {
		interval = DefaultTokenProbeInterval
	}

	d.tokenProbeMu.Lock()
	entry, ok := d.tokenProbeCache[provider]
	fresh := ok && time.Since(entry.checkedAt) < interval
	d.tokenProbeMu.Unlock()
	if fresh {
		return entry.verdict.status
	}

	// The exec's own hard timeout is independent of the cache TTL — token-guard
	// already enforces its own internal alarm (exit 124) well under this, so
	// tokenProbeExecTimeout only needs to guard against the script itself
	// hanging outside that contract.
	probeCtx, cancel := context.WithTimeout(ctx, tokenProbeExecTimeout)
	defer cancel()
	verdict := runTokenProbe(probeCtx, d.cfg.TokenProbeScript, provider)

	if verdict.ambiguous {
		d.logger.Warn("token probe ambiguous: Keychain-backed credential, verdict untrustworthy — leaving runtime status unchanged", "provider", provider)
	}

	d.tokenProbeMu.Lock()
	if d.tokenProbeCache == nil {
		d.tokenProbeCache = make(map[string]tokenProbeCacheEntry)
	}
	d.tokenProbeCache[provider] = tokenProbeCacheEntry{verdict: verdict, checkedAt: time.Now()}
	d.tokenProbeMu.Unlock()

	return verdict.status
}
