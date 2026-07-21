package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// dispatchingProbeScript writes a fake token-guard that sleeps for
// probeDelay when invoked for any provider name in slowProviders, and
// returns immediately (exit 0) otherwise. This lets a test pin exactly which
// providers/runtimes are "cold" without depending on the real script.
func dispatchingProbeScript(t *testing.T, slowProviders []string, probeDelay time.Duration) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-token-guard")

	// probeDelay in seconds, formatted plainly (e.g. "0.30") for `sleep`.
	caseArms := ""
	for _, p := range slowProviders {
		caseArms += fmt.Sprintf("  %s) sleep %.2f ;;\n", p, probeDelay.Seconds())
	}
	script := "#!/bin/sh\ncase \"$1\" in\n" + caseArms + "  *) ;;\nesac\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write dispatching probe script: %v", err)
	}
	return path
}

// TestSendWSHeartbeats_SlowProbeDoesNotDelayOtherRuntimes is DOS-1255 AC3: a
// slow/cold token-probe cache entry for one runtime must not delay heartbeat
// delivery for other runtimes on the same daemon beyond a small constant
// bound. Three runtimes each probe a distinct provider; every probe is slow
// (probeDelay). Serial execution would take ~3*probeDelay; the goroutine-
// per-runtime fan-out in sendWSHeartbeats (wakeup.go) must keep total elapsed
// close to a single probeDelay.
func TestSendWSHeartbeats_SlowProbeDoesNotDelayOtherRuntimes(t *testing.T) {
	const probeDelay = 300 * time.Millisecond
	script := dispatchingProbeScript(t, []string{"p1", "p2", "p3"}, probeDelay)

	d := freshDaemon("http://unused.invalid")
	d.cfg.TokenProbeScript = script
	d.cfg.TokenProbeInterval = time.Hour

	runtimeIDs := []string{"rt-1", "rt-2", "rt-3"}
	providers := []string{"p1", "p2", "p3"}
	for i, rid := range runtimeIDs {
		d.runtimeIndex[rid] = Runtime{ID: rid, Provider: providers[i]}
	}

	writes := make(chan []byte, len(runtimeIDs))

	start := time.Now()
	d.sendWSHeartbeats(context.Background(), runtimeIDs, writes)
	elapsed := time.Since(start)

	// Serial execution of 3 slow probes would take ~900ms; the concurrent
	// fan-out should land close to a single probeDelay. Bound generously at
	// 2*probeDelay to absorb scheduling jitter without masking a regression
	// back to serial behavior.
	if elapsed > 2*probeDelay {
		t.Fatalf("sendWSHeartbeats took %s for 3 runtimes each with a %s probe; want close to one probeDelay (concurrent), not the serial sum", elapsed, probeDelay)
	}

	close(writes)
	var frames int
	for range writes {
		frames++
	}
	if frames != len(runtimeIDs) {
		t.Fatalf("got %d heartbeat frames, want %d", frames, len(runtimeIDs))
	}
}

// TestRegisterRuntimesForWorkspace_SlowProbeDoesNotDelayOtherProviders is the
// registration-path half of DOS-1255 AC3: a slow/cold probe for one
// configured provider must not delay registration for the others beyond a
// small constant bound.
func TestRegisterRuntimesForWorkspace_SlowProbeDoesNotDelayOtherProviders(t *testing.T) {
	// Not t.Parallel: stubAgentVersion mutates package-level vars.
	const probeDelay = 300 * time.Millisecond
	script := dispatchingProbeScript(t, []string{"p1", "p2", "p3"}, probeDelay)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/daemon/register" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(RegisterResponse{
				Runtimes: []Runtime{{ID: "rt-1", Name: "P1", Provider: "p1", Status: "online"}},
				Repos:    []RepoData{},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	d := freshDaemon(srv.URL)
	d.cfg.TokenProbeScript = script
	d.cfg.TokenProbeInterval = time.Hour
	d.cfg.Agents = map[string]AgentEntry{
		"p1": {Path: "/usr/bin/true"},
		"p2": {Path: "/usr/bin/true"},
		"p3": {Path: "/usr/bin/true"},
	}
	t.Cleanup(stubAgentVersion(t))

	start := time.Now()
	if _, err := d.registerRuntimesForWorkspace(context.Background(), "ws-1"); err != nil {
		t.Fatalf("registerRuntimesForWorkspace: %v", err)
	}
	elapsed := time.Since(start)

	// Serial execution of 3 slow probes would take ~900ms; the concurrent
	// fan-out in registerRuntimesForWorkspace (daemon.go) should land close
	// to a single probeDelay.
	if elapsed > 2*probeDelay {
		t.Fatalf("registerRuntimesForWorkspace took %s for 3 providers each with a %s probe; want close to one probeDelay (concurrent), not the serial sum", elapsed, probeDelay)
	}
}
