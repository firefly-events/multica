package agentconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// refusedCustomEnvKeysByClass holds, for each floor class, keys that must be
// refused whatever the allow-list says. The first rows of the code-execution,
// network and credential classes are the keys review-4 finding 1 named
// (NODE_OPTIONS, ANTHROPIC_BASE_URL, *_PROXY, LD_*, PYTHON*, BASH_ENV, ENV,
// GIT_*, *_TOKEN, *_KEY, PATH, HOME).
var refusedCustomEnvKeysByClass = map[string][]string{
	CustomEnvClassName: {
		"", "1ABC", "BASH_FUNC_ls%%", "A=B", "A B", "A-B", "A.B", "\x00",
		strings.Repeat("A", 129),
	},
	CustomEnvClassReserved: {
		"PATH", "HOME", "path", "USER", "SHELL", "TERM", "TMPDIR", "TMP", "TEMP",
		"MULTICA_TOKEN", "multica_server_url", "CODEX_HOME", "REASONIX_STATE_HOME",
		"CURSOR_DATA_DIR", "CURSOR_MCP_AUTH_SOURCE", "OPENCLAW_CONFIG_PATH", "OPENCLAW_INCLUDE_ROOTS",
	},
	CustomEnvClassCodeExecution: {
		"NODE_OPTIONS", "node_options", "NODE_PATH", "NODE_EXTRA_CA_CERTS",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "DYLD_INSERT_LIBRARIES",
		"PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME", "PERL5OPT", "PERL5LIB", "RUBYOPT",
		"BASH_ENV", "ENV", "ZDOTDIR", "PROMPT_COMMAND", "SHELLOPTS", "PS4", "IFS",
		"GIT_SSH_COMMAND", "GIT_CONFIG_GLOBAL", "GIT_EXEC_PATH", "GIT_ASKPASS", "git_dir",
		"EDITOR", "VISUAL", "PAGER", "SSH_ASKPASS", "JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS",
		"GOFLAGS", "GOTOOLCHAIN", "RUSTC_WRAPPER", "NPM_CONFIG_PREFIX", "PIP_INDEX_URL",
		"UV_INDEX_URL", "OPENSSL_CONF", "BASH_FUNC_x",
	},
	CustomEnvClassConfigRedirect: {
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "GNUPGHOME", "DOCKER_CONFIG", "KUBECONFIG",
		"NETRC", "SOME_TOOL_CONFIG_DIR", "SOME_TOOL_CONFIG_FILE", "SOME_TOOL_DATA_DIR",
	},
	CustomEnvClassNetworkRedirect: {
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "NO_PROXY", "GOPROXY",
		"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "SOME_API_URL", "OPENAI_API_BASE",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "AWS_ENDPOINT_URL", "DOCKER_HOST", "SOME_ENDPOINT",
	},
	CustomEnvClassTLSTrust: {
		"SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "SSLKEYLOGFILE",
	},
	CustomEnvClassCredential: {
		"GH_TOKEN", "GITHUB_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "API_KEY", "OPENAI_API_KEY",
		"ANTHROPIC_API_KEY", "CODEX_API_KEY", "DB_PASSWORD", "CLIENT_SECRET", "AWS_PROFILE",
		"AWS_SESSION_TOKEN", "SSH_AUTH_SOCK", "GOOGLE_APPLICATION_CREDENTIALS", "NPM_AUTH",
	},
	CustomEnvClassAgentCLI: {
		"CLAUDE_CODE_SHELL_PREFIX", "ANTHROPIC_CUSTOM_HEADERS", "ANTHROPIC_MODEL",
		"CLAUDE_CODE_USE_BEDROCK", "CODEX_SANDBOX", "OPENAI_ORG_ID", "GEMINI_MODEL",
		"GH_HOST_X", "GITHUB_REPOSITORY", "OLLAMA_MODELS",
	},
}

// legitimateCustomEnvKeys are custom_env keys in use today that the floor
// must keep admitting: the Pantheon uptime probe reads SLACK_WEBHOOK_URL and
// the PROBE_* overrides from its assignee agent's custom_env
// (plugins/claud-ometer/docs/uptime-probe-runbook.md). The rest are inert
// settings of the shape the env tab is meant for.
var legitimateCustomEnvKeys = []string{
	"SLACK_WEBHOOK_URL", "PROBE_METRICS_PATH", "PROBE_NAME_URL", "PROBE_IP_URL",
	"PROBE_HERMES_URL", "PROBE_DAEMON_URL", "PROBE_TAILSCALE_PEER", "PROBE_TIMEOUT_MS",
	"PROBE_FORCE_FAIL", "KEEP_ME", "LOG_LEVEL", "TZ", "LANG", "FEATURE_X", "K",
}

func TestCustomEnvFloorClass_RefusesEachClass(t *testing.T) {
	for class, keys := range refusedCustomEnvKeysByClass {
		for _, key := range keys {
			if got := CustomEnvFloorClass(key); got == "" {
				t.Errorf("CustomEnvFloorClass(%q) admitted the key; want class %s", key, class)
			} else if class != CustomEnvClassCredential && class != CustomEnvClassAgentCLI && got != class {
				// Credential and agent-CLI keys often also match an earlier
				// class (ANTHROPIC_BASE_URL is network, GH_TOKEN is credential
				// before agent_cli). Refusal is what matters there; the other
				// classes must report their own name.
				t.Errorf("CustomEnvFloorClass(%q) = %s; want %s", key, got, class)
			}
		}
	}
}

func TestCustomEnvFloorClass_AdmitsLegitimateKeys(t *testing.T) {
	for _, key := range legitimateCustomEnvKeys {
		if got := CustomEnvFloorClass(key); got != "" {
			t.Errorf("CustomEnvFloorClass(%q) = %s; legitimate key must be admitted", key, got)
		}
	}
}

func TestIsDaemonReservedEnvKey_StaysNarrow(t *testing.T) {
	// The daemon reads HERMES_HOME and REASONIX_HOME from custom_env itself,
	// so its reserved list must not grow with the server floor.
	for _, key := range []string{"HERMES_HOME", "REASONIX_HOME", "ANTHROPIC_API_KEY", "NODE_OPTIONS"} {
		if IsDaemonReservedEnvKey(key) {
			t.Errorf("IsDaemonReservedEnvKey(%q) = true; want false", key)
		}
	}
	for _, key := range []string{"MULTICA_TOKEN", "home", "PATH", "CURSOR_MCP_AUTH_SOURCE"} {
		if !IsDaemonReservedEnvKey(key) {
			t.Errorf("IsDaemonReservedEnvKey(%q) = false; want true", key)
		}
	}
}

func TestCustomEnvPolicy_FloorOnlyByDefault(t *testing.T) {
	var p CustomEnvPolicy
	if p.HasAllowList() {
		t.Fatal("zero policy must not have an allow-list")
	}
	for _, key := range legitimateCustomEnvKeys {
		if err := p.CheckKey(key); err != nil {
			t.Errorf("CheckKey(%q) = %v; want nil", key, err)
		}
	}
	var keyErr *CustomEnvKeyError
	if err := p.CheckKey("NODE_OPTIONS"); !errors.As(err, &keyErr) || keyErr.Class != CustomEnvClassCodeExecution {
		t.Fatalf("CheckKey(NODE_OPTIONS) = %v; want code_execution refusal", err)
	}
}

func TestCustomEnvPolicy_AllowList(t *testing.T) {
	p, err := NewCustomEnvPolicy(" SLACK_WEBHOOK_URL , PROBE_* ,,")
	if err != nil {
		t.Fatalf("NewCustomEnvPolicy: %v", err)
	}
	if !p.HasAllowList() {
		t.Fatal("expected an allow-list")
	}
	for _, key := range []string{"SLACK_WEBHOOK_URL", "PROBE_METRICS_PATH", "PROBE_FORCE_FAIL", "probe_ip_url"} {
		if err := p.CheckKey(key); err != nil {
			t.Errorf("CheckKey(%q) = %v; want allowed", key, err)
		}
	}
	var keyErr *CustomEnvKeyError
	for _, key := range []string{"LOG_LEVEL", "SLACK_WEBHOOK_URL_2", "PROB_X"} {
		if err := p.CheckKey(key); !errors.As(err, &keyErr) || keyErr.Class != CustomEnvClassNotAllowed {
			t.Errorf("CheckKey(%q) = %v; want not_in_allow_list", key, err)
		}
	}
	// The floor still wins inside an allowed prefix.
	if err := p.CheckKey("PROBE_TOKEN"); !errors.As(err, &keyErr) || keyErr.Class != CustomEnvClassCredential {
		t.Errorf("CheckKey(PROBE_TOKEN) = %v; want credential refusal", err)
	}
	if err := p.CheckKey("PROBE_HTTPS_PROXY"); !errors.As(err, &keyErr) || keyErr.Class != CustomEnvClassNetworkRedirect {
		t.Errorf("CheckKey(PROBE_HTTPS_PROXY) = %v; want network refusal", err)
	}
}

func TestNewCustomEnvPolicy_RefusesEntriesTheFloorRefuses(t *testing.T) {
	for _, spec := range []string{
		"NODE_OPTIONS", "SLACK_WEBHOOK_URL,ANTHROPIC_BASE_URL", "GIT_*", "LD_*", "*",
		"MULTICA_*", "BAD-NAME", "OPENAI_*", "XDG_*", "1X*",
	} {
		if _, err := NewCustomEnvPolicy(spec); err == nil {
			t.Errorf("NewCustomEnvPolicy(%q) = nil error; want refusal", spec)
		} else if !strings.Contains(err.Error(), CustomEnvAllowedKeysEnv) {
			t.Errorf("NewCustomEnvPolicy(%q) error %q should name %s", spec, err, CustomEnvAllowedKeysEnv)
		}
	}
	if p, err := NewCustomEnvPolicy("  "); err != nil || p.HasAllowList() {
		t.Fatalf("blank spec: policy=%+v err=%v; want floor-only", p, err)
	}
}

func TestCustomEnvPolicyFromEnv(t *testing.T) {
	t.Setenv(CustomEnvAllowedKeysEnv, "SLACK_WEBHOOK_URL")
	p, err := CustomEnvPolicyFromEnv()
	if err != nil || p.CheckKey("SLACK_WEBHOOK_URL") != nil || p.CheckKey("LOG_LEVEL") == nil {
		t.Fatalf("policy from env: err=%v", err)
	}
	t.Setenv(CustomEnvAllowedKeysEnv, "LD_PRELOAD")
	if _, err := CustomEnvPolicyFromEnv(); err == nil {
		t.Fatal("expected an error for a floor-refused entry")
	}
}

func TestDenyAllCustomEnvPolicy(t *testing.T) {
	p := DenyAllCustomEnvPolicy()
	if err := p.CheckKey("SLACK_WEBHOOK_URL"); err == nil {
		t.Fatal("deny-all policy admitted a key")
	}
	if err := p.Validate(map[string]string{}); err != nil {
		t.Fatalf("deny-all must still accept an empty map: %v", err)
	}
}

func TestCustomEnvPolicy_ValidateListsEveryRefusedKey(t *testing.T) {
	var p CustomEnvPolicy
	err := p.Validate(map[string]string{
		"SLACK_WEBHOOK_URL":  "https://hooks.example/x",
		"NODE_OPTIONS":       "--import=data:text/javascript,1",
		"ANTHROPIC_BASE_URL": "https://attacker.example",
	})
	var envErr *CustomEnvError
	if !errors.As(err, &envErr) {
		t.Fatalf("Validate = %v; want *CustomEnvError", err)
	}
	var keys []string
	for _, k := range envErr.Keys {
		keys = append(keys, k.Key)
	}
	if !reflect.DeepEqual(keys, []string{"ANTHROPIC_BASE_URL", "NODE_OPTIONS"}) {
		t.Fatalf("refused keys = %v", keys)
	}
	if strings.Contains(err.Error(), "attacker.example") || strings.Contains(err.Error(), "javascript") {
		t.Fatalf("error text must not echo values: %s", err)
	}
	if err := p.Validate(nil); err != nil {
		t.Fatalf("Validate(nil) = %v", err)
	}
}

func TestCustomEnvPolicy_Filter(t *testing.T) {
	var p CustomEnvPolicy
	in := map[string]string{"SLACK_WEBHOOK_URL": "u", "NODE_OPTIONS": "x", "LD_PRELOAD": "y"}
	kept, dropped := p.Filter(in)
	if !reflect.DeepEqual(kept, map[string]string{"SLACK_WEBHOOK_URL": "u"}) {
		t.Fatalf("kept = %v", kept)
	}
	if !reflect.DeepEqual(dropped, []string{"LD_PRELOAD", "NODE_OPTIONS"}) {
		t.Fatalf("dropped = %v", dropped)
	}
	if len(in) != 3 {
		t.Fatal("Filter modified its input")
	}
}

// pantheonClaudeAPIPins is the value Pantheon sets: its runtime routes Claude
// API credit-lane agents by custom_env CLAUDE_CONFIG_DIR, one directory per
// credits account (bin/multica-runtime-entrypoint.sh CLAUDE_API_DIRS).
const pantheonClaudeAPIPins = "CLAUDE_CONFIG_DIR=/home/node/.claude-api|/home/node/.claude-api-2"

func TestCustomEnvPolicy_PinnedValueAcceptedExactly(t *testing.T) {
	p, err := CustomEnvPolicy{}.WithAllowedValues(pantheonClaudeAPIPins)
	if err != nil {
		t.Fatalf("WithAllowedValues: %v", err)
	}
	for _, value := range []string{"/home/node/.claude-api", "/home/node/.claude-api-2"} {
		if err := p.CheckEntry("CLAUDE_CONFIG_DIR", value); err != nil {
			t.Errorf("CheckEntry(CLAUDE_CONFIG_DIR, %q) = %v; want accepted", value, err)
		}
	}
	if err := p.Validate(map[string]string{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api-2", "SLACK_WEBHOOK_URL": "u"}); err != nil {
		t.Fatalf("Validate = %v; want accepted", err)
	}
	kept, dropped := p.Filter(map[string]string{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api"})
	if len(dropped) != 0 || kept["CLAUDE_CONFIG_DIR"] != "/home/node/.claude-api" {
		t.Fatalf("Filter kept=%v dropped=%v; want pinned value kept", kept, dropped)
	}
	if !reflect.DeepEqual(p.PinnedKeys(), []string{"CLAUDE_CONFIG_DIR"}) {
		t.Fatalf("PinnedKeys = %v", p.PinnedKeys())
	}
	// A pin also admits the key under an allow-list that does not name it.
	withList, err := NewCustomEnvPolicy("SLACK_WEBHOOK_URL")
	if err != nil {
		t.Fatal(err)
	}
	if withList, err = withList.WithAllowedValues(pantheonClaudeAPIPins); err != nil {
		t.Fatal(err)
	}
	if err := withList.CheckEntry("CLAUDE_CONFIG_DIR", "/home/node/.claude-api"); err != nil {
		t.Fatalf("pinned key under an allow-list = %v; want accepted", err)
	}
}

func TestCustomEnvPolicy_PinnedKeyOtherValuesRefused(t *testing.T) {
	p, err := CustomEnvPolicy{}.WithAllowedValues(pantheonClaudeAPIPins)
	if err != nil {
		t.Fatalf("WithAllowedValues: %v", err)
	}
	for _, value := range []string{
		"",
		"/home/node/.claude",               // the OAuth lane
		"/home/node/.claude-api/",          // trailing slash
		"/home/node/.claude-api//",         // doubled slash
		"/home/node//.claude-api",          // empty segment
		"/home/node/./.claude-api",         // dot segment
		"/home/node/x/../.claude-api",      // dot-dot back to the pin
		"/home/node/.claude-api/../.codex", // dot-dot out of the pin
		"/home/node/.claude-api-2/..",
		"/home/node/.claude-api-3",
		"/home/node/.claude-ap",                 // prefix of a pin
		"/home/node/.claude-api-",               // prefix of a pin
		"/home/node/.claude-api-22",             // pin as prefix
		"/home/node/.claude-api/sub",            // pin as prefix
		" /home/node/.claude-api",               // whitespace
		"/home/node/.claude-api\n",              // control character
		"/HOME/NODE/.CLAUDE-API",                // case
		"~/.claude-api",                         // unexpanded home
		"$HOME/.claude-api",                     // unexpanded variable
		"/home/node/.claude-api|/tmp",           // separator smuggling
		"/home/node/.claude-api\x00",            // NUL
		"/proc/self/root/home/node/.claude-api", // symlink-style alias
	} {
		var keyErr *CustomEnvKeyError
		if err := p.CheckEntry("CLAUDE_CONFIG_DIR", value); !errors.As(err, &keyErr) || keyErr.Class != CustomEnvClassValueNotPinned {
			t.Errorf("CheckEntry(CLAUDE_CONFIG_DIR, %q) = %v; want value_not_pinned", value, err)
		}
	}
	// The pin is for the exact key name: other spellings fall to the floor.
	for _, key := range []string{"claude_config_dir", "Claude_Config_Dir", "CLAUDE_CONFIG_DIR_2"} {
		if err := p.CheckEntry(key, "/home/node/.claude-api"); err == nil {
			t.Errorf("CheckEntry(%q, pinned value) accepted; want floor refusal", key)
		}
	}
	// The error names the key and class but never echoes the value.
	err = p.Validate(map[string]string{"CLAUDE_CONFIG_DIR": "/attacker/dir"})
	if err == nil || !strings.Contains(err.Error(), CustomEnvClassValueNotPinned) || strings.Contains(err.Error(), "/attacker/dir") {
		t.Fatalf("Validate error = %v; want value_not_pinned without the value", err)
	}
	kept, dropped := p.Filter(map[string]string{"CLAUDE_CONFIG_DIR": "/home/node/.claude", "SLACK_WEBHOOK_URL": "u"})
	if !reflect.DeepEqual(kept, map[string]string{"SLACK_WEBHOOK_URL": "u"}) || !reflect.DeepEqual(dropped, []string{"CLAUDE_CONFIG_DIR"}) {
		t.Fatalf("Filter kept=%v dropped=%v; want unpinned value dropped", kept, dropped)
	}
}

func TestCustomEnvPolicy_PinsDoNotOpenOtherDeniedKeys(t *testing.T) {
	p, err := CustomEnvPolicy{}.WithAllowedValues(pantheonClaudeAPIPins)
	if err != nil {
		t.Fatalf("WithAllowedValues: %v", err)
	}
	for _, key := range []string{
		"CODEX_CONFIG_DIR", "OPENCODE_CONFIG_DIR", "SOME_TOOL_CONFIG_DIR", "CLAUDE_CODE_SHELL_PREFIX",
		"ANTHROPIC_BASE_URL", "NODE_OPTIONS", "LD_PRELOAD", "HTTPS_PROXY", "PATH", "BASH_ENV", "HOME",
	} {
		if err := p.CheckEntry(key, "/home/node/.claude-api"); err == nil {
			t.Errorf("CheckEntry(%q, pinned value) accepted; want refusal", key)
		}
	}
	if err := DenyAllCustomEnvPolicy().CheckEntry("CLAUDE_CONFIG_DIR", "/home/node/.claude-api"); err == nil {
		t.Error("deny-all policy admitted a pinned value")
	}
}

func TestCustomEnvPolicy_PinsRefuseHardClassesAndBadValues(t *testing.T) {
	for _, spec := range []string{
		// Keys whose class no pin can open.
		"NODE_OPTIONS=--max-old-space-size=4096",
		"LD_PRELOAD=/usr/lib/x.so", "LD_LIBRARY_PATH=/usr/lib",
		"ANTHROPIC_BASE_URL=https://api.anthropic.com", "OPENAI_BASE_URL=https://api.openai.com",
		"HTTPS_PROXY=http://proxy:3128", "http_proxy=http://proxy:3128", "NO_PROXY=localhost",
		"PATH=/usr/bin", "BASH_ENV=/etc/profile", "HOME=/home/node", "MULTICA_TOKEN=x",
		"SSL_CERT_FILE=/etc/ssl/cert.pem", "ANTHROPIC_API_KEY=sk", "GH_TOKEN=t",
		// A config redirect that is also a network redirect.
		"OTEL_CONFIG_DIR=/x",
		// One bad entry spoils the whole setting.
		pantheonClaudeAPIPins + ";NODE_OPTIONS=--inspect",
		// Malformed entries.
		"CLAUDE_CONFIG_DIR", "=/x", "BAD-NAME=/x", "CLAUDE_*=/x",
		// Values that are not one canonical spelling.
		"CLAUDE_CONFIG_DIR=", "CLAUDE_CONFIG_DIR=/a|", "CLAUDE_CONFIG_DIR=/home/node/.claude-api/",
		"CLAUDE_CONFIG_DIR=/home/node/../node/.claude-api", "CLAUDE_CONFIG_DIR=/home/node/./.claude-api",
		"CLAUDE_CONFIG_DIR=/home//node/.claude-api", "CLAUDE_CONFIG_DIR=/a/..",
	} {
		if _, err := (CustomEnvPolicy{}).WithAllowedValues(spec); err == nil {
			t.Errorf("WithAllowedValues(%q) = nil error; want refusal", spec)
		} else if !strings.Contains(err.Error(), CustomEnvAllowedValuesEnv) {
			t.Errorf("WithAllowedValues(%q) error %q should name %s", spec, err, CustomEnvAllowedValuesEnv)
		}
	}
	for _, spec := range []string{"", "  ", ";;", " " + pantheonClaudeAPIPins + " ; ", "CLAUDE_MODEL_LABEL=opus"} {
		if _, err := (CustomEnvPolicy{}).WithAllowedValues(spec); err != nil {
			t.Errorf("WithAllowedValues(%q) = %v; want accepted", spec, err)
		}
	}
}

func TestCustomEnvPolicyFromEnv_AllowedValues(t *testing.T) {
	t.Setenv(CustomEnvAllowedKeysEnv, "")
	t.Setenv(CustomEnvAllowedValuesEnv, pantheonClaudeAPIPins)
	p, err := CustomEnvPolicyFromEnv()
	if err != nil {
		t.Fatalf("policy from env: %v", err)
	}
	if p.CheckEntry("CLAUDE_CONFIG_DIR", "/home/node/.claude-api-2") != nil || p.CheckEntry("CLAUDE_CONFIG_DIR", "/home/node/.claude") == nil {
		t.Fatal("pins from env not applied")
	}
	// Each hard class named in the task stops the server from starting.
	for _, spec := range []string{"NODE_OPTIONS=x", "LD_PRELOAD=/x", "ANTHROPIC_BASE_URL=https://x", "HTTPS_PROXY=http://x", "PATH=/usr/bin", "BASH_ENV=/x"} {
		t.Setenv(CustomEnvAllowedValuesEnv, spec)
		if _, err := CustomEnvPolicyFromEnv(); err == nil {
			t.Errorf("%s=%q: expected a startup error", CustomEnvAllowedValuesEnv, spec)
		}
	}
	// A bad allow-list still fails even with good pins.
	t.Setenv(CustomEnvAllowedValuesEnv, pantheonClaudeAPIPins)
	t.Setenv(CustomEnvAllowedKeysEnv, "LD_PRELOAD")
	if _, err := CustomEnvPolicyFromEnv(); err == nil {
		t.Fatal("expected an error for a floor-refused allow-list entry")
	}
}
