// Package plan renders "implementation planning" documents from a session:
// a human-readable, ordered plan of every command and file operation that a
// replay will perform, plus planning docs describing the tool itself.
package plan

import (
	"encoding/json"
	"fmt"
	"strings"

	"bytewheel/determinism/internal/session"
)

// Step is a single planned action.
type Step struct {
	Order   int    `json:"order"`
	Role    string `json:"role"`
	PartID  string `json:"part_id"`
	Kind    string `json:"kind"`
	Detail  string `json:"detail,omitempty"`
	Session string `json:"session"`
}

// Build extracts the ordered list of replayable actions from a session.
func Build(s *session.Session) []Step {
	var steps []Step
	order := 0
	var walk func(sess *session.Session, label string)
	walk = func(sess *session.Session, label string) {
		for _, m := range sess.Messages {
			for i := range m.Parts {
				p := &m.Parts[i]
				if p.Type == "text" {
					steps = append(steps, Step{Order: order, Role: m.Role, PartID: p.ID, Kind: "text", Detail: truncate(p.Text, 120), Session: label})
					order++
					continue
				}
				if p.Type == "tool" {
					detail := toolDetail(p)
					steps = append(steps, Step{Order: order, Role: m.Role, PartID: p.ID, Kind: "tool:" + p.ToolName, Detail: detail, Session: label})
					order++
				}
			}
		}
	}
	walk(s, "root")
	return steps
}

func toolDetail(p *session.Part) string {
	var m map[string]any
	if err := json.Unmarshal(p.Input, &m); err != nil {
		return ""
	}
	switch p.ToolName {
	case "bash":
		if c, ok := m["command"].(string); ok {
			return truncate(c, 160)
		}
	case "write":
		if f, ok := m["filePath"].(string); ok {
			return truncate(f, 120)
		}
		if f, ok := m["path"].(string); ok {
			return truncate(f, 120)
		}
	case "edit":
		if f, ok := m["filePath"].(string); ok {
			return truncate(f, 120)
		}
		if f, ok := m["path"].(string); ok {
			return truncate(f, 120)
		}
	case "apply_patch":
		if pt, ok := m["patchText"].(string); ok {
			return truncate(strings.ReplaceAll(pt, "\n", " "), 120)
		}
	case "task":
		if d, ok := m["description"].(string); ok {
			return truncate(d, 120)
		}
	}
	return ""
}

// RenderMarkdown emits an implementation-plan markdown document for a session.
func RenderMarkdown(s *session.Session, steps []Step) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Implementation Plan: %s\n\n", s.Title)
	fmt.Fprintf(&b, "- Session: %s (%s)\n", s.Slug, s.ID)
	fmt.Fprintf(&b, "- Directory: `%s`\n\n", s.Directory)
	fmt.Fprintf(&b, "## Actions\n\n")
	fmt.Fprintf(&b, "| # | Session | Kind | Detail |\n")
	fmt.Fprintf(&b, "|---|---------|------|--------|\n")
	for _, st := range steps {
		detail := strings.ReplaceAll(st.Detail, "|", `\|`)
		kind := strings.ReplaceAll(st.Kind, "|", `\|`)
		fmt.Fprintf(&b, "| %d | %s | `%s` | %s |\n", st.Order, st.Session, kind, detail)
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
