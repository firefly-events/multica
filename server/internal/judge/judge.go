// Package judge implements the LLM-as-judge sampled quality/tone scoring
// pass (DOS-860): a strong Claude/Gemini-tier model grades a sampled
// subset of completed agent_task_queue rows against an explicit rubric
// (correctness, adherence, tone, clarity) plus a trajectory score graded
// from the ordered tool-call sequence, not just the final diff.
//
// All scores produced by this package are provisional — see
// Result.CalibrationStatus / ModeledStatus — until a future calibration
// story validates them against human review.
package judge

import "context"

// ModeledStatus is the calibration_status written for every score this
// package produces until the calibration follow-up story lands.
const ModeledStatus = "MODELED"

// CalibratedStatus is reserved for the future calibration story; scoring
// code in this package never writes it today.
const CalibratedStatus = "CALIBRATED"

// Rubric dimensions are 0-100 so they can sit next to human review scores
// on the same scale once calibration lands.
const (
	MinScore = 0
	MaxScore = 100
)

// Trajectory is the ordered record of what an agent actually did on a
// task: the tool-call sequence plus enough surrounding context (the
// issue/task prompt and the final result) for a judge model to grade
// both "did it do the right thing" (rubric) and "did it get there the
// right way" (trajectory), per the LangSmith-style trajectory-scoring
// requirement in DOS-860.
type Trajectory struct {
	TaskID      string
	IssueTitle  string
	IssueBody   string
	Steps       []Step
	FinalResult string
	FinalError  string
}

// Step is one row of task_message: a single tool call (or message) the
// agent emitted, in original seq order.
type Step struct {
	Seq     int32
	Type    string
	Tool    string
	Content string
	Input   string
	Output  string
}

// Input is what the judge model is asked to grade.
type Input struct {
	Trajectory Trajectory
}

// Result is one rubric pass over a single task. Scores are 0-100.
type Result struct {
	Provider string
	Model    string

	CorrectnessScore int
	AdherenceScore   int
	ToneScore        int
	ClarityScore     int
	TrajectoryScore  int
	OverallScore     int

	Rationale           string
	TrajectoryRationale string

	InputTokens  int64
	OutputTokens int64
}

// Judge scores a single task trajectory. Implementations must be safe
// for concurrent use. Production code uses the Anthropic-backed
// implementation in anthropic.go; tests inject a fake so the scheduler
// job can be exercised without a real LLM call.
type Judge interface {
	Score(ctx context.Context, in Input) (Result, error)
}
