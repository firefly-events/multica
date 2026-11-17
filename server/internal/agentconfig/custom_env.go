package agentconfig

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// CustomEnvAllowedKeysEnv names the server setting that turns the custom_env
// rule into an allow-list. Its value is a comma-separated list of exact key
// names and/or prefixes ending in `*` (for example
// "SLACK_WEBHOOK_URL,PROBE_*"). Unset or empty means no allow-list: only the
// fixed deny floor below applies.
const CustomEnvAllowedKeysEnv = "MULTICA_AGENT_ENV_ALLOWED_KEYS"

// CustomEnvAllowedValuesEnv names the server setting that pins exact values
// for keys the floor would otherwise refuse. Its value is a `;`-separated list
// of KEY=VALUE|VALUE entries, for example
// "CLAUDE_CONFIG_DIR=/home/node/.claude-api|/home/node/.claude-api-2". A
// pinned key is accepted only when its value is byte-for-byte one of the
// listed values; any other value is refused as before. Only the config
// redirect and agent CLI classes can be pinned: an entry for a key that runs
// code, redirects network traffic, changes TLS trust, carries a credential,
// is daemon-owned or is not a valid name stops the server from starting.
const CustomEnvAllowedValuesEnv = "MULTICA_AGENT_ENV_ALLOWED_VALUES"

// Custom_env key classes. They name why a key is refused, so an operator
// reading a 400 or a log line knows what the key would have done.
const (
	CustomEnvClassName            = "invalid_name"
	CustomEnvClassReserved        = "daemon_reserved"
	CustomEnvClassCodeExecution   = "code_execution"
	CustomEnvClassConfigRedirect  = "config_redirect"
	CustomEnvClassNetworkRedirect = "network_redirect"
	CustomEnvClassTLSTrust        = "tls_trust"
	CustomEnvClassCredential      = "credential"
	CustomEnvClassAgentCLI        = "agent_cli"
	CustomEnvClassNotAllowed      = "not_in_allow_list"
	CustomEnvClassValueNotPinned  = "value_not_pinned"
)

// customEnvClassReason is the human half of each class, used in error text.
var customEnvClassReason = map[string]string{
	CustomEnvClassName:            "it is not a valid environment variable name",
	CustomEnvClassReserved:        "the daemon owns it",
	CustomEnvClassCodeExecution:   "it can make a shell, interpreter, loader or tool run other code",
	CustomEnvClassConfigRedirect:  "it moves where a tool reads its config, home or data",
	CustomEnvClassNetworkRedirect: "it redirects network traffic (proxy, base URL, endpoint or host)",
	CustomEnvClassTLSTrust:        "it changes which TLS certificates are trusted",
	CustomEnvClassCredential:      "it carries or selects a credential",
	CustomEnvClassAgentCLI:        "it steers the agent CLI's account, model endpoint, headers or config",
	CustomEnvClassNotAllowed:      "it is not on this server's " + CustomEnvAllowedKeysEnv + " list",
	CustomEnvClassValueNotPinned:  "its value is not one of the values this server's " + CustomEnvAllowedValuesEnv + " pins for it",
}

// customEnvUnpinnableClasses are the floor classes no value pin can open. A
// key in any of them can run code, move traffic, change trust, carry a secret
// or override the daemon whatever its value looks like, so pinning a value
// does not make it safe.
var customEnvUnpinnableClasses = map[string]struct{}{
	CustomEnvClassName:            {},
	CustomEnvClassReserved:        {},
	CustomEnvClassCodeExecution:   {},
	CustomEnvClassNetworkRedirect: {},
	CustomEnvClassTLSTrust:        {},
	CustomEnvClassCredential:      {},
}

// customEnvNamePattern is the POSIX portable name: letters, digits and
// underscore, not starting with a digit. It also refuses bash's exported
// function form (BASH_FUNC_x%%), "=" and NUL.
var customEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// daemonReservedCustomEnvKeys are the names the daemon sets itself for every
// task. The daemon strips them from custom_env at launch (IsDaemonReservedEnvKey);
// the server refuses them outright. Every MULTICA_* name is reserved as well.
var daemonReservedCustomEnvKeys = map[string]struct{}{
	"HOME": {}, "PATH": {}, "USER": {}, "SHELL": {}, "TERM": {},
	"TMPDIR": {}, "TMP": {}, "TEMP": {},
	"CODEX_HOME": {}, "REASONIX_STATE_HOME": {}, "CURSOR_DATA_DIR": {},
	"CURSOR_MCP_AUTH_SOURCE": {}, "OPENCLAW_CONFIG_PATH": {}, "OPENCLAW_INCLUDE_ROOTS": {},
}

// IsDaemonReservedEnvKey reports whether the daemon owns key and must not let
// custom_env override it in the child environment. The daemon uses this list
// (not the full floor) because it also reads some custom_env keys itself, such
// as HERMES_HOME; the server refuses the wider floor before they get there.
func IsDaemonReservedEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "MULTICA_") {
		return true
	}
	_, ok := daemonReservedCustomEnvKeys[upper]
	return ok
}

type customEnvRule struct {
	class    string
	exact    []string
	prefixes []string
	suffixes []string
}

// customEnvFloor is the deny floor: keys no server setting can allow. Names
// compare upper-cased, because several tools also honour the lower-case form
// (http_proxy, no_proxy). The floor is a best effort over known classes; an
// allow-list is the control that does not depend on it being complete.
var customEnvFloor = []customEnvRule{
	{
		class: CustomEnvClassCodeExecution,
		exact: []string{
			"ENV", "BASH_ENV", "ZDOTDIR", "PROMPT_COMMAND", "SHELLOPTS", "BASHOPTS",
			"PS4", "IFS", "CDPATH", "GLOBIGNORE", "INPUTRC",
			"EDITOR", "VISUAL", "PAGER", "MANPAGER", "BROWSER", "LESSOPEN", "LESSCLOSE",
			"SSH_ASKPASS", "SUDO_ASKPASS",
			"GCONV_PATH", "CLASSPATH", "GEM_PATH", "RUBYOPT", "RUBYLIB", "PERLLIB",
			"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS",
			"GOFLAGS", "GOENV", "GOTOOLCHAIN", "GOROOT", "GOPATH", "GOBIN",
			"RUSTC_WRAPPER", "RUSTC", "RUSTFLAGS", "CC", "CXX", "LDFLAGS", "MAKEFLAGS",
			"OPENSSL_CONF", "OPENSSL_ENGINES",
		},
		prefixes: []string{
			"LD_", "DYLD_", "NODE_", "PYTHON", "PERL5", "BASH_FUNC_", "GIT_",
			"NPM_CONFIG_", "YARN_", "PNPM_", "BUN_", "DENO_", "PIP_", "UV_", "CARGO_",
		},
	},
	{
		class:    CustomEnvClassConfigRedirect,
		exact:    []string{"KUBECONFIG", "GNUPGHOME", "NETRC", "WGETRC"},
		prefixes: []string{"XDG_"},
		suffixes: []string{"_HOME", "_CONFIG", "_CONFIG_DIR", "_CONFIG_PATH", "_CONFIG_FILE", "_DATA_DIR", "_STATE_DIR", "_PROFILE"},
	},
	{
		class:    CustomEnvClassNetworkRedirect,
		prefixes: []string{"OTEL_"},
		suffixes: []string{"PROXY", "_BASE_URL", "_API_URL", "_API_BASE", "_ENDPOINT", "_ENDPOINT_URL", "_HOST", "_REGISTRY"},
	},
	{
		class:    CustomEnvClassTLSTrust,
		exact:    []string{"SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE"},
		suffixes: []string{"_CA_BUNDLE", "_CA_CERT", "_CA_CERTS", "_CAFILE", "_CAPATH", "_TLS_REJECT_UNAUTHORIZED"},
	},
	{
		class:    CustomEnvClassCredential,
		exact:    []string{"SSH_AUTH_SOCK"},
		prefixes: []string{"AWS_", "AZURE_", "GCLOUD_", "CLOUDSDK_"},
		suffixes: []string{"_TOKEN", "_KEY", "_SECRET", "_PASSWORD", "_PASSWD", "_CREDENTIAL", "_CREDENTIALS", "_AUTH"},
	},
	{
		// Agent CLIs read account, endpoint, header, permission and shell
		// settings from these namespaces (ANTHROPIC_BASE_URL,
		// ANTHROPIC_CUSTOM_HEADERS, CLAUDE_CODE_SHELL_PREFIX, ...).
		class: CustomEnvClassAgentCLI,
		prefixes: []string{
			"ANTHROPIC_", "CLAUDE_", "CODEX_", "OPENAI_", "GEMINI_", "GOOGLE_GENAI_", "VERTEX_",
			"CURSOR_", "OPENCODE_", "HERMES_", "OPENCLAW_", "REASONIX_", "COPILOT_", "GH_", "GITHUB_",
			"CODEBUDDY_", "CODEARTS_", "KIMI_", "QWEN_", "DEEPSEEK_", "OPENROUTER_", "OLLAMA_", "MISTRAL_",
		},
	},
}

// CustomEnvKeyError says why one custom_env key is refused.
type CustomEnvKeyError struct {
	Key   string
	Class string
}

func (e *CustomEnvKeyError) Error() string {
	return fmt.Sprintf("custom_env key %q is refused: %s (%s)", e.Key, customEnvClassReason[e.Class], e.Class)
}

// CustomEnvError lists every refused key of one custom_env map, sorted by key.
type CustomEnvError struct {
	Keys []*CustomEnvKeyError
}

func (e *CustomEnvError) Error() string {
	parts := make([]string, 0, len(e.Keys))
	for _, k := range e.Keys {
		parts = append(parts, k.Error())
	}
	return strings.Join(parts, "; ")
}

// CustomEnvFloorClass returns the floor class that refuses key, or "" when the
// floor admits it. The floor applies whatever the allow-list says.
func CustomEnvFloorClass(key string) string {
	if classes := customEnvFloorClasses(key); len(classes) > 0 {
		return classes[0]
	}
	return ""
}

// customEnvFloorClasses returns every floor class that refuses key, in floor
// order. A key can match several (OTEL_CONFIG_DIR is a config redirect and a
// network redirect); a value pin needs all of them to be pinnable.
func customEnvFloorClasses(key string) []string {
	if !customEnvNamePattern.MatchString(key) {
		return []string{CustomEnvClassName}
	}
	var classes []string
	if IsDaemonReservedEnvKey(key) {
		classes = append(classes, CustomEnvClassReserved)
	}
	upper := strings.ToUpper(key)
	for _, rule := range customEnvFloor {
		if customEnvRuleMatches(rule, upper) {
			classes = append(classes, rule.class)
		}
	}
	return classes
}

func customEnvRuleMatches(rule customEnvRule, upper string) bool {
	for _, exact := range rule.exact {
		if upper == exact {
			return true
		}
	}
	for _, prefix := range rule.prefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	for _, suffix := range rule.suffixes {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

// CustomEnvPolicy is the one rule for which keys an agent's custom_env may
// hold. The server applies it on every write (agent create, env replace) and
// again when it hands the env to a daemon at claim, so a row written straight
// to the database is filtered before it reaches a runtime.
//
// The zero value has no allow-list: only the floor applies.
type CustomEnvPolicy struct {
	allowExact    map[string]struct{}
	allowPrefixes []string
	denyAll       bool
	// pinnedValues maps an exact key name (case-sensitive) to the only values
	// it may hold. See CustomEnvAllowedValuesEnv.
	pinnedValues map[string]map[string]struct{}
}

// NewCustomEnvPolicy parses an allow-list spec (see CustomEnvAllowedKeysEnv).
// An empty spec yields the floor-only policy. An entry that the floor would
// refuse is an error, so a misconfigured server does not start believing it
// allowed something it cannot.
func NewCustomEnvPolicy(spec string) (CustomEnvPolicy, error) {
	p := CustomEnvPolicy{}
	var errs []error
	for _, raw := range strings.Split(spec, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if p.allowExact == nil {
			p.allowExact = map[string]struct{}{}
		}
		if prefix, ok := strings.CutSuffix(entry, "*"); ok {
			// Probe the prefix with a neutral tail. A prefix the floor
			// refuses by prefix or exact match ("GIT_*", "LD_*") is a
			// configuration error; suffix rules still apply per key.
			if prefix == "" || !customEnvNamePattern.MatchString(prefix+"X") {
				errs = append(errs, fmt.Errorf("%s entry %q is not a key prefix", CustomEnvAllowedKeysEnv, entry))
				continue
			}
			if class := CustomEnvFloorClass(prefix + "X"); class != "" {
				errs = append(errs, fmt.Errorf("%s entry %q can never be allowed: %s (%s)", CustomEnvAllowedKeysEnv, entry, customEnvClassReason[class], class))
				continue
			}
			p.allowPrefixes = append(p.allowPrefixes, strings.ToUpper(prefix))
			continue
		}
		if class := CustomEnvFloorClass(entry); class != "" {
			errs = append(errs, fmt.Errorf("%s entry %q can never be allowed: %s (%s)", CustomEnvAllowedKeysEnv, entry, customEnvClassReason[class], class))
			continue
		}
		p.allowExact[strings.ToUpper(entry)] = struct{}{}
	}
	if len(errs) > 0 {
		return CustomEnvPolicy{}, errors.Join(errs...)
	}
	return p, nil
}

// WithAllowedValues returns p with the value pins of spec (see
// CustomEnvAllowedValuesEnv) added. An entry for a key in an unpinnable class,
// a malformed entry, or a value that is not in canonical form is an error, so
// the server does not start with a pin it cannot honour safely.
func (p CustomEnvPolicy) WithAllowedValues(spec string) (CustomEnvPolicy, error) {
	pins, err := parseCustomEnvAllowedValues(spec)
	if err != nil {
		return CustomEnvPolicy{}, err
	}
	p.pinnedValues = pins
	return p, nil
}

func parseCustomEnvAllowedValues(spec string) (map[string]map[string]struct{}, error) {
	var pins map[string]map[string]struct{}
	var errs []error
	for _, raw := range strings.Split(spec, ";") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		key, values, ok := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			errs = append(errs, fmt.Errorf("%s entry %q is not KEY=VALUE|VALUE", CustomEnvAllowedValuesEnv, entry))
			continue
		}
		if !customEnvNamePattern.MatchString(key) {
			errs = append(errs, fmt.Errorf("%s key %q is not a valid environment variable name", CustomEnvAllowedValuesEnv, key))
			continue
		}
		classes := customEnvFloorClasses(key)
		refusedClass := ""
		for _, class := range classes {
			if _, hard := customEnvUnpinnableClasses[class]; hard {
				refusedClass = class
				break
			}
		}
		if refusedClass != "" {
			errs = append(errs, fmt.Errorf("%s key %q can never be allowed, whatever its value: %s (%s)", CustomEnvAllowedValuesEnv, key, customEnvClassReason[refusedClass], refusedClass))
			continue
		}
		for _, value := range strings.Split(values, "|") {
			if reason := nonCanonicalPinnedValue(value); reason != "" {
				// The value is operator config, not a secret: name it so the
				// operator can fix the entry.
				errs = append(errs, fmt.Errorf("%s value %q for %s is refused: %s", CustomEnvAllowedValuesEnv, value, key, reason))
				continue
			}
			if pins == nil {
				pins = map[string]map[string]struct{}{}
			}
			if pins[key] == nil {
				pins[key] = map[string]struct{}{}
			}
			pins[key][value] = struct{}{}
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return pins, nil
}

// nonCanonicalPinnedValue returns why value cannot be pinned, or "". Pinned
// values match byte for byte, never after normalisation, so a listed value
// must already be the one canonical spelling: an agent sending
// "/a/b/", "/a/./b" or "/x/../a/b" for a pinned "/a/b" is refused, and the
// list itself must not hold such a spelling.
func nonCanonicalPinnedValue(value string) string {
	switch {
	case value == "":
		return "it is empty"
	case strings.TrimSpace(value) != value:
		return "it has leading or trailing whitespace"
	case strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "it contains a control character"
	}
	if strings.Contains(value, "/") {
		if len(value) > 1 && strings.HasSuffix(value, "/") {
			return "it ends with a slash"
		}
		if strings.Contains(value, "//") {
			return "it contains an empty path segment"
		}
		for _, segment := range strings.Split(value, "/") {
			if segment == "." || segment == ".." {
				return "it contains a . or .. path segment"
			}
		}
	}
	return ""
}

// CustomEnvPolicyFromEnv reads CustomEnvAllowedKeysEnv and
// CustomEnvAllowedValuesEnv from the process env.
func CustomEnvPolicyFromEnv() (CustomEnvPolicy, error) {
	p, keysErr := NewCustomEnvPolicy(os.Getenv(CustomEnvAllowedKeysEnv))
	pinned, valuesErr := p.WithAllowedValues(os.Getenv(CustomEnvAllowedValuesEnv))
	if err := errors.Join(keysErr, valuesErr); err != nil {
		return CustomEnvPolicy{}, err
	}
	return pinned, nil
}

// DenyAllCustomEnvPolicy refuses every key. The server falls back to it when
// its allow-list setting cannot be parsed, so a bad setting fails closed.
func DenyAllCustomEnvPolicy() CustomEnvPolicy {
	return CustomEnvPolicy{denyAll: true}
}

// HasAllowList reports whether the policy restricts keys to an allow-list.
func (p CustomEnvPolicy) HasAllowList() bool {
	return p.denyAll || p.allowExact != nil
}

// PinnedKeys returns the keys that have value pins, sorted.
func (p CustomEnvPolicy) PinnedKeys() []string {
	keys := make([]string, 0, len(p.pinnedValues))
	for key := range p.pinnedValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// CheckEntry returns nil when key may be stored and sent to a runtime with
// value, or a *CustomEnvKeyError naming the class that refuses it. A key with
// value pins (CustomEnvAllowedValuesEnv) is accepted exactly when value is one
// of its pinned values, whatever the floor or the allow-list say about the
// key; with any other value it is refused. The error never holds the value.
func (p CustomEnvPolicy) CheckEntry(key, value string) error {
	if p.denyAll {
		return p.CheckKey(key)
	}
	if allowed, pinned := p.pinnedValues[key]; pinned {
		if _, ok := allowed[value]; ok {
			return nil
		}
		return &CustomEnvKeyError{Key: key, Class: CustomEnvClassValueNotPinned}
	}
	return p.CheckKey(key)
}

// CheckKey returns nil when key may be stored and sent to a runtime whatever
// its value, or a *CustomEnvKeyError naming the class that refuses it. It
// ignores value pins; use CheckEntry when the value is known.
func (p CustomEnvPolicy) CheckKey(key string) error {
	if class := CustomEnvFloorClass(key); class != "" {
		return &CustomEnvKeyError{Key: key, Class: class}
	}
	if !p.HasAllowList() {
		return nil
	}
	if p.denyAll {
		return &CustomEnvKeyError{Key: key, Class: CustomEnvClassNotAllowed}
	}
	upper := strings.ToUpper(key)
	if _, ok := p.allowExact[upper]; ok {
		return nil
	}
	for _, prefix := range p.allowPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return nil
		}
	}
	return &CustomEnvKeyError{Key: key, Class: CustomEnvClassNotAllowed}
}

// Validate checks every key of env. It returns nil or a *CustomEnvError
// listing each refused key.
func (p CustomEnvPolicy) Validate(env map[string]string) error {
	var refused []*CustomEnvKeyError
	for key, value := range env {
		var keyErr *CustomEnvKeyError
		if errors.As(p.CheckEntry(key, value), &keyErr) {
			refused = append(refused, keyErr)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	sort.Slice(refused, func(i, j int) bool { return refused[i].Key < refused[j].Key })
	return &CustomEnvError{Keys: refused}
}

// Filter returns env without the keys the policy refuses, and the refused
// keys sorted. The input map is not modified.
func (p CustomEnvPolicy) Filter(env map[string]string) (map[string]string, []string) {
	if len(env) == 0 {
		return env, nil
	}
	kept := make(map[string]string, len(env))
	var dropped []string
	for key, value := range env {
		if p.CheckEntry(key, value) != nil {
			dropped = append(dropped, key)
			continue
		}
		kept[key] = value
	}
	sort.Strings(dropped)
	return kept, dropped
}
