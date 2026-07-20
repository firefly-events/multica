package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeProbeScript writes a tiny shell script that exits with the given code,
// standing in for bin/token-guard's documented exit-code contract (DOS-1036,
// PR #67) without depending on the real script being installed.
func fakeProbeScript(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-token-guard")
	script := fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake probe script: %v", err)
	}
	return path
}

func TestRunTokenProbe_ExitCodeMapping(t *testing.T) {
	cases := []struct {
		name          string
		exitCode      int
		wantStatus    string
		wantAmbiguous bool
	}{
		{"exit 0 is a confirmed ok verdict", tokenProbeExitOK, "online", false},
		{"exit 2 is a confirmed invalid verdict", tokenProbeExitInvalid, "offline", false},
		{"exit 124 (timeout/hung auth prompt) is treated as a confirmed failure", tokenProbeExitTimeout, "offline", false},
		{"exit 5 is ambiguous and must not resolve to offline", tokenProbeExitAmbiguous, "", true},
		{"exit 3 (usage/setup error) is inconclusive, not a token failure", tokenProbeExitUsageErr, "", false},
		{"an undocumented exit code is treated as inconclusive, never offline", 17, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := fakeProbeScript(t, tc.exitCode)
			got := runTokenProbe(context.Background(), script, "claude")
			if got.status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.status, tc.wantStatus)
			}
			if got.ambiguous != tc.wantAmbiguous {
				t.Errorf("ambiguous = %v, want %v", got.ambiguous, tc.wantAmbiguous)
			}
		})
	}
}

func TestRunTokenProbe_MissingScriptIsInconclusive(t *testing.T) {
	got := runTokenProbe(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), "claude")
	if got.status != "" || got.ambiguous {
		t.Fatalf("got %+v, want a fully inconclusive verdict for a script that never ran", got)
	}
}

func newTestDaemonForProbe(scriptPath string, interval time.Duration) *Daemon {
	return &Daemon{
		cfg: Config{
			TokenProbeScript:   scriptPath,
			TokenProbeInterval: interval,
		},
		logger: slog.Default(),
	}
}

func TestCurrentTokenStatus_DisabledWhenScriptUnset(t *testing.T) {
	d := newTestDaemonForProbe("", time.Minute)
	if got := d.currentTokenStatus(context.Background(), "claude"); got != "" {
		t.Fatalf("currentTokenStatus with no script configured = %q, want \"\" (no-op)", got)
	}
}

// TestCurrentTokenStatus_CachesWithinInterval verifies the bounded re-probe
// cadence (DOS-1037 AC6): within TokenProbeInterval, a second call must not
// re-exec the probe. We can't observe exec calls directly, so we swap the
// script out from under the cache and confirm the stale (cached) verdict is
// still returned instead of the new script's verdict.
func TestCurrentTokenStatus_CachesWithinInterval(t *testing.T) {
	okScript := fakeProbeScript(t, tokenProbeExitOK)
	d := newTestDaemonForProbe(okScript, time.Hour)

	got := d.currentTokenStatus(context.Background(), "claude")
	if got != "online" {
		t.Fatalf("first probe status = %q, want online", got)
	}

	// Point the same provider at a script that would report offline. Since
	// the cache entry is still fresh (interval=1h), this must be ignored.
	d.cfg.TokenProbeScript = fakeProbeScript(t, tokenProbeExitInvalid)
	got = d.currentTokenStatus(context.Background(), "claude")
	if got != "online" {
		t.Fatalf("cached probe status = %q, want online (cache should not have re-probed)", got)
	}
}

// TestCurrentTokenStatus_ReprobesAfterIntervalExpires is the recovery half of
// AC6: once the cache entry goes stale, a runtime whose token has since
// become valid again is automatically re-probed and flips back — no manual
// status flip required.
func TestCurrentTokenStatus_ReprobesAfterIntervalExpires(t *testing.T) {
	badScript := fakeProbeScript(t, tokenProbeExitInvalid)
	d := newTestDaemonForProbe(badScript, 30*time.Millisecond)

	if got := d.currentTokenStatus(context.Background(), "claude"); got != "offline" {
		t.Fatalf("first probe status = %q, want offline", got)
	}

	time.Sleep(40 * time.Millisecond)
	d.cfg.TokenProbeScript = fakeProbeScript(t, tokenProbeExitOK)
	if got := d.currentTokenStatus(context.Background(), "claude"); got != "online" {
		t.Fatalf("re-probed status after cache expiry = %q, want online", got)
	}
}

func TestCurrentTokenStatus_AmbiguousVerdictReturnsNoOp(t *testing.T) {
	script := fakeProbeScript(t, tokenProbeExitAmbiguous)
	d := newTestDaemonForProbe(script, time.Hour)

	if got := d.currentTokenStatus(context.Background(), "claude"); got != "" {
		t.Fatalf("ambiguous verdict returned status %q, want \"\" (leave runtime status unchanged)", got)
	}
}
