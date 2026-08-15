package replay

import (
	"fmt"
	"strings"

	"bytewheel/determinism/internal/session"
)

// replaySubprompt handles task tool calls and subtask parts.
//
// Graceful-failure semantics:
//   - If subprompt execution is disabled, the subprompt is recorded as skipped.
//   - If enabled, the child session is replayed by first replaying all of the
//     commands and read/write actions from the prompts that lead up to it (the
//     parent's earlier actions, which the engine has already executed), then
//     replaying the child's own actions.
//   - If any command or write fails leading up to the subprompt, or the child
//     session cannot be loaded, the subprompt fails gracefully and replay
//     continues.
func (e *Engine) replaySubprompt(s *session.Session, m session.Message, p *session.Part, o Outcome) Outcome {
	o.Kind = p.Type
	if p.Type == "tool" {
		o.Kind = "task"
	}

	desc, prompt := p.Description, p.Prompt
	if p.Type == "tool" {
		if input, err := decode[TaskInput](p.Input); err == nil {
			desc, prompt = input.Description, input.Prompt
		}
	}
	o.Summary = truncate(firstNonEmpty(desc, prompt), 120)

	if !e.Subprompts {
		o.Kind = "skip"
		o.Summary = "subprompt (disabled): " + o.Summary
		return o
	}

	childID := p.ChildID
	if childID == "" && e.FindChild != nil {
		if id, err := e.FindChild(s.ID, desc); err == nil && id != "" {
			childID = id
		}
	}
	if childID == "" {
		o.OK = false
		o.Error = "subprompt has no resolvable child session"
		return o
	}
	if e.LoadChild == nil {
		o.OK = false
		o.Error = "subprompt execution enabled but LoadChild is not configured"
		return o
	}

	e.log("subprompt: %s (child %s)", o.Summary, childID)
	child, err := e.LoadChild(childID)
	if err != nil {
		o.OK = false
		o.Error = "load child session: " + err.Error()
		return o
	}

	// Replay the child session in the same workdir. The parent's leading-up
	// actions were already executed because the engine replays linearly.
	childRep, err := e.Replay(child)
	if err != nil {
		o.OK = false
		o.Error = "child replay: " + err.Error()
		return o
	}
	if fails := childRep.Failures(); len(fails) > 0 {
		o.OK = false
		o.Error = fmt.Sprintf("child session had %d failing step(s)", len(fails))
	}
	return o
}

func firstNonEmpty(parts ...string) string {
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			return p
		}
	}
	return ""
}
