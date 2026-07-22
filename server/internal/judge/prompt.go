package judge

import (
	"fmt"
	"strings"
)

// rubricInstructions is the explicit scoring rubric handed to the judge
// model. Kept in one place so the four rubric dimensions plus the
// trajectory dimension stay in sync with the judge_score columns and
// with scoreSchema below.
const rubricInstructions = `You are grading a coding agent's completed task. Score five dimensions from 0-100:

- correctness: did the final result actually satisfy the task's acceptance criteria / requirements, without introducing bugs?
- adherence: did the agent follow the explicit instructions, repo conventions, and any constraints stated in the task?
- tone: is the agent's final comment/output professional, appropriately concise, and free of overclaiming or filler?
- clarity: is the final result (code, comment, or answer) easy for a human reviewer to understand and verify?
- trajectory: judged from the tool-call sequence alone (not the final result) — did the agent take a reasonable, efficient path, or did it flail, repeat failed actions, take unnecessary destructive/irreversible steps, or waste calls?

Provide a short rationale for the four content dimensions and a separate short rationale specifically for the trajectory dimension. Then give an overall 0-100 score summarizing the task quality as a whole (not a plain average — weigh correctness most heavily).

Be strict: reserve 90+ for genuinely excellent work. Use the full range; do not default to the 70-85 band unless it's warranted.`

// BuildPrompt renders the full user-turn prompt for one judge call:
// rubric instructions, task context, and the numbered trajectory.
func BuildPrompt(t Trajectory) string {
	var b strings.Builder
	b.WriteString(rubricInstructions)
	b.WriteString("\n\n## Task\n\n")
	fmt.Fprintf(&b, "Title: %s\n\n", t.IssueTitle)
	if t.IssueBody != "" {
		fmt.Fprintf(&b, "Description:\n%s\n\n", t.IssueBody)
	}

	b.WriteString("## Agent trajectory (tool calls in order)\n\n")
	if len(t.Steps) == 0 {
		b.WriteString("(no recorded tool-call steps)\n\n")
	}
	for _, s := range t.Steps {
		fmt.Fprintf(&b, "%d. [%s]", s.Seq, s.Type)
		if s.Tool != "" {
			fmt.Fprintf(&b, " tool=%s", s.Tool)
		}
		b.WriteString("\n")
		if s.Content != "" {
			fmt.Fprintf(&b, "   content: %s\n", truncate(s.Content, 2000))
		}
		if s.Input != "" {
			fmt.Fprintf(&b, "   input: %s\n", truncate(s.Input, 2000))
		}
		if s.Output != "" {
			fmt.Fprintf(&b, "   output: %s\n", truncate(s.Output, 2000))
		}
	}

	b.WriteString("\n## Final outcome\n\n")
	if t.FinalError != "" {
		fmt.Fprintf(&b, "error: %s\n", truncate(t.FinalError, 4000))
	}
	if t.FinalResult != "" {
		fmt.Fprintf(&b, "result: %s\n", truncate(t.FinalResult, 8000))
	}
	if t.FinalError == "" && t.FinalResult == "" {
		b.WriteString("(no recorded result or error)\n")
	}

	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
