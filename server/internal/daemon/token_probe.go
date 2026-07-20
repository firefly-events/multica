package daemon

import (
	"context"
	"os/exec"
	"strconv"
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

// token-guard defaults to its own 30s internal alarm (DOS-1036). Relying on
// that default matching our own backstop is a race: the outer exec.CommandContext
// timer starts at process spawn, while token-guard's inner `perl -e "alarm N"`
// only starts counting once the interpreter has booted — so two *equal*
// durations mean the outer timer reliably fires first, SIGKILLs the process,
// and turns a legitimate "timed out" (exit 124) verdict into an untraceable
// signal-kill that the switch below can't distinguish from a broken script
// (reproduced during DOS-1037 review). We close the race by owning both ends
// explicitly: pass token-guard a hard inner timeout well below our own outer
// backstop, instead of trusting the two scripts' defaults to stay apart.
const (
	tokenProbeInnerTimeout = 20 * time.Second
	tokenProbeExecTimeout  = 45 * time.Second
)

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
	// reason explains a "" (inconclusive) verdict for logging — empty for a
	// confirmed online/offline/ambiguous verdict, which already log elsewhere.
	reason string
}

// runTokenProbe shells out to scriptPath (bin/token-guard) for the given
// provider ("claude" or "codex") and maps its exit code to a verdict. The
// caller's context should already carry a hard deadline — token-guard has
// its own internal timeout (exit 124) but a broken script could still hang
// indefinitely without one here.
func runTokenProbe(ctx context.Context, scriptPath, provider string) tokenProbeVerdict {
	cmd := exec.CommandContext(ctx, scriptPath, provider, "--timeout", strconv.Itoa(int(tokenProbeInnerTimeout.Seconds())))
	err := cmd.Run()
	if err == nil {
		return tokenProbeVerdict{status: "online"}
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		// The probe never ran (missing binary, permissions, ctx already
		// cancelled) — inconclusive, not a confirmed failure.
		return tokenProbeVerdict{reason: "probe did not run: " + err.Error()}
	}
	switch exitErr.ExitCode() {
	case tokenProbeExitInvalid, tokenProbeExitTimeout:
		return tokenProbeVerdict{status: "offline"}
	case tokenProbeExitAmbiguous:
		return tokenProbeVerdict{ambiguous: true}
	case tokenProbeExitUsageErr:
		return tokenProbeVerdict{reason: "usage/setup error (exit 3)"}
	case -1:
		// Killed by our own outer timeout (tokenProbeExecTimeout) rather than
		// token-guard's inner alarm — the inner --timeout keeps this well
		// clear of the exit-124 case in normal operation, but a broken or
		// unresponsive script can still land here. Inconclusive, not offline.
		return tokenProbeVerdict{reason: "probe killed by outer exec timeout without an exit code"}
	default:
		return tokenProbeVerdict{reason: "undocumented exit code " + strconv.Itoa(exitErr.ExitCode())}
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
//
// NOTE: this still probes synchronously on a cache miss/staleness, bounded
// by tokenProbeExecTimeout — currentTokenStatus itself blocks its caller for
// up to that long. CodeRabbit's review flagged that this could serialize
// heartbeat/registration delivery for every runtime behind one runtime's
// cold probe; callers (sendWSHeartbeats in wakeup.go, and
// registerRuntimesForWorkspace in daemon.go) now run one goroutine per
// runtime/provider so a slow probe only delays its own runtime, not the
// others (DOS-1256). A given provider's own probe is still serialized
// against itself via tokenProbeMu.
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

	// The exec's own hard timeout is independent of the cache TTL. We pass
	// token-guard an explicit --timeout (tokenProbeInnerTimeout) well below
	// tokenProbeExecTimeout, so the outer backstop only fires for a script
	// that's hanging outside its own documented contract, not in the normal
	// exit-124 case.
	probeCtx, cancel := context.WithTimeout(ctx, tokenProbeExecTimeout)
	defer cancel()
	verdict := runTokenProbe(probeCtx, d.cfg.TokenProbeScript, provider)

	if verdict.ambiguous {
		d.logger.Warn("token probe ambiguous: Keychain-backed credential, verdict untrustworthy — leaving runtime status unchanged", "provider", provider)
	} else if verdict.status == "" && verdict.reason != "" {
		d.logger.Warn("token probe inconclusive: leaving runtime status unchanged", "provider", provider, "reason", verdict.reason)
	}

	d.tokenProbeMu.Lock()
	if d.tokenProbeCache == nil {
		d.tokenProbeCache = make(map[string]tokenProbeCacheEntry)
	}
	d.tokenProbeCache[provider] = tokenProbeCacheEntry{verdict: verdict, checkedAt: time.Now()}
	d.tokenProbeMu.Unlock()

	return verdict.status
}
