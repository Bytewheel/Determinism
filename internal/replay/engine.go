package replay

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"bytewheel/determinism/internal/session"
)

// Outcome describes the result of executing a single part.
type Outcome struct {
	SessionID  string
	MessageID  string
	PartID     string
	Kind       string // text, reasoning, bash, write, edit, apply_patch, task, subtask, skip
	Summary    string
	Error      string
	OutputText string
	OK         bool
}

// Report is the complete result of a replay.
type Report struct {
	Session  *session.Session
	Outcomes []Outcome
}

func (r *Report) Failures() []Outcome {
	var out []Outcome
	for _, o := range r.Outcomes {
		if !o.OK {
			out = append(out, o)
		}
	}
	return out
}

func (r *Report) Counts() (ran, failed, skipped int) {
	for _, o := range r.Outcomes {
		switch {
		case !o.OK:
			failed++
		case o.Kind == "skip":
			skipped++
		default:
			ran++
		}
	}
	return
}

// Engine replays a session's tool calls deterministically.
type Engine struct {
	// WorkDir is the base directory commands run in and where relative paths
	// resolve. If empty, defaults to the session's directory.
	WorkDir string
	// Shell is the shell used for bash tool replays. Defaults to /bin/sh.
	Shell string
	// Subprompts enables execution of task/subtask subprompt sessions.
	Subprompts bool
	// FailGracefully controls whether errors are recorded and replay continues
	// (true, the default) or replay stops at the first error.
	FailGracefully bool
	// DryRun prints planned actions without executing them.
	DryRun bool
	// Log receives human-readable progress lines.
	Log func(format string, args ...any)

	// LoadChild resolves a child session id (ses_...) to a full session. If
	// nil, subprompts cannot be executed and fail gracefully.
	LoadChild func(id string) (*session.Session, error)
	// FindChild resolves a subprompt to a child session id when the task tool
	// output did not record a task_id (e.g. the task errored). It receives the
	// parent session id and the subprompt description/title to match on.
	FindChild func(parentID, description string) (string, error)
	// SaveCache, when non-nil, is called after each successful command/write
	// so a subprompt that needs pre-existing state can restore a checkpoint.
	SaveCache func(tag string) error
}

func (e *Engine) log(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

func (e *Engine) shell() string {
	if e.Shell != "" {
		return e.Shell
	}
	return "/bin/sh"
}

// Replay runs the session and returns a report. If FailGracefully is true
// (default) all actions are attempted and failures are collected.
func (e *Engine) Replay(s *session.Session) (*Report, error) {
	if e.FailGracefully {
		e.FailGracefully = true
	}
	base := e.WorkDir
	if base == "" {
		base = s.Directory
	}
	if base == "" {
		base, _ = os.Getwd()
	}
	e.WorkDir = base

	rep := &Report{Session: s}
	for _, m := range s.Messages {
		for i := range m.Parts {
			p := &m.Parts[i]
			out := e.replayPart(s, m, p)
			rep.Outcomes = append(rep.Outcomes, out)
			if !out.OK && !e.FailGracefully {
				return rep, fmt.Errorf("replay aborted at part %s: %s", p.ID, out.Error)
			}
		}
	}
	return rep, nil
}

func (e *Engine) replayPart(s *session.Session, m session.Message, p *session.Part) Outcome {
	o := Outcome{SessionID: s.ID, MessageID: m.ID, PartID: p.ID, Kind: p.Type, OK: true}

	switch p.Type {
	case "text", "reasoning":
		o.Kind = p.Type
		o.Summary = strings.TrimSpace(truncate(p.Text, 100))
		return o
	case "tool":
		// fall through
	case "subtask":
		return e.replaySubprompt(s, m, p, o)
	default:
		o.Kind = "skip"
		o.Summary = fmt.Sprintf("unsupported part type %q", p.Type)
		return o
	}

	switch p.ToolName {
	case "bash":
		o.Kind = "bash"
		return e.replayBash(s, m, p, o)
	case "write":
		o.Kind = "write"
		return e.replayWrite(s, p, o)
	case "edit":
		o.Kind = "edit"
		return e.replayEdit(s, p, o)
	case "apply_patch":
		o.Kind = "apply_patch"
		return e.replayApplyPatch(s, p, o)
	case "task":
		return e.replaySubprompt(s, m, p, o)
	default:
		o.Kind = "skip"
		o.Summary = fmt.Sprintf("read-only / non-replayable tool %q", p.ToolName)
		return o
	}
}

func (e *Engine) replayBash(s *session.Session, m session.Message, p *session.Part, o Outcome) Outcome {
	input, err := decode[BashInput](p.Input)
	if err != nil {
		o.OK = false
		o.Error = "decode bash input: " + err.Error()
		return o
	}
	dir := e.WorkDir
	if input.Workdir != "" {
		dir = resolve(e.WorkDir, input.Workdir)
	}
	o.Summary = truncate(input.Command, 100)
	if e.DryRun {
		e.log("dry-run bash: %s", input.Command)
		return o
	}
	e.log("$ %s", input.Command)

	ctx := time.After(10 * time.Minute)
	if input.Timeout > 0 {
		ctx = time.After(time.Duration(input.Timeout) * time.Millisecond)
	}

	cmd := exec.Command(e.shell(), "-c", input.Command)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		o.OK = false
		o.Error = err.Error()
		return o
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx:
		_ = cmd.Process.Kill()
		<-done
		o.OK = false
		o.Error = "command timed out"
	case err := <-done:
		if err != nil {
			o.OK = false
			if ee, ok := err.(*exec.ExitError); ok {
				o.Error = fmt.Sprintf("exit code %d", ee.ExitCode())
			} else {
				o.Error = err.Error()
			}
		}
	}
	o.OutputText = buf.String()
	if !o.OK {
		e.log("  !! %s", o.Error)
	}
	return o
}

func (e *Engine) replayWrite(s *session.Session, p *session.Part, o Outcome) Outcome {
	input, err := decode[WriteInput](p.Input)
	if err != nil {
		o.OK = false
		o.Error = "decode write input: " + err.Error()
		return o
	}
	target := input.FilePath
	if target == "" {
		target = input.Path
	}
	if target == "" {
		o.OK = false
		o.Error = "write input has no file path"
		return o
	}
	abs := resolve(e.WorkDir, target)
	o.Summary = target
	if e.DryRun {
		e.log("dry-run write: %s (%d bytes)", target, len(input.Content))
		return o
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		o.OK = false
		o.Error = err.Error()
		return o
	}
	if err := os.WriteFile(abs, []byte(input.Content), 0o644); err != nil {
		o.OK = false
		o.Error = err.Error()
		return o
	}
	e.log("wrote %s (%d bytes)", target, len(input.Content))
	return o
}

func (e *Engine) replayEdit(s *session.Session, p *session.Part, o Outcome) Outcome {
	input, err := decode[EditInput](p.Input)
	if err != nil {
		o.OK = false
		o.Error = "decode edit input: " + err.Error()
		return o
	}
	target := input.FilePath
	if target == "" {
		target = input.Path
	}
	if target == "" {
		o.OK = false
		o.Error = "edit input has no file path"
		return o
	}
	abs := resolve(e.WorkDir, target)
	o.Summary = target
	if e.DryRun {
		e.log("dry-run edit: %s", target)
		return o
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		o.OK = false
		o.Error = err.Error()
		return o
	}
	str := string(content)
	if input.ReplaceAll {
		if !strings.Contains(str, input.OldString) {
			o.OK = false
			o.Error = "oldString not found (replaceAll)"
			return o
		}
		str = strings.ReplaceAll(str, input.OldString, input.NewString)
	} else {
		idx := strings.Index(str, input.OldString)
		if idx == -1 {
			o.OK = false
			o.Error = "oldString not found"
			return o
		}
		str = str[:idx] + input.NewString + str[idx+len(input.OldString):]
	}
	if err := os.WriteFile(abs, []byte(str), 0o644); err != nil {
		o.OK = false
		o.Error = err.Error()
		return o
	}
	e.log("edited %s", target)
	return o
}

func (e *Engine) replayApplyPatch(s *session.Session, p *session.Part, o Outcome) Outcome {
	input, err := decode[PatchInput](p.Input)
	if err != nil {
		o.OK = false
		o.Error = "decode apply_patch input: " + err.Error()
		return o
	}
	o.Summary = truncate(input.PatchText, 100)
	if e.DryRun {
		e.log("dry-run apply_patch (%d chars)", len(input.PatchText))
		return o
	}
	ops, err := ApplyPatch(e.WorkDir, input.PatchText)
	if err != nil {
		o.OK = false
		o.Error = err.Error()
		return o
	}
	for _, op := range ops {
		e.log("  %s", op)
	}
	return o
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
