package judge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAnthropicJudgeScoreParsesToolUseResponse exercises the real HTTP
// request/response parsing path against a fake Anthropic-shaped server,
// so the tool-forced JSON parsing logic is verified without hitting the
// network or needing a live API key.
func TestAnthropicJudgeScoreParsesToolUseResponse(t *testing.T) {
	var gotReq map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key header to be forwarded, got %q", r.Header.Get("x-api-key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		toolInput := rubricToolInput{
			CorrectnessScore:    95,
			AdherenceScore:      90,
			ToneScore:           85,
			ClarityScore:        92,
			TrajectoryScore:     70,
			OverallScore:        88,
			Rationale:           "matched acceptance criteria precisely",
			TrajectoryRationale: "a couple of redundant tool calls",
		}
		inputRaw, _ := json.Marshal(toolInput)

		resp := anthropicResponse{
			Content: []anthropicContentBlock{
				{Type: "tool_use", Name: scoreToolName, Input: inputRaw},
			},
		}
		resp.Usage.InputTokens = 500
		resp.Usage.OutputTokens = 120

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	j := NewAnthropicJudge("test-key", "claude-opus-4.8", srv.Client())
	// Point at the fake server instead of the real Anthropic endpoint.
	j.HTTP = srv.Client()

	result, err := scoreAgainst(t, srv.URL, j)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	if result.Provider != "anthropic" || result.Model != "claude-opus-4.8" {
		t.Fatalf("expected provider/model recorded, got %+v", result)
	}
	if result.CorrectnessScore != 95 || result.OverallScore != 88 {
		t.Fatalf("expected rubric scores to round-trip, got %+v", result)
	}
	if result.Rationale == "" || result.TrajectoryRationale == "" {
		t.Fatalf("expected both rationale fields populated, got %+v", result)
	}
	if result.InputTokens != 500 || result.OutputTokens != 120 {
		t.Fatalf("expected judge's own token usage recorded, got %+v", result)
	}

	if gotReq["model"] != "claude-opus-4.8" {
		t.Fatalf("expected model in request body, got %v", gotReq["model"])
	}
	toolChoice, _ := gotReq["tool_choice"].(map[string]any)
	if toolChoice["name"] != scoreToolName {
		t.Fatalf("expected tool_choice to force %s, got %v", scoreToolName, gotReq["tool_choice"])
	}
}

func TestClampScore(t *testing.T) {
	cases := map[int]int{-10: 0, 0: 0, 50: 50, 100: 100, 150: 100}
	for in, want := range cases {
		if got := clampScore(in); got != want {
			t.Fatalf("clampScore(%d) = %d, want %d", in, got, want)
		}
	}
}

// scoreAgainst points the judge at a test server URL by temporarily
// swapping anthropicAPIURL's effective target: since the constant isn't
// injectable, this test instead relies on AnthropicJudge.HTTP's
// transport not being reachable for the real host, and Go's http.Client
// resolves relative to the request URL, so we build the request
// manually against the test server for this one assertion path.
func scoreAgainst(t *testing.T, baseURL string, j *AnthropicJudge) (Result, error) {
	t.Helper()
	// AnthropicJudge always targets the fixed anthropicAPIURL constant,
	// so redirect via a client-level RoundTripper that rewrites the
	// request URL to the test server instead.
	j.HTTP = &http.Client{Transport: rewriteHostTransport{base: baseURL}}
	return j.Score(context.Background(), Input{Trajectory: Trajectory{
		TaskID:     "11111111-1111-1111-1111-111111111111",
		IssueTitle: "Fix login redirect",
		Steps: []Step{
			{Seq: 1, Type: "tool_call", Tool: "Edit", Content: "patch auth.go"},
		},
		FinalResult: "Fixed the redirect loop.",
	}})
}

type rewriteHostTransport struct{ base string }

func (rt rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL := rt.base + req.URL.Path
	r2 := req.Clone(req.Context())
	u, err := req.URL.Parse(newURL)
	if err != nil {
		return nil, err
	}
	r2.URL = u
	r2.Host = u.Host
	return http.DefaultTransport.RoundTrip(r2)
}
