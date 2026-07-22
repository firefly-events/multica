package judge

import (
	"context"
	"encoding/json"
	"fmt"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// BuildTrajectory assembles the judge input for one completed task: the
// originating issue's title/description, the ordered tool-call sequence
// from task_message, and the task's final result/error. This is the
// full "what did the agent actually do" record the trajectory-scoring
// requirement in DOS-860 needs — grading only the final diff would miss
// wrong turns, unnecessary destructive actions, or excessive tool calls
// that the final state doesn't reveal.
func BuildTrajectory(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (Trajectory, error) {
	// issue_id is nullable (chat-originated tasks have no issue), so
	// only look it up when present rather than erroring on a task that
	// legitimately has none.
	var issueTitle, issueBody string
	if task.IssueID.Valid {
		issue, err := q.GetIssue(ctx, task.IssueID)
		if err != nil {
			return Trajectory{}, fmt.Errorf("get issue %s: %w", task.IssueID.String(), err)
		}
		issueTitle = issue.Title
		issueBody = issue.Description.String
	}

	messages, err := q.ListTaskMessages(ctx, task.ID)
	if err != nil {
		return Trajectory{}, fmt.Errorf("list task messages for %s: %w", task.ID.String(), err)
	}

	steps := make([]Step, 0, len(messages))
	for _, m := range messages {
		var inputStr string
		if len(m.Input) > 0 {
			// task_message.input is JSONB; render it compactly rather
			// than round-tripping through a typed struct since the
			// shape varies per tool.
			compact, err := json.Marshal(json.RawMessage(m.Input))
			if err == nil {
				inputStr = string(compact)
			}
		}
		steps = append(steps, Step{
			Seq:     m.Seq,
			Type:    m.Type,
			Tool:    m.Tool.String,
			Content: m.Content.String,
			Input:   inputStr,
			Output:  m.Output.String,
		})
	}

	var finalResult string
	if len(task.Result) > 0 {
		finalResult = string(task.Result)
	}

	return Trajectory{
		TaskID:      task.ID.String(),
		IssueTitle:  issueTitle,
		IssueBody:   issueBody,
		Steps:       steps,
		FinalResult: finalResult,
		FinalError:  task.Error.String,
	}, nil
}
