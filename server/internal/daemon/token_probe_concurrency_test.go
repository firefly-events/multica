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

// barrierPollInterval and barrierMaxWait tune barrierProbeScript. maxWait is
// deliberately generous (headroom over scheduler jitter, not over any
// artificial "probe work" duration) — see barrierProbeScript for why the gap
// between the concurrent and serial cases is large regardless of its exact
// value.
const (
	barrierPollInterval = 20 * time.Millisecond
	barrierMaxWait      = 2 * time.Second
)

// barrierProbeScript writes a fake token-guard that turns "did these
// providers' probes actually run at the same time" into a structural
// deadlock/no-deadlock question instead of a wall-clock measurement. When
// invoked for provider p, the script touches "<barrierDir>/p.started" and
// then spins polling barrierDir until it observes a start marker from every
// provider in providers (or gives up after barrierMaxWait).
//
// A genuinely concurrent fan-out (one goroutine/process per provider, as
// production code does) launches all probes at once: each writes its own
// marker almost immediately, observes the others' markers within a handful
// of poll intervals, and exits — total wall time on the order of tens of
// milliseconds no matter how slow the machine is, because nothing here
// depends on an artificial sleep duration.
//
// A regression to serial execution can never resolve the barrier before
// giving up: the second probe can't even start (let alone write its marker)
// until the first probe's exec.Cmd.Run() returns, which won't happen until
// the first probe sees marker 2 — which will never come. So a serial caller
// burns the full barrierMaxWait on each probe but the last, i.e. at least
// (len(providers)-1)*barrierMaxWait in total. That gap (tens of ms vs
// seconds) is what the test asserts on, not a tight multiplier/slack bound
// tied to a real probe's execution time — this is why it stays robust
// instead of flaking as the old probeDelay-relative bound did.
func barrierProbeScript(t *testing.T, providers []string, barrierDir string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-token-guard")

	maxPolls := int(barrierMaxWait / barrierPollInterval)
	pollSeconds := fmt.Sprintf("%.3f", barrierPollInterval.Seconds())
	script := fmt.Sprintf(`#!/bin/sh
touch "%s/$1.started"
i=0
while [ "$(ls "%s"/*.started 2>/dev/null | wc -l)" -lt %d ]; do
  i=$((i+1))
  if [ "$i" -ge %d ]; then
    exit 0
  fi
  sleep %s
done
exit 0
`, barrierDir, barrierDir, len(providers), maxPolls, pollSeconds)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write barrier probe script: %v", err)
	}
	return path
}

// TestSendWSHeartbeats_SlowProbeDoesNotDelayOtherRuntimes is DOS-1255 AC3: a
// slow/cold token-probe cache entry for one runtime must not delay heartbeat
// delivery for other runtimes on the same daemon beyond a small constant
// bound. Three runtimes each probe a distinct provider; the fake probes
// rendezvous on a shared barrier (see barrierProbeScript) so the assertion
// below is about whether the goroutine-per-runtime fan-out in
// sendWSHeartbeats (wakeup.go) actually ran them concurrently, not about
// landing close to a hand-picked timing threshold (DOS-1266).
func TestSendWSHeartbeats_SlowProbeDoesNotDelayOtherRuntimes(t *testing.T) {
	providers := []string{"p1", "p2", "p3"}
	barrierDir := t.TempDir()
	script := barrierProbeScript(t, providers, barrierDir)

	d := freshDaemon("http://unused.invalid")
	d.cfg.TokenProbeScript = script
	d.cfg.TokenProbeInterval = time.Hour

	runtimeIDs := []string{"rt-1", "rt-2", "rt-3"}
	for i, rid := range runtimeIDs {
		d.runtimeIndex[rid] = Runtime{ID: rid, Provider: providers[i]}
	}

	writes := make(chan []byte, len(runtimeIDs))

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		d.sendWSHeartbeats(context.Background(), runtimeIDs, writes)
	}()

	select {
	case <-done:
	case <-time.After(barrierMaxWait):
		t.Fatalf("sendWSHeartbeats did not return within %s; a concurrent fan-out resolves the probe barrier in a handful of poll intervals, so this only trips on a regression to serial probing", barrierMaxWait)
	}
	elapsed := time.Since(start)

	// A serial regression burns at least (len(runtimeIDs)-1)*barrierMaxWait
	// before it can finish; a concurrent fan-out resolves the barrier almost
	// immediately. barrierMaxWait itself is a huge margin above either case.
	if elapsed >= barrierMaxWait {
		t.Fatalf("sendWSHeartbeats took %s, at or beyond the %s barrier timeout; probes did not run concurrently", elapsed, barrierMaxWait)
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
// small constant bound. Same barrier-based rendezvous and reasoning as
// TestSendWSHeartbeats_SlowProbeDoesNotDelayOtherRuntimes (DOS-1266).
func TestRegisterRuntimesForWorkspace_SlowProbeDoesNotDelayOtherProviders(t *testing.T) {
	// Not t.Parallel: stubAgentVersion mutates package-level vars.
	providers := []string{"p1", "p2", "p3"}
	barrierDir := t.TempDir()
	script := barrierProbeScript(t, providers, barrierDir)

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

	type result struct {
		err error
	}
	done := make(chan result)
	start := time.Now()
	go func() {
		_, err := d.registerRuntimesForWorkspace(context.Background(), "ws-1")
		done <- result{err: err}
	}()

	var res result
	select {
	case res = <-done:
	case <-time.After(barrierMaxWait):
		t.Fatalf("registerRuntimesForWorkspace did not return within %s; a concurrent fan-out resolves the probe barrier in a handful of poll intervals, so this only trips on a regression to serial probing", barrierMaxWait)
	}
	if res.err != nil {
		t.Fatalf("registerRuntimesForWorkspace: %v", res.err)
	}
	elapsed := time.Since(start)

	// A serial regression burns at least (len(providers)-1)*barrierMaxWait
	// before it can finish; a concurrent fan-out resolves the barrier almost
	// immediately. barrierMaxWait itself is a huge margin above either case.
	if elapsed >= barrierMaxWait {
		t.Fatalf("registerRuntimesForWorkspace took %s, at or beyond the %s barrier timeout; probes did not run concurrently", elapsed, barrierMaxWait)
	}
}
