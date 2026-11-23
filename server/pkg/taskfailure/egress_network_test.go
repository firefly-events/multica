package taskfailure

import (
	"strings"
	"testing"
)

// Wire shapes captured on dragon on 2026-11-11, when container DNS was dead
// after boot (pantheon-v2 #540), plus the equivalent wording from the other
// resolver layers the bundled CLIs sit on.
var egressFailureSamples = []struct {
	name string
	in   string
}{
	{"gemini EAI_AGAIN", "getaddrinfo EAI_AGAIN generativelanguage.googleapis.com"},
	{"gemini EAI_AGAIN in exit wrapper", "[API Error: request to https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:streamGenerateContent failed, reason: getaddrinfo EAI_AGAIN generativelanguage.googleapis.com]; gemini exited with error: exit status 1"},
	{"codex ENOTFOUND", "Error: getaddrinfo ENOTFOUND api.openai.com"},
	{"openclaw ECONNREFUSED", "openclaw: FetchError: request to https://api.anthropic.com/v1/messages failed, reason: connect ECONNREFUSED 160.79.104.10:443"},
	{"provider ETIMEDOUT", "Error: connect ETIMEDOUT generativelanguage.googleapis.com:443"},
	{"glibc", "curl: (6) Could not resolve host: api.anthropic.com"},
	{"glibc gai_strerror", "socket.gaierror: [Errno -3] Temporary failure in name resolution"},
	{"go resolver", "Post \"https://api.openai.com/v1/responses\": dial tcp: lookup api.openai.com: no such host"},
}

func TestClassifyEgressFailuresAsProviderNetwork(t *testing.T) {
	t.Parallel()
	for _, tc := range egressFailureSamples {
		for _, in := range []string{tc.in, strings.ToUpper(tc.in)} {
			if got := Classify(in); got != ReasonAgentProviderNetwork {
				t.Errorf("%s: Classify(%q) = %s, want %s", tc.name, in, got, ReasonAgentProviderNetwork)
			}
		}
		if !IsEgressFailure(tc.in) {
			t.Errorf("%s: IsEgressFailure(%q) = false, want true", tc.name, tc.in)
		}
	}
}

// ETIMEDOUT alone is not provider evidence: local tools and MCP servers time
// out too, and those must neither be retried as network blips nor trip the
// daemon's egress breaker.
func TestIsEgressFailureScope(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"local tool failed: connect ETIMEDOUT 192.0.2.1:443",
		"MCP server error: connect ETIMEDOUT 10.0.0.4:5432",
		"tests failed: 3 assertions",
		"API Error: 401 unauthorized",
		"",
	} {
		if IsEgressFailure(in) {
			t.Errorf("IsEgressFailure(%q) = true, want false", in)
		}
	}
}

// An installed daemon predating the egress witnesses reports gemini's resolver
// failure as the catchall or as process_failure. The server must upgrade it so
// the retry applies without waiting for every host to update.
func TestNormalizeDaemonReasonUpgradesEgressFailure(t *testing.T) {
	t.Parallel()
	const raw = "getaddrinfo EAI_AGAIN generativelanguage.googleapis.com; gemini exited with error: exit status 1"
	for _, legacy := range []string{string(ReasonAgentUnknown), string(ReasonAgentProcessFailure), "agent_error"} {
		if got := NormalizeDaemonReason(legacy, raw); got != ReasonAgentProviderNetwork {
			t.Errorf("NormalizeDaemonReason(%q) = %s, want %s", legacy, got, ReasonAgentProviderNetwork)
		}
	}
	// A refined reason an earlier rule chose for a different cause is kept.
	if got := NormalizeDaemonReason(string(ReasonAgentProviderAuthOrAccess), raw); got != ReasonAgentProviderAuthOrAccess {
		t.Errorf("auth reason was rewritten to %s", got)
	}
	// No witness, no upgrade.
	if got := NormalizeDaemonReason(string(ReasonAgentUnknown), "something else broke"); got != ReasonAgentUnknown {
		t.Errorf("unwitnessed catchall was rewritten to %s", got)
	}
}
