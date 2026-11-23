package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// Egress circuit breaker (pantheon-v2 #540).
//
// When the host loses DNS or outbound connectivity, every task the daemon
// claims dies within seconds on getaddrinfo EAI_AGAIN / ENOTFOUND, burns an
// attempt and — once the budget is gone — terminates the issue. The breaker
// turns a host-wide outage into a pause:
//
//   - It opens for a runtime when egressTripFailures provider-network failures
//     from at least egressTripAgents distinct agents land on that runtime within
//     egressTripWindow. Requiring several agents keeps one agent's broken
//     config from pausing a whole runtime.
//   - While open, the poller does not claim for that runtime, and every
//     provider-network failure on it is reported as egress_degraded, which the
//     server retries without consuming an attempt (never blocking the issue).
//   - A probe loop checks DNS + an HTTPS HEAD to the runtime's provider every
//     egressProbeInterval and closes the breaker once the probe passes.
//   - On daemon start every registered runtime starts gated ("preflight") until
//     its first probe passes, so a daemon booting into dead DNS waits and logs
//     instead of claiming work it cannot run. The preflight never fails Run.
//
// MULTICA_EGRESS_BREAKER=off disables all of it. MULTICA_EGRESS_PROBE_TARGETS
// (comma-separated hosts or URLs) replaces the probe targets for every runtime;
// all of them must pass.
const (
	egressTripFailures  = 3
	egressTripAgents    = 2
	egressTripWindow    = 2 * time.Minute
	egressProbeInterval = 30 * time.Second
	egressProbeTimeout  = 10 * time.Second

	egressCauseFailures  = "provider_network_failures"
	egressCausePreflight = "preflight"
)

type egressFailure struct {
	at      time.Time
	agentID string
}

type egressDegradation struct {
	since       time.Time
	cause       string
	lastProbe   time.Time
	lastProbeOK bool
	lastError   string
}

// egressBreaker tracks per-runtime egress health. The zero value is not used;
// a nil *egressBreaker is a disabled breaker, so every method is nil-safe and
// test fixtures that build a bare Daemon keep their behavior.
type egressBreaker struct {
	mu       sync.Mutex
	now      func() time.Time
	failures map[string][]egressFailure
	degraded map[string]*egressDegradation
}

func newEgressBreaker(now func() time.Time) *egressBreaker {
	if now == nil {
		now = time.Now
	}
	return &egressBreaker{
		now:      now,
		failures: make(map[string][]egressFailure),
		degraded: make(map[string]*egressDegradation),
	}
}

func egressBreakerEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MULTICA_EGRESS_BREAKER"))) {
	case "off", "0", "false", "disabled":
		return false
	}
	return true
}

// RecordFailure registers a provider-network failure of agentID on runtimeID.
// It reports whether this failure opened the breaker and whether the runtime
// is degraded after recording it (true for both the tripping failure and any
// failure that lands while the breaker is already open).
func (b *egressBreaker) RecordFailure(runtimeID, agentID string) (opened, degraded bool) {
	if b == nil || runtimeID == "" {
		return false, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.degraded[runtimeID]; ok {
		return false, true
	}
	now := b.now()
	cutoff := now.Add(-egressTripWindow)
	kept := b.failures[runtimeID][:0]
	for _, f := range b.failures[runtimeID] {
		if f.at.After(cutoff) {
			kept = append(kept, f)
		}
	}
	kept = append(kept, egressFailure{at: now, agentID: agentID})
	b.failures[runtimeID] = kept

	agents := make(map[string]struct{}, len(kept))
	for _, f := range kept {
		agents[f.agentID] = struct{}{}
	}
	if len(kept) < egressTripFailures || len(agents) < egressTripAgents {
		return false, false
	}
	b.degraded[runtimeID] = &egressDegradation{since: now, cause: egressCauseFailures}
	delete(b.failures, runtimeID)
	return true, true
}

// Gate marks runtimes degraded until their first probe passes (startup
// preflight). Runtimes already degraded keep their original cause.
func (b *egressBreaker) Gate(runtimeIDs []string, cause string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for _, id := range runtimeIDs {
		if _, ok := b.degraded[id]; !ok && id != "" {
			b.degraded[id] = &egressDegradation{since: now, cause: cause}
		}
	}
}

func (b *egressBreaker) IsDegraded(runtimeID string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.degraded[runtimeID]
	return ok
}

// Degraded returns the degraded runtime IDs, sorted.
func (b *egressBreaker) Degraded() []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.degraded))
	for id := range b.degraded {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// FilterHealthy returns runtimeIDs minus the degraded ones, preserving order.
func (b *egressBreaker) FilterHealthy(runtimeIDs []string) []string {
	if b == nil {
		return runtimeIDs
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.degraded) == 0 {
		return runtimeIDs
	}
	out := make([]string, 0, len(runtimeIDs))
	for _, id := range runtimeIDs {
		if _, ok := b.degraded[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// RecordProbe stores a probe outcome for a degraded runtime and closes the
// breaker when it passed. It reports whether the breaker closed and, if so,
// how long the runtime had been degraded.
func (b *egressBreaker) RecordProbe(runtimeID string, probeErr error) (closed bool, downFor time.Duration) {
	if b == nil {
		return false, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	deg, ok := b.degraded[runtimeID]
	if !ok {
		return false, 0
	}
	now := b.now()
	if probeErr != nil {
		deg.lastProbe, deg.lastProbeOK, deg.lastError = now, false, probeErr.Error()
		return false, 0
	}
	delete(b.degraded, runtimeID)
	delete(b.failures, runtimeID)
	return true, now.Sub(deg.since)
}

// Forget drops all state for runtimes that are no longer registered.
func (b *egressBreaker) Forget(keep []string) {
	if b == nil {
		return
	}
	live := make(map[string]struct{}, len(keep))
	for _, id := range keep {
		live[id] = struct{}{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range b.degraded {
		if _, ok := live[id]; !ok {
			delete(b.degraded, id)
		}
	}
	for id := range b.failures {
		if _, ok := live[id]; !ok {
			delete(b.failures, id)
		}
	}
}

// healthEgressRuntime is one degraded runtime in /health.
type healthEgressRuntime struct {
	RuntimeID   string `json:"runtime_id"`
	Cause       string `json:"cause"`
	Since       string `json:"since"`
	LastProbe   string `json:"last_probe,omitempty"`
	LastProbeOK bool   `json:"last_probe_ok"`
	LastError   string `json:"last_error,omitempty"`
}

// healthEgress is the /health egress block. EgressDegraded is true when any
// runtime is gated; the daemon stays "running" (HTTP 200) because it is alive
// and will resume on its own.
type healthEgress struct {
	EgressDegraded bool                  `json:"egress_degraded"`
	Runtimes       []healthEgressRuntime `json:"runtimes,omitempty"`
}

func (b *egressBreaker) Snapshot() *healthEgress {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := &healthEgress{EgressDegraded: len(b.degraded) > 0}
	for id, deg := range b.degraded {
		rt := healthEgressRuntime{
			RuntimeID:   id,
			Cause:       deg.cause,
			Since:       deg.since.UTC().Format(time.RFC3339),
			LastProbeOK: deg.lastProbeOK,
			LastError:   deg.lastError,
		}
		if !deg.lastProbe.IsZero() {
			rt.LastProbe = deg.lastProbe.UTC().Format(time.RFC3339)
		}
		out.Runtimes = append(out.Runtimes, rt)
	}
	sort.Slice(out.Runtimes, func(i, j int) bool { return out.Runtimes[i].RuntimeID < out.Runtimes[j].RuntimeID })
	return out
}

// isEgressCandidate reports whether a failed task result is evidence of a
// host-level egress problem: the reason is provider_network, or the raw error
// carries a resolver/connect witness.
func isEgressCandidate(failureReason, rawError string) bool {
	return failureReason == string(taskfailure.ReasonAgentProviderNetwork) ||
		taskfailure.IsEgressFailure(rawError)
}

// applyEgressBreaker feeds a failed task result into the breaker and, when the
// runtime is (or just became) degraded, rewrites its failure_reason to
// egress_degraded so the server retries it without consuming an attempt.
func (d *Daemon) applyEgressBreaker(task Task, result *TaskResult, taskLog *slog.Logger) {
	if d.egress == nil || result.Status == "completed" || result.Status == "cancelled" {
		return
	}
	reason := result.FailureReason
	if reason == "" {
		reason = taskfailure.Classify(result.Comment).String()
	}
	if !isEgressCandidate(reason, result.Comment) {
		return
	}
	opened, degraded := d.egress.RecordFailure(task.RuntimeID, task.AgentID)
	if opened {
		taskLog.Warn("egress circuit breaker opened: pausing claims for runtime until provider probes pass",
			"runtime_id", task.RuntimeID, "failures", egressTripFailures, "agents", egressTripAgents,
			"window", egressTripWindow.String())
	}
	if degraded {
		result.FailureReason = string(taskfailure.ReasonEgressDegraded)
		taskLog.Info("task failed while runtime egress is degraded; reporting egress_degraded (attempt not consumed)",
			"runtime_id", task.RuntimeID, "original_failure_reason", reason)
	}
}

// egressProbeFunc checks a set of targets. Injectable for tests.
type egressProbeFunc func(ctx context.Context, t egressTargets) error

// egressTargets is what a runtime's probe must reach. With all=false the probe
// passes when any host passes (the generic fallback for providers whose
// upstream we cannot know); with all=true every host must pass.
type egressTargets struct {
	hosts []string
	all   bool
}

func (t egressTargets) key() string {
	return fmt.Sprintf("%t|%s", t.all, strings.Join(t.hosts, ","))
}

var egressGenericHosts = []string{"api.anthropic.com", "api.openai.com", "generativelanguage.googleapis.com"}

// egressTargetsForProvider maps a runtime provider to the hosts its probe
// checks, honoring base-URL overrides in the daemon's environment.
func egressTargetsForProvider(provider string) egressTargets {
	if raw := strings.TrimSpace(os.Getenv("MULTICA_EGRESS_PROBE_TARGETS")); raw != "" {
		var hosts []string
		for _, part := range strings.Split(raw, ",") {
			if h := egressHost(part); h != "" {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) > 0 {
			return egressTargets{hosts: hosts, all: true}
		}
	}
	switch provider {
	case "claude":
		return egressTargets{hosts: []string{egressHostOr(os.Getenv("ANTHROPIC_BASE_URL"), "api.anthropic.com")}, all: true}
	case "codex":
		return egressTargets{hosts: []string{egressHostOr(os.Getenv("OPENAI_BASE_URL"), "api.openai.com")}, all: true}
	case "cursor":
		return egressTargets{hosts: []string{"api2.cursor.sh"}, all: true}
	}
	return egressTargets{hosts: egressGenericHosts, all: false}
}

func egressHostOr(raw, fallback string) string {
	if h := egressHost(raw); h != "" {
		return h
	}
	return fallback
}

// egressHost extracts host[:port] from a bare host or a URL.
func egressHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// probeEgressTargets is the production probe: DNS resolution plus an HTTPS
// HEAD per host. Any HTTP response counts as reachable — a 401/404 still
// proves DNS, TCP and TLS work. DNS is skipped when an HTTPS proxy applies,
// because the proxy resolves the name, not this host.
func probeEgressTargets(ctx context.Context, t egressTargets) error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	client := &http.Client{
		Timeout:       egressProbeTimeout,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer transport.CloseIdleConnections()

	var errs []error
	for _, host := range t.hosts {
		err := probeEgressHost(ctx, client, host)
		if err == nil && !t.all {
			return nil
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", host, err))
		}
	}
	return errors.Join(errs...)
}

func probeEgressHost(ctx context.Context, client *http.Client, host string) error {
	target := "https://" + host + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return err
	}
	if proxy, _ := http.ProxyFromEnvironment(req); proxy == nil {
		name := host
		if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
			name = h
		}
		lookupCtx, cancel := context.WithTimeout(ctx, egressProbeTimeout)
		_, err := net.DefaultResolver.LookupHost(lookupCtx, name)
		cancel()
		if err != nil {
			return fmt.Errorf("dns: %w", err)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("https: %w", err)
	}
	resp.Body.Close()
	return nil
}

// egressProbeRound probes every degraded runtime once (one probe per distinct
// target set) and closes the breakers whose probe passed. It returns the
// runtimes that resumed.
func (d *Daemon) egressProbeRound(ctx context.Context, probe egressProbeFunc) []string {
	degraded := d.egress.Degraded()
	if len(degraded) == 0 {
		return nil
	}
	results := make(map[string]error)
	var resumed []string
	for _, id := range degraded {
		provider := ""
		if rt := d.findRuntime(id); rt != nil {
			provider = rt.Provider
		}
		targets := egressTargetsForProvider(provider)
		key := targets.key()
		err, done := results[key]
		if !done {
			err = probe(ctx, targets)
			results[key] = err
		}
		if ctx.Err() != nil {
			return resumed
		}
		closed, downFor := d.egress.RecordProbe(id, err)
		switch {
		case closed:
			resumed = append(resumed, id)
			d.logger.Info("egress restored: runtime resumed claiming",
				"runtime_id", id, "provider", provider, "degraded_for", downFor.Truncate(time.Second).String())
		case err != nil:
			d.logger.Warn("egress probe failed; runtime stays paused (no tasks claimed, no attempts consumed)",
				"runtime_id", id, "provider", provider, "error", err)
		}
	}
	return resumed
}

// egressProbeLoop runs the startup preflight and then probes degraded runtimes
// every egressProbeInterval, waking the claim loop when a runtime resumes.
func (d *Daemon) egressProbeLoop(ctx context.Context, taskWakeups chan<- taskWakeup, probe egressProbeFunc) {
	if d.egress == nil {
		return
	}
	ticker := time.NewTicker(egressProbeInterval)
	defer ticker.Stop()
	for {
		d.egress.Forget(d.allRuntimeIDs())
		for _, id := range d.egressProbeRound(ctx, probe) {
			select {
			case taskWakeups <- taskWakeup{runtimeID: id}:
			default:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// startEgressBreaker gates every registered runtime behind the preflight probe
// and starts the probe loop. Called from Run before the claim loop starts.
func (d *Daemon) startEgressBreaker(ctx context.Context, taskWakeups chan<- taskWakeup) {
	if d.egress == nil {
		return
	}
	ids := d.allRuntimeIDs()
	d.egress.Gate(ids, egressCausePreflight)
	if len(ids) > 0 {
		d.logger.Info("egress preflight: holding claims until provider DNS/HTTPS probes pass", "runtimes", len(ids))
	}
	go d.egressProbeLoop(ctx, taskWakeups, probeEgressTargets)
}
