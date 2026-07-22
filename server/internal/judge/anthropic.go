package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const anthropicAPIURL = "https://api.anthropic.com/v1/messages"
const anthropicAPIVersion = "2023-06-01"

// scoreToolName/scoreToolSchema force the judge model to answer through
// a single tool call whose input is the rubric, instead of parsing
// scores back out of free-form prose.
const scoreToolName = "submit_rubric_score"

var scoreToolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"correctness_score":    map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"adherence_score":      map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"tone_score":           map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"clarity_score":        map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"trajectory_score":     map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"overall_score":        map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"rationale":            map[string]any{"type": "string"},
		"trajectory_rationale": map[string]any{"type": "string"},
	},
	"required": []string{
		"correctness_score", "adherence_score", "tone_score", "clarity_score",
		"trajectory_score", "overall_score", "rationale", "trajectory_rationale",
	},
}

// AnthropicJudge calls the Anthropic Messages API with the judge model
// as a strong-tier grader. It is the production Judge implementation;
// tests use a fake Judge instead so they don't depend on network
// access or an API key (see judge_test.go / scheduler tests).
type AnthropicJudge struct {
	APIKey string
	Model  string
	HTTP   *http.Client
}

// NewAnthropicJudge constructs a judge bound to apiKey/model. HTTP
// defaults to a client with a bounded timeout if httpClient is nil.
func NewAnthropicJudge(apiKey, model string, httpClient *http.Client) *AnthropicJudge {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &AnthropicJudge{APIKey: apiKey, Model: model, HTTP: httpClient}
}

type anthropicRequest struct {
	Model      string              `json:"model"`
	MaxTokens  int                 `json:"max_tokens"`
	Messages   []anthropicMessage  `json:"messages"`
	Tools      []anthropicTool     `json:"tools"`
	ToolChoice anthropicToolChoice `json:"tool_choice"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Usage   struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type rubricToolInput struct {
	CorrectnessScore    int    `json:"correctness_score"`
	AdherenceScore      int    `json:"adherence_score"`
	ToneScore           int    `json:"tone_score"`
	ClarityScore        int    `json:"clarity_score"`
	TrajectoryScore     int    `json:"trajectory_score"`
	OverallScore        int    `json:"overall_score"`
	Rationale           string `json:"rationale"`
	TrajectoryRationale string `json:"trajectory_rationale"`
}

func (j *AnthropicJudge) Score(ctx context.Context, in Input) (Result, error) {
	prompt := BuildPrompt(in.Trajectory)

	reqBody := anthropicRequest{
		Model:     j.Model,
		MaxTokens: 2048,
		Messages: []anthropicMessage{
			{Role: "user", Content: prompt},
		},
		Tools: []anthropicTool{
			{
				Name:        scoreToolName,
				Description: "Submit the rubric score for the graded task.",
				InputSchema: scoreToolSchema,
			},
		},
		ToolChoice: anthropicToolChoice{Type: "tool", Name: scoreToolName},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return Result{}, fmt.Errorf("marshal judge request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicAPIURL, bytes.NewReader(payload))
	if err != nil {
		return Result{}, fmt.Errorf("build judge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", j.APIKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := j.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("judge request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read judge response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("judge API returned %d: %s", resp.StatusCode, string(body))
	}

	var parsed anthropicResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Result{}, fmt.Errorf("unmarshal judge response: %w", err)
	}
	if parsed.Error != nil {
		return Result{}, fmt.Errorf("judge API error: %s", parsed.Error.Message)
	}

	var toolInput *rubricToolInput
	for _, block := range parsed.Content {
		if block.Type != "tool_use" || block.Name != scoreToolName {
			continue
		}
		var ti rubricToolInput
		if err := json.Unmarshal(block.Input, &ti); err != nil {
			return Result{}, fmt.Errorf("unmarshal rubric tool input: %w", err)
		}
		toolInput = &ti
		break
	}
	if toolInput == nil {
		return Result{}, fmt.Errorf("judge response had no %s tool_use block", scoreToolName)
	}

	return Result{
		Provider:            "anthropic",
		Model:               j.Model,
		CorrectnessScore:    clampScore(toolInput.CorrectnessScore),
		AdherenceScore:      clampScore(toolInput.AdherenceScore),
		ToneScore:           clampScore(toolInput.ToneScore),
		ClarityScore:        clampScore(toolInput.ClarityScore),
		TrajectoryScore:     clampScore(toolInput.TrajectoryScore),
		OverallScore:        clampScore(toolInput.OverallScore),
		Rationale:           toolInput.Rationale,
		TrajectoryRationale: toolInput.TrajectoryRationale,
		InputTokens:         parsed.Usage.InputTokens,
		OutputTokens:        parsed.Usage.OutputTokens,
	}, nil
}

func clampScore(v int) int {
	if v < MinScore {
		return MinScore
	}
	if v > MaxScore {
		return MaxScore
	}
	return v
}
