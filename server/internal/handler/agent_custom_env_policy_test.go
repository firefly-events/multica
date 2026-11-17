package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/agentconfig"
)

// refusedCustomEnvSamples has one key per refused class of the custom_env
// rule (agentconfig.CustomEnvPolicy), plus the keys review-4 finding 1 used:
// NODE_OPTIONS runs code in the runtime, ANTHROPIC_BASE_URL sends the
// runtime's credential to another host.
var refusedCustomEnvSamples = []struct {
	class string
	key   string
}{
	{agentconfig.CustomEnvClassName, "BASH_FUNC_ls%%"},
	{agentconfig.CustomEnvClassReserved, "PATH"},
	{agentconfig.CustomEnvClassReserved, "HOME"},
	{agentconfig.CustomEnvClassReserved, "MULTICA_TOKEN"},
	{agentconfig.CustomEnvClassCodeExecution, "NODE_OPTIONS"},
	{agentconfig.CustomEnvClassCodeExecution, "LD_PRELOAD"},
	{agentconfig.CustomEnvClassCodeExecution, "PYTHONPATH"},
	{agentconfig.CustomEnvClassCodeExecution, "BASH_ENV"},
	{agentconfig.CustomEnvClassCodeExecution, "ENV"},
	{agentconfig.CustomEnvClassCodeExecution, "GIT_SSH_COMMAND"},
	{agentconfig.CustomEnvClassConfigRedirect, "XDG_CONFIG_HOME"},
	{agentconfig.CustomEnvClassNetworkRedirect, "ANTHROPIC_BASE_URL"},
	{agentconfig.CustomEnvClassNetworkRedirect, "OPENAI_API_URL"},
	{agentconfig.CustomEnvClassNetworkRedirect, "HTTPS_PROXY"},
	{agentconfig.CustomEnvClassTLSTrust, "SSL_CERT_FILE"},
	{agentconfig.CustomEnvClassCredential, "GH_TOKEN"},
	{agentconfig.CustomEnvClassCredential, "OPENAI_API_KEY"},
	{agentconfig.CustomEnvClassAgentCLI, "CLAUDE_CODE_SHELL_PREFIX"},
}

// legitimateCustomEnv is what the Pantheon uptime probe keeps in its
// assignee agent's custom_env today.
var legitimateCustomEnv = map[string]string{
	"SLACK_WEBHOOK_URL":  "https://hooks.slack.example/services/T/B/x",
	"PROBE_METRICS_PATH": "/data/probe.jsonl",
	"PROBE_TIMEOUT_MS":   "5000",
}

func readStoredCustomEnv(t *testing.T, agentID string) map[string]string {
	t.Helper()
	var stored string
	if err := testPool.QueryRow(context.Background(), `SELECT custom_env::text FROM agent WHERE id = $1`, agentID).Scan(&stored); err != nil {
		t.Fatalf("read custom_env: %v", err)
	}
	got := map[string]string{}
	if err := json.Unmarshal([]byte(stored), &got); err != nil {
		t.Fatalf("decode custom_env: %v", err)
	}
	return got
}

// POST /api/agents (also `multica agent create` and `multica agent copy`).
func TestCreateAgent_RefusesCustomEnvKeyPerClass(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, tc := range refusedCustomEnvSamples {
		t.Run(tc.class+"/"+tc.key, func(t *testing.T) {
			name := "Custom Env Refused " + tc.key
			req := newRequest(http.MethodPost, "/api/agents", map[string]any{
				"name":       name,
				"runtime_id": handlerTestRuntimeID(t),
				"custom_env": map[string]string{tc.key: "x", "SLACK_WEBHOOK_URL": "https://ok.example"},
			})
			w := httptest.NewRecorder()
			testHandler.CreateAgent(w, req)
			if w.Code != http.StatusBadRequest {
				t.Cleanup(func() {
					testPool.Exec(context.Background(), `DELETE FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name)
				})
				t.Fatalf("CreateAgent with %s: expected 400, got %d: %s", tc.key, w.Code, w.Body.String())
			}
			if body := w.Body.String(); !strings.Contains(body, tc.class) || strings.Contains(body, "SLACK_WEBHOOK_URL") {
				t.Errorf("error should name the refused class %s and only the refused key: %s", tc.class, body)
			}
			var n int
			if err := testPool.QueryRow(context.Background(),
				`SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name).Scan(&n); err != nil {
				t.Fatalf("count agents: %v", err)
			}
			if n != 0 {
				t.Fatalf("refused create still stored %d agent(s)", n)
			}
		})
	}
}

// PUT /api/agents/{id}/env (also `multica agent env set`).
func TestUpdateAgentEnv_RefusesCustomEnvKeyPerClass(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "custom-env-refuse-update", nil)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET custom_env = '{"SLACK_WEBHOOK_URL":"before"}' WHERE id = $1`, agentID); err != nil {
		t.Fatalf("seed custom_env: %v", err)
	}
	for _, tc := range refusedCustomEnvSamples {
		t.Run(tc.class+"/"+tc.key, func(t *testing.T) {
			body := map[string]any{"custom_env": map[string]string{tc.key: "x", "SLACK_WEBHOOK_URL": "after"}}
			req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID+"/env", body), "id", agentID)
			w := httptest.NewRecorder()
			testHandler.UpdateAgentEnv(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("UpdateAgentEnv with %s: expected 400, got %d: %s", tc.key, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.class) {
				t.Errorf("error should name class %s: %s", tc.class, w.Body.String())
			}
			if got := readStoredCustomEnv(t, agentID); !reflect.DeepEqual(got, map[string]string{"SLACK_WEBHOOK_URL": "before"}) {
				t.Fatalf("refused update changed the stored env: %v", got)
			}
		})
	}
}

// A key stored before the rule existed (or written straight to the
// database) cannot be carried forward with the **** marker.
func TestUpdateAgentEnv_RefusesPreservingARefusedLegacyKey(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "custom-env-legacy-refused", nil)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE agent SET custom_env = '{"NODE_OPTIONS":"--require=/x.js","LOG_LEVEL":"info"}' WHERE id = $1`, agentID); err != nil {
		t.Fatalf("seed custom_env: %v", err)
	}
	body := map[string]any{"custom_env": map[string]string{"NODE_OPTIONS": envSentinel, "LOG_LEVEL": "debug"}}
	req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID+"/env", body), "id", agentID)
	w := httptest.NewRecorder()
	testHandler.UpdateAgentEnv(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "NODE_OPTIONS") {
		t.Fatalf("expected 400 naming NODE_OPTIONS, got %d: %s", w.Code, w.Body.String())
	}

	// Dropping the legacy key is how the owner gets out of that state.
	body = map[string]any{"custom_env": map[string]string{"LOG_LEVEL": "debug"}}
	req = withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID+"/env", body), "id", agentID)
	w = httptest.NewRecorder()
	testHandler.UpdateAgentEnv(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 once the refused key is dropped, got %d: %s", w.Code, w.Body.String())
	}
	if got := readStoredCustomEnv(t, agentID); !reflect.DeepEqual(got, map[string]string{"LOG_LEVEL": "debug"}) {
		t.Fatalf("stored env = %v", got)
	}
}

// The keys in use today still save on both write paths.
func TestCustomEnv_LegitimateKeysStillAccepted(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	req := newRequest(http.MethodPost, "/api/agents", map[string]any{
		"name":       "Custom Env Legitimate Create",
		"runtime_id": handlerTestRuntimeID(t),
		"custom_env": legitimateCustomEnv,
	})
	w := httptest.NewRecorder()
	testHandler.CreateAgent(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAgent: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, created.ID) })
	if got := readStoredCustomEnv(t, created.ID); !reflect.DeepEqual(got, legitimateCustomEnv) {
		t.Fatalf("created env = %v", got)
	}

	body := map[string]any{"custom_env": map[string]string{
		"SLACK_WEBHOOK_URL": envSentinel,
		"PROBE_FORCE_FAIL":  "name",
	}}
	req = withURLParam(newRequest(http.MethodPut, "/api/agents/"+created.ID+"/env", body), "id", created.ID)
	w = httptest.NewRecorder()
	testHandler.UpdateAgentEnv(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateAgentEnv: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	want := map[string]string{"SLACK_WEBHOOK_URL": legitimateCustomEnv["SLACK_WEBHOOK_URL"], "PROBE_FORCE_FAIL": "name"}
	if got := readStoredCustomEnv(t, created.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("updated env = %v; want %v", got, want)
	}
}

// A refused key written straight to the database (the postgres path in
// review-4 finding 1) never reaches the runtime: the claim drops it.
func TestClaimTaskByRuntime_WithholdsRefusedCustomEnvKeys(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "custom-env-claim runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "custom-env-claim agent")
	if _, err := testPool.Exec(ctx, `UPDATE agent SET custom_env = $1 WHERE id = $2`,
		`{"SLACK_WEBHOOK_URL":"https://ok.example","NODE_OPTIONS":"--import=data:text/javascript,1","ANTHROPIC_BASE_URL":"https://attacker.example"}`,
		agentID); err != nil {
		t.Fatalf("seed custom_env: %v", err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "custom-env-claim-daemon")
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var claimResp struct {
		Task *AgentTaskResponse `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &claimResp); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if claimResp.Task == nil || claimResp.Task.Agent == nil {
		t.Fatalf("missing task agent in claim response: %s", w.Body.String())
	}
	if got := claimResp.Task.Agent.CustomEnv; !reflect.DeepEqual(got, map[string]string{"SLACK_WEBHOOK_URL": "https://ok.example"}) {
		t.Fatalf("claimed custom_env = %v; want only SLACK_WEBHOOK_URL", got)
	}
}

// The allow-list is a server setting, so it is exercised on a handler built
// with it rather than on the shared test handler.
func TestRequireAllowedCustomEnv_AllowList(t *testing.T) {
	policy, err := agentconfig.NewCustomEnvPolicy("SLACK_WEBHOOK_URL,PROBE_*")
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	h := &Handler{cfg: Config{CustomEnvPolicy: policy}}

	w := httptest.NewRecorder()
	if !h.requireAllowedCustomEnv(w, legitimateCustomEnv) {
		t.Fatalf("allow-listed keys refused: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	if h.requireAllowedCustomEnv(w, map[string]string{"SLACK_WEBHOOK_URL": "u", "LOG_LEVEL": "info"}) {
		t.Fatal("key outside the allow-list was accepted")
	}
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), agentconfig.CustomEnvClassNotAllowed) {
		t.Fatalf("expected 400 not_in_allow_list, got %d: %s", w.Code, w.Body.String())
	}

	kept := h.filterCustomEnvForDaemon("agent", map[string]string{"PROBE_IP_URL": "u", "LOG_LEVEL": "info", "LD_PRELOAD": "x"})
	if !reflect.DeepEqual(kept, map[string]string{"PROBE_IP_URL": "u"}) {
		t.Fatalf("claim filter kept %v", kept)
	}
}

// pantheonClaudeAPIPins is what Pantheon sets: its runtime routes Claude API
// credit-lane agents by custom_env CLAUDE_CONFIG_DIR, one directory per
// credits account.
const pantheonClaudeAPIPins = "CLAUDE_CONFIG_DIR=/home/node/.claude-api|/home/node/.claude-api-2"

// withPinnedCustomEnvPolicy gives the shared test handler the Pantheon value
// pins for the rest of the test.
func withPinnedCustomEnvPolicy(t *testing.T) {
	t.Helper()
	policy, err := agentconfig.CustomEnvPolicy{}.WithAllowedValues(pantheonClaudeAPIPins)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	previous := testHandler.cfg.CustomEnvPolicy
	testHandler.cfg.CustomEnvPolicy = policy
	t.Cleanup(func() { testHandler.cfg.CustomEnvPolicy = previous })
}

// Without the pins the credit-lane key is refused (the rollout hazard the
// setting exists for); with them only the two exact directories save.
func TestCustomEnv_PinnedValueOnWritePaths(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	create := func(name string, env map[string]string) *httptest.ResponseRecorder {
		req := newRequest(http.MethodPost, "/api/agents", map[string]any{
			"name": name, "runtime_id": handlerTestRuntimeID(t), "custom_env": env,
		})
		w := httptest.NewRecorder()
		testHandler.CreateAgent(w, req)
		if w.Code == http.StatusCreated {
			var created AgentResponse
			if err := json.Unmarshal(w.Body.Bytes(), &created); err == nil {
				t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, created.ID) })
			}
		}
		return w
	}

	if w := create("Custom Env Pin Unset", map[string]string{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api"}); w.Code != http.StatusBadRequest {
		t.Fatalf("without pins: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	withPinnedCustomEnvPolicy(t)

	w := create("Custom Env Pin Create", map[string]string{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api", "SLACK_WEBHOOK_URL": "u"})
	if w.Code != http.StatusCreated {
		t.Fatalf("pinned value: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := readStoredCustomEnv(t, created.ID); got["CLAUDE_CONFIG_DIR"] != "/home/node/.claude-api" {
		t.Fatalf("stored env = %v", got)
	}

	for _, value := range []string{"/home/node/.claude", "/home/node/.claude-api/", "/home/node/x/../.claude-api", "/home/node/.claude-api-22"} {
		w := create("Custom Env Pin Refused "+value, map[string]string{"CLAUDE_CONFIG_DIR": value})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), agentconfig.CustomEnvClassValueNotPinned) {
			t.Errorf("create with %q: expected 400 value_not_pinned, got %d: %s", value, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), value) {
			t.Errorf("error echoes the refused value %q: %s", value, w.Body.String())
		}
	}

	update := func(env map[string]string) *httptest.ResponseRecorder {
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+created.ID+"/env", map[string]any{"custom_env": env}), "id", created.ID)
		w := httptest.NewRecorder()
		testHandler.UpdateAgentEnv(w, req)
		return w
	}
	// The **** marker keeps the stored pinned value; it is checked as stored.
	if w := update(map[string]string{"CLAUDE_CONFIG_DIR": envSentinel, "SLACK_WEBHOOK_URL": "v"}); w.Code != http.StatusOK {
		t.Fatalf("update keeping the pinned value: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// Switching to the other credits account.
	if w := update(map[string]string{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api-2"}); w.Code != http.StatusOK {
		t.Fatalf("update to the second pinned value: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	for _, env := range []map[string]string{
		{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api-2/"},
		{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api-2/.."},
		{"CLAUDE_CONFIG_DIR": "/home/node/.claude"},
		{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api", "CLAUDE_CODE_SHELL_PREFIX": "/home/node/.claude-api"},
		{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api", "CODEX_CONFIG_DIR": "/home/node/.claude-api"},
		{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api", "NODE_OPTIONS": "/home/node/.claude-api"},
	} {
		if w := update(env); w.Code != http.StatusBadRequest {
			t.Errorf("update %v: expected 400, got %d: %s", env, w.Code, w.Body.String())
		}
	}
	if got := readStoredCustomEnv(t, created.ID); !reflect.DeepEqual(got, map[string]string{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api-2"}) {
		t.Fatalf("refused updates changed the stored env: %v", got)
	}
}

// At claim the pinned value reaches the runtime and any other value of the
// same key (written straight to the database) is withheld.
func TestClaimTaskByRuntime_PinnedCustomEnvValue(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withPinnedCustomEnvPolicy(t)
	ctx := context.Background()
	claim := func(label, customEnv string) map[string]string {
		runtimeID := createClaimReclaimRuntime(t, ctx, label+" runtime")
		agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, label+" agent")
		if _, err := testPool.Exec(ctx, `UPDATE agent SET custom_env = $1 WHERE id = $2`, customEnv, agentID); err != nil {
			t.Fatalf("seed custom_env: %v", err)
		}
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
			VALUES ($1, $2, $3, 'queued', 0)
			RETURNING id
		`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
			t.Fatalf("create task: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
		w := httptest.NewRecorder()
		req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, label+"-daemon")
		req = withURLParam(req, "runtimeId", runtimeID)
		testHandler.ClaimTaskByRuntime(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Task *AgentTaskResponse `json:"task"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Task == nil || resp.Task.Agent == nil {
			t.Fatalf("decode claim (%v): %s", err, w.Body.String())
		}
		return resp.Task.Agent.CustomEnv
	}

	got := claim("custom-env-pin-ok", `{"CLAUDE_CONFIG_DIR":"/home/node/.claude-api-2","NODE_OPTIONS":"--inspect","SLACK_WEBHOOK_URL":"u"}`)
	if !reflect.DeepEqual(got, map[string]string{"CLAUDE_CONFIG_DIR": "/home/node/.claude-api-2", "SLACK_WEBHOOK_URL": "u"}) {
		t.Fatalf("claimed custom_env = %v; want the pinned value and SLACK_WEBHOOK_URL", got)
	}
	got = claim("custom-env-pin-bad", `{"CLAUDE_CONFIG_DIR":"/home/node/.claude-api/../.claude","SLACK_WEBHOOK_URL":"u"}`)
	if !reflect.DeepEqual(got, map[string]string{"SLACK_WEBHOOK_URL": "u"}) {
		t.Fatalf("claimed custom_env = %v; want the unpinned value withheld", got)
	}
}
