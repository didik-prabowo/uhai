// Keeping the thread of multi-step work. Long jobs lose their shape: the
// model states a plan in prose on turn one and by turn six it is guessing at
// what it had meant to do, because the plan is one message among forty tool
// calls.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// The statuses a step may be in. Three, not five: "blocked" and "cancelled"
// are things to say in the step's own text, and a status nobody can act on
// differently is a word, not a state.
const (
	statusTodo  = "todo"
	statusDoing = "doing"
	statusDone  = "done"
)

// mark is how each status is drawn back to the model.
var mark = map[string]string{statusTodo: "[ ]", statusDoing: "[>]", statusDone: "[x]"}

// planTool is the set_plan tool: what the model is told about it, and the
// thing that runs.
//
// It holds nothing. The plan is the tool result, and the tool result is in the
// history the next request carries — so re-stating the plan is what keeps it
// alive, and there is no field on Agent, no lock, and nothing crossing the
// daemon's socket. The version that stores it can be built the day something
// other than the model needs to read it.
type planTool struct{}

func (planTool) Name() string       { return NamePlan }
func (planTool) NeedsConfirm() bool { return false }
func (planTool) Description() string {
	return "Write down the plan for a job of several steps, and rewrite it as the work moves. " +
		"Send the whole plan every time, not the part that changed: the newest call is the plan, " +
		"and it is how you will remember what you were doing twenty tool calls from now. " +
		"Skip it for anything a single tool call finishes."
}
func (planTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"steps": {
					"type": "array",
					"description": "The whole plan, in order",
					"items": {
						"type": "object",
						"properties": {
							"step": {"type": "string", "description": "What is to be done, in a few words"},
							"status": {"type": "string", "enum": ["todo", "doing", "done"], "description": "Default \"todo\""}
						},
						"required": ["step"]
					}
				}
			},
			"required": ["steps"]
		}`)
}

func (planTool) Run(_ context.Context, _ string, input json.RawMessage) (string, bool) {
	var args struct {
		Steps []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	if len(args.Steps) == 0 {
		return "a plan needs at least one step", true
	}

	var out []string
	done, doing := 0, 0
	for i, s := range args.Steps {
		if strings.TrimSpace(s.Step) == "" {
			return fmt.Sprintf("step %d has no text", i+1), true
		}
		status := s.Status
		if status == "" {
			status = statusTodo
		}
		box, ok := mark[status]
		if !ok {
			return fmt.Sprintf("step %d has status %q; it must be todo, doing or done", i+1, s.Status), true
		}
		switch status {
		case statusDone:
			done++
		case statusDoing:
			doing++
		}
		out = append(out, box+" "+strings.TrimSpace(s.Step))
	}

	// The tally is the part worth reading at a glance, and the warning below
	// it is the mistake this tool exists to catch: a plan with everything
	// still "todo" on the tenth call is a plan nobody is keeping.
	tail := fmt.Sprintf("%d/%d done", done, len(args.Steps))
	if doing > 1 {
		tail += ", but " + fmt.Sprintf("%d steps are marked doing — only one thing is being done at a time", doing)
	}
	return strings.Join(out, "\n") + "\n\n" + tail, false
}
