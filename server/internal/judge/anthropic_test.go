package judge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestAnthropicJudgeScoreClampsToolUseScores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		toolInput := rubricToolInput{
			CorrectnessScore:    -10,
			AdherenceScore:      101,
			ToneScore:           50,
			ClarityScore:        0,
			TrajectoryScore:     100,
			OverallScore:        150,
			Rationale:           "scores intentionally outside the rubric range",
			TrajectoryRationale: "trajectory score is already in range",
		}
		inputRaw, _ := json.Marshal(toolInput)

		resp := anthropicResponse{
			Content: []anthropicContentBlock{
				{Type: "tool_use", Name: scoreToolName, Input: inputRaw},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	j := NewAnthropicJudge("test-key", "claude-opus-4.8", srv.Client())
	result, err := scoreAgainst(t, srv.URL, j)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	if result.CorrectnessScore != MinScore {
		t.Fatalf("expected correctness score clamped to %d, got %d", MinScore, result.CorrectnessScore)
	}
	if result.AdherenceScore != MaxScore {
		t.Fatalf("expected adherence score clamped to %d, got %d", MaxScore, result.AdherenceScore)
	}
	if result.OverallScore != MaxScore {
		t.Fatalf("expected overall score clamped to %d, got %d", MaxScore, result.OverallScore)
	}
	if result.ToneScore != 50 || result.ClarityScore != 0 || result.TrajectoryScore != 100 {
		t.Fatalf("expected in-range scores preserved, got %+v", result)
	}
}

func TestAnthropicJudgeScoreErrorResponses(t *testing.T) {
	cases := []struct {
		name       string
		handler    http.HandlerFunc
		wantErrSub []string
	}{
		{
			name: "non-200 status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "rate limited", http.StatusTooManyRequests)
			},
			wantErrSub: []string{"judge API returned 429", "rate limited"},
		},
		{
			name: "anthropic error envelope",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(anthropicResponse{
					Error: &struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					}{
						Type:    "overloaded_error",
						Message: "try again later",
					},
				})
			},
			wantErrSub: []string{"judge API error", "try again later"},
		},
		{
			name: "missing score tool use block",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(anthropicResponse{
					Content: []anthropicContentBlock{
						{Type: "tool_use", Name: "other_tool", Input: json.RawMessage(`{}`)},
					},
				})
			},
			wantErrSub: []string{"no " + scoreToolName + " tool_use block"},
		},
		{
			name: "malformed JSON response body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"content":`))
			},
			wantErrSub: []string{"unmarshal judge response"},
		},
		{
			name: "malformed tool input",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(anthropicResponse{
					Content: []anthropicContentBlock{
						{Type: "tool_use", Name: scoreToolName, Input: json.RawMessage(`{"correctness_score":"high"}`)},
					},
				})
			},
			wantErrSub: []string{"unmarshal rubric tool input"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			j := NewAnthropicJudge("test-key", "claude-opus-4.8", srv.Client())
			_, err := scoreAgainst(t, srv.URL, j)
			if err == nil {
				t.Fatal("expected Score to return an error")
			}
			for _, sub := range tc.wantErrSub {
				if !strings.Contains(err.Error(), sub) {
					t.Fatalf("expected error %q to contain %q", err.Error(), sub)
				}
			}
		})
	}
}

func TestAnthropicJudgeScoreContextCancellationDuringRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer srv.Close()
	defer close(release)

	j := NewAnthropicJudge("test-key", "claude-opus-4.8", srv.Client())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := scoreAgainstContext(ctx, t, srv.URL, j)
		errCh <- err
	}()

	select {
	case <-started:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive judge request")
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected Score to return an error")
		}
		if !strings.Contains(err.Error(), "judge request") || !errors.Is(err, context.Canceled) {
			t.Fatalf("expected descriptive context cancellation error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Score did not return after context cancellation")
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
	return scoreAgainstContext(context.Background(), t, baseURL, j)
}

func scoreAgainstContext(ctx context.Context, t *testing.T, baseURL string, j *AnthropicJudge) (Result, error) {
	t.Helper()
	// AnthropicJudge always targets the fixed anthropicAPIURL constant,
	// so redirect via a client-level RoundTripper that rewrites the
	// request URL to the test server instead.
	j.HTTP = &http.Client{Transport: rewriteHostTransport{base: baseURL}}
	return j.Score(ctx, Input{Trajectory: Trajectory{
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
