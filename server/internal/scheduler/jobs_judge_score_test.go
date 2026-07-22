package scheduler

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/judge"
)

// fakeJudge is a deterministic, in-memory Judge used so these tests
// never depend on network access or a real Anthropic API key. It
// records every task id it was asked to score so assertions can be
// scoped to a single test's fixture rows even though the sampler
// handler draws its candidate pool from the whole (shared, possibly
// non-empty) test database.
type fakeJudge struct {
	model      string
	err        error
	result     judge.Result
	calledTask []string
}

func (f *fakeJudge) Score(_ context.Context, in judge.Input) (judge.Result, error) {
	f.calledTask = append(f.calledTask, in.Trajectory.TaskID)
	if f.err != nil {
		return judge.Result{}, f.err
	}
	r := f.result
	if r.Provider == "" {
		r.Provider = "anthropic"
	}
	if r.Model == "" {
		r.Model = f.model
	}
	return r, nil
}

func (f *fakeJudge) callsFor(taskIDs []string) int {
	want := make(map[string]bool, len(taskIDs))
	for _, id := range taskIDs {
		want[id] = true
	}
	n := 0
	for _, id := range f.calledTask {
		if want[id] {
			n++
		}
	}
	return n
}

func passingFakeJudge(model string) *fakeJudge {
	return &fakeJudge{
		model: model,
		result: judge.Result{
			CorrectnessScore:    90,
			AdherenceScore:      85,
			ToneScore:           80,
			ClarityScore:        88,
			TrajectoryScore:     75,
			OverallScore:        86,
			Rationale:           "solid fix, matched acceptance criteria",
			TrajectoryRationale: "efficient tool use, no dead ends",
			InputTokens:         1234,
			OutputTokens:        200,
		},
	}
}

type judgeFixture struct {
	WorkspaceID string
	AgentID     string
	IssueID     string
	TaskIDs     []string
}

// seedJudgeFixture creates one workspace/agent/issue and N completed
// agent_task_queue rows (each with one task_message row) so the sampler
// handler has a real candidate pool to draw from. completed_at is now(),
// so these rows sort after any pre-existing completed tasks in the
// (oldest-first) candidate query; tests use a BatchLimit large enough
// to still reach them.
func seedJudgeFixture(t *testing.T, pool *pgxpool.Pool, n int) judgeFixture {
	t.Helper()
	ctx := context.Background()

	var wsID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id
	`, "judge-score-test", uniqueSlug(t)).Scan(&wsID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID) })

	var runtimeID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status)
		VALUES ($1, 'Judge Test Runtime', 'cloud', 'multica_cloud', 'online') RETURNING id
	`, wsID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert agent_runtime: %v", err)
	}

	var agentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, runtime_mode, runtime_id)
		VALUES ($1, 'Judge Test Agent', 'cloud', $2) RETURNING id
	`, wsID, runtimeID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	var issueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, description, creator_type, creator_id, assignee_type, assignee_id)
		VALUES ($1, 'Fix the thing', 'Do the fix per spec', 'agent', $2, 'agent', $2)
		RETURNING id
	`, wsID, agentID).Scan(&issueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}

	taskIDs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		var taskID string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, completed_at, result)
			VALUES ($1, $2, $3, 'completed', 0, now(), $4)
			RETURNING id
		`, agentID, runtimeID, issueID, []byte(`{"comment":"done"}`)).Scan(&taskID); err != nil {
			t.Fatalf("insert task %d: %v", i, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO task_message (task_id, seq, type, tool, content, output)
			VALUES ($1, 1, 'tool_call', 'Edit', 'apply the fix', 'ok')
		`, taskID); err != nil {
			t.Fatalf("insert task_message %d: %v", i, err)
		}
		taskIDs = append(taskIDs, taskID)
	}

	return judgeFixture{WorkspaceID: wsID, AgentID: agentID, IssueID: issueID, TaskIDs: taskIDs}
}

func uniqueSlug(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("judge-score-test-%s", uniqueJobName(t, "ws"))
}

// bigBatchLimit is large enough to sweep past whatever pre-existing
// completed tasks a shared dev/CI database already has, so every test
// fixture's own (newest, sorted-last) rows are always reached.
const bigBatchLimit = int32(20000)

// TestJudgeScoreSamplerScoresSampledSubset is the integration test the
// DOS-860 acceptance criteria call for: given completed tasks, running
// the sampler with a mocked judge writes judge_score rows (with
// rationale + judge model recorded) only for the sampled subset, never
// for every completed task.
func TestJudgeScoreSamplerScoresSampledSubset(t *testing.T) {
	pool := integrationPool(t)
	// No separate judge_score cleanup here: seedJudgeFixture's own
	// t.Cleanup deletes the fixture workspace, which cascades through
	// agent -> agent_task_queue -> judge_score/judge_sample_decision. A
	// bare `DELETE FROM judge_score` would instead wipe every row in
	// whatever database DATABASE_URL points at (the shared local dev DB
	// when unset), not just this test's fixture rows.
	fx := seedJudgeFixture(t, pool, 20)

	fake := passingFakeJudge("claude-opus-4.8")
	cfg := JudgeScoreSamplerConfig{SampleRate: 0.5, BatchLimit: bigBatchLimit}
	handler := makeJudgeScoreSamplerHandler(pool, fake, cfg)

	if _, err := handler(context.Background(), HandlerInput{}); err != nil {
		t.Fatalf("handler: %v", err)
	}

	var scoredCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM judge_score WHERE task_id = ANY($1)
	`, fx.TaskIDs).Scan(&scoredCount); err != nil {
		t.Fatalf("count judge_score: %v", err)
	}

	if scoredCount == 0 || scoredCount == len(fx.TaskIDs) {
		t.Fatalf("expected a strict subset scored at 50%% sample rate over %d fixture tasks, got %d scored",
			len(fx.TaskIDs), scoredCount)
	}
	if fake.callsFor(fx.TaskIDs) != scoredCount {
		t.Fatalf("judge was called %d times for fixture tasks but only %d scores landed", fake.callsFor(fx.TaskIDs), scoredCount)
	}

	var provider, model, rationale, calibration string
	if err := pool.QueryRow(context.Background(), `
		SELECT judge_provider, judge_model, rationale, calibration_status
		FROM judge_score WHERE task_id = ANY($1) LIMIT 1
	`, fx.TaskIDs).Scan(&provider, &model, &rationale, &calibration); err != nil {
		t.Fatalf("read judge_score row: %v", err)
	}
	if provider != "anthropic" || model != "claude-opus-4.8" {
		t.Fatalf("expected judge provider/model recorded, got %q/%q", provider, model)
	}
	if rationale == "" {
		t.Fatalf("expected rationale to be recorded")
	}
	if calibration != "MODELED" {
		t.Fatalf("expected calibration_status=MODELED before calibration lands, got %q", calibration)
	}
}

// TestJudgeScoreSamplerZeroRateScoresNothing covers the cost-bound
// acceptance criterion directly: a 0% sample rate must not call the
// judge or write any rows for a task, regardless of how many completed
// tasks exist.
func TestJudgeScoreSamplerZeroRateScoresNothing(t *testing.T) {
	pool := integrationPool(t)
	// No separate judge_score cleanup here: seedJudgeFixture's own
	// t.Cleanup deletes the fixture workspace, which cascades through
	// agent -> agent_task_queue -> judge_score/judge_sample_decision. A
	// bare `DELETE FROM judge_score` would instead wipe every row in
	// whatever database DATABASE_URL points at (the shared local dev DB
	// when unset), not just this test's fixture rows.
	fx := seedJudgeFixture(t, pool, 10)

	fake := passingFakeJudge("claude-opus-4.8")
	cfg := JudgeScoreSamplerConfig{SampleRate: 0, BatchLimit: bigBatchLimit}
	handler := makeJudgeScoreSamplerHandler(pool, fake, cfg)

	if _, err := handler(context.Background(), HandlerInput{}); err != nil {
		t.Fatalf("handler: %v", err)
	}

	if fake.callsFor(fx.TaskIDs) != 0 {
		t.Fatalf("expected judge to never be called for fixture tasks at sample_rate=0, got %d calls", fake.callsFor(fx.TaskIDs))
	}

	var scoredCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM judge_score WHERE task_id = ANY($1)
	`, fx.TaskIDs).Scan(&scoredCount); err != nil {
		t.Fatalf("count judge_score: %v", err)
	}
	if scoredCount != 0 {
		t.Fatalf("expected zero judge_score rows at sample_rate=0, got %d", scoredCount)
	}
}

// TestJudgeScoreSamplerSkipsAlreadyScoredTasks ensures a second run
// doesn't re-score (and re-bill) a task that already has a judge_score
// row, since ListUnjudgedCompletedTasks excludes it from the candidate
// pool entirely.
func TestJudgeScoreSamplerSkipsAlreadyScoredTasks(t *testing.T) {
	pool := integrationPool(t)
	// No separate judge_score cleanup here: seedJudgeFixture's own
	// t.Cleanup deletes the fixture workspace, which cascades through
	// agent -> agent_task_queue -> judge_score/judge_sample_decision. A
	// bare `DELETE FROM judge_score` would instead wipe every row in
	// whatever database DATABASE_URL points at (the shared local dev DB
	// when unset), not just this test's fixture rows.
	fx := seedJudgeFixture(t, pool, 5)

	fake := passingFakeJudge("claude-opus-4.8")
	cfg := JudgeScoreSamplerConfig{SampleRate: 1, BatchLimit: bigBatchLimit}
	handler := makeJudgeScoreSamplerHandler(pool, fake, cfg)

	if _, err := handler(context.Background(), HandlerInput{}); err != nil {
		t.Fatalf("first handler run: %v", err)
	}
	firstFixtureCalls := fake.callsFor(fx.TaskIDs)
	if firstFixtureCalls != len(fx.TaskIDs) {
		t.Fatalf("expected all %d fixture tasks scored at sample_rate=1, got %d judge calls for them", len(fx.TaskIDs), firstFixtureCalls)
	}

	var scoredCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM judge_score WHERE task_id = ANY($1)
	`, fx.TaskIDs).Scan(&scoredCount); err != nil {
		t.Fatalf("count judge_score after first run: %v", err)
	}
	if scoredCount != len(fx.TaskIDs) {
		t.Fatalf("expected %d judge_score rows after first run, got %d", len(fx.TaskIDs), scoredCount)
	}

	if _, err := handler(context.Background(), HandlerInput{}); err != nil {
		t.Fatalf("second handler run: %v", err)
	}

	if fake.callsFor(fx.TaskIDs) != firstFixtureCalls {
		t.Fatalf("expected no additional judge calls for already-scored fixture tasks on second run, went from %d to %d",
			firstFixtureCalls, fake.callsFor(fx.TaskIDs))
	}
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM judge_score WHERE task_id = ANY($1)
	`, fx.TaskIDs).Scan(&scoredCount); err != nil {
		t.Fatalf("count judge_score after second run: %v", err)
	}
	if scoredCount != len(fx.TaskIDs) {
		t.Fatalf("expected still exactly %d judge_score rows after second run (no duplicates), got %d", len(fx.TaskIDs), scoredCount)
	}
}
