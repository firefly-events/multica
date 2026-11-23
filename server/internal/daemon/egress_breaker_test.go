package daemon

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestBreaker() (*egressBreaker, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)}
	return newEgressBreaker(clk.now), clk
}

func TestEgressBreakerOpensOnFailuresAcrossAgents(t *testing.T) {
	b, clk := newTestBreaker()
	if opened, deg := b.RecordFailure("rt1", "agentA"); opened || deg {
		t.Fatal("first failure must not open")
	}
	clk.advance(20 * time.Second)
	if opened, deg := b.RecordFailure("rt1", "agentB"); opened || deg {
		t.Fatal("second failure must not open")
	}
	clk.advance(20 * time.Second)
	opened, deg := b.RecordFailure("rt1", "agentA")
	if !opened || !deg {
		t.Fatalf("third failure across 2 agents in window: opened=%v degraded=%v, want true/true", opened, deg)
	}
	if !b.IsDegraded("rt1") || b.IsDegraded("rt2") {
		t.Fatal("only rt1 should be degraded")
	}
	// Further failures while open are degraded but do not re-open.
	if opened, deg := b.RecordFailure("rt1", "agentC"); opened || !deg {
		t.Fatalf("failure while open: opened=%v degraded=%v, want false/true", opened, deg)
	}
	if got := b.FilterHealthy([]string{"rt1", "rt2"}); !reflect.DeepEqual(got, []string{"rt2"}) {
		t.Fatalf("FilterHealthy = %v, want [rt2]", got)
	}
}

func TestEgressBreakerSingleAgentDoesNotOpen(t *testing.T) {
	b, clk := newTestBreaker()
	for range 10 {
		if opened, _ := b.RecordFailure("rt1", "agentA"); opened {
			t.Fatal("one agent's failures must not open the runtime breaker")
		}
		clk.advance(time.Second)
	}
}

func TestEgressBreakerWindowExpires(t *testing.T) {
	b, clk := newTestBreaker()
	b.RecordFailure("rt1", "agentA")
	b.RecordFailure("rt1", "agentB")
	clk.advance(egressTripWindow + time.Second)
	if opened, _ := b.RecordFailure("rt1", "agentA"); opened {
		t.Fatal("failures older than the window must not count")
	}
	// Failures are per runtime.
	b.RecordFailure("rt2", "agentA")
	b.RecordFailure("rt2", "agentB")
	if b.IsDegraded("rt1") {
		t.Fatal("rt2 failures must not open rt1")
	}
}

func TestEgressBreakerProbeCloses(t *testing.T) {
	b, clk := newTestBreaker()
	b.RecordFailure("rt1", "a")
	b.RecordFailure("rt1", "b")
	b.RecordFailure("rt1", "a")

	clk.advance(30 * time.Second)
	if closed, _ := b.RecordProbe("rt1", errors.New("dns: lookup api.anthropic.com: server misbehaving")); closed {
		t.Fatal("failed probe must keep breaker open")
	}
	snap := b.Snapshot()
	if !snap.EgressDegraded || len(snap.Runtimes) != 1 || snap.Runtimes[0].LastError == "" || snap.Runtimes[0].Cause != egressCauseFailures {
		t.Fatalf("snapshot while open = %+v", snap)
	}

	clk.advance(30 * time.Second)
	closed, downFor := b.RecordProbe("rt1", nil)
	if !closed || downFor != time.Minute {
		t.Fatalf("passing probe: closed=%v downFor=%s, want true/1m", closed, downFor)
	}
	if b.IsDegraded("rt1") || b.Snapshot().EgressDegraded {
		t.Fatal("breaker should be closed")
	}
	// Closing resets the failure history: one new failure does not re-open.
	if opened, _ := b.RecordFailure("rt1", "a"); opened {
		t.Fatal("history must reset on close")
	}
}

func TestEgressBreakerNilIsDisabled(t *testing.T) {
	var b *egressBreaker
	if opened, deg := b.RecordFailure("rt", "a"); opened || deg {
		t.Fatal("nil breaker must never open")
	}
	ids := []string{"rt"}
	if got := b.FilterHealthy(ids); !reflect.DeepEqual(got, ids) {
		t.Fatal("nil breaker must not filter")
	}
	if b.Snapshot() != nil || b.IsDegraded("rt") {
		t.Fatal("nil breaker must report nothing")
	}
	b.Gate(ids, egressCausePreflight)
	b.Forget(nil)
}

func newEgressTestDaemon() (*Daemon, *fakeClock) {
	b, clk := newTestBreaker()
	d := &Daemon{
		logger: slog.New(slog.DiscardHandler),
		egress: b,
		runtimeIndex: map[string]Runtime{
			"rt-claude": {ID: "rt-claude", Provider: "claude"},
			"rt-claw":   {ID: "rt-claw", Provider: "openclaw"},
		},
		workspaces: map[string]*workspaceState{
			"ws": {runtimeIDs: []string{"rt-claude", "rt-claw"}},
		},
	}
	return d, clk
}

// Startup preflight: every runtime is gated until its first probe passes, and
// a failing probe keeps it gated (wait + log, never fail).
func TestEgressPreflightGatesUntilProbePasses(t *testing.T) {
	t.Setenv("MULTICA_EGRESS_PROBE_TARGETS", "")
	d, _ := newEgressTestDaemon()
	d.egress.Gate(d.allRuntimeIDs(), egressCausePreflight)
	if got := d.egress.FilterHealthy(d.allRuntimeIDs()); len(got) != 0 {
		t.Fatalf("preflight must gate all runtimes, healthy = %v", got)
	}

	var probed []egressTargets
	down := func(_ context.Context, tg egressTargets) error {
		probed = append(probed, tg)
		return errors.New("dns: temporary failure in name resolution")
	}
	if resumed := d.egressProbeRound(context.Background(), down); len(resumed) != 0 {
		t.Fatalf("resumed during outage: %v", resumed)
	}
	if len(probed) != 2 {
		t.Fatalf("want one probe per distinct target set (claude, generic), got %d", len(probed))
	}

	up := func(context.Context, egressTargets) error { return nil }
	resumed := d.egressProbeRound(context.Background(), up)
	if !reflect.DeepEqual(resumed, []string{"rt-claude", "rt-claw"}) {
		t.Fatalf("resumed = %v", resumed)
	}
	if got := d.egress.FilterHealthy(d.allRuntimeIDs()); len(got) != 2 {
		t.Fatalf("all runtimes should claim again, healthy = %v", got)
	}
}

// The tripping failure and every egress failure while open are reported as
// egress_degraded; failures before the trip and unrelated failures are not.
func TestApplyEgressBreakerRewritesReason(t *testing.T) {
	d, _ := newEgressTestDaemon()
	log := slog.New(slog.DiscardHandler)
	const dnsErr = "getaddrinfo EAI_AGAIN generativelanguage.googleapis.com; gemini exited with error: exit status 1"
	fail := func(agent, comment, reason string) TaskResult {
		r := TaskResult{Status: "failed", Comment: comment, FailureReason: reason}
		d.applyEgressBreaker(Task{ID: "t", RuntimeID: "rt-claw", AgentID: agent}, &r, log)
		return r
	}

	if r := fail("a", dnsErr, ""); r.FailureReason != "" {
		t.Fatalf("pre-trip failure rewritten to %q", r.FailureReason)
	}
	if r := fail("b", "", string(taskfailure.ReasonAgentProviderNetwork)); r.FailureReason != string(taskfailure.ReasonAgentProviderNetwork) {
		t.Fatalf("pre-trip provider_network rewritten to %q", r.FailureReason)
	}
	if r := fail("a", "tests failed", ""); r.FailureReason != "" {
		t.Fatal("non-egress failure must not count or be rewritten")
	}
	if r := fail("a", dnsErr, ""); r.FailureReason != string(taskfailure.ReasonEgressDegraded) {
		t.Fatalf("tripping failure reason = %q, want egress_degraded", r.FailureReason)
	}
	if r := fail("c", dnsErr, string(taskfailure.ReasonAgentProcessFailure)); r.FailureReason != string(taskfailure.ReasonEgressDegraded) {
		t.Fatalf("egress witness while open = %q, want egress_degraded", r.FailureReason)
	}
	if r := fail("c", "tests failed", ""); r.FailureReason != "" {
		t.Fatal("non-egress failure while open must be reported as-is")
	}
	ok := TaskResult{Status: "completed", Comment: dnsErr}
	d.applyEgressBreaker(Task{RuntimeID: "rt-claw", AgentID: "a"}, &ok, log)
	if ok.FailureReason != "" {
		t.Fatal("completed results must never be touched")
	}
}

func TestEgressTargetsForProvider(t *testing.T) {
	t.Setenv("MULTICA_EGRESS_PROBE_TARGETS", "")
	t.Setenv("ANTHROPIC_BASE_URL", "https://llm-gw.internal:8443/anthropic")
	t.Setenv("OPENAI_BASE_URL", "")
	if got := egressTargetsForProvider("claude"); !got.all || !reflect.DeepEqual(got.hosts, []string{"llm-gw.internal:8443"}) {
		t.Errorf("claude targets = %+v", got)
	}
	if got := egressTargetsForProvider("codex"); !reflect.DeepEqual(got.hosts, []string{"api.openai.com"}) {
		t.Errorf("codex targets = %+v", got)
	}
	if got := egressTargetsForProvider("openclaw"); got.all || !reflect.DeepEqual(got.hosts, egressGenericHosts) {
		t.Errorf("unknown provider should use any-of generic hosts, got %+v", got)
	}
	t.Setenv("MULTICA_EGRESS_PROBE_TARGETS", "generativelanguage.googleapis.com, https://api.anthropic.com/v1")
	want := []string{"generativelanguage.googleapis.com", "api.anthropic.com"}
	if got := egressTargetsForProvider("claude"); !got.all || !reflect.DeepEqual(got.hosts, want) {
		t.Errorf("override targets = %+v, want all of %v", got, want)
	}
}
