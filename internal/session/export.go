package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// chatTimeFormat is the human-readable timestamp used in exported transcripts.
const chatTimeFormat = "1/2/2006, 3:04:05 PM"

// WriteChatText writes the session in the opencode chat text format (see
// packages/tui/src/util/transcript.ts) extended with a **Directory:** line so
// reimporting the file via ParseChatText preserves the session's working
// directory. The output round-trips through ParseChatText.
func WriteChatText(w io.Writer, s *Session) error {
	title := s.Title
	if title == "" {
		title = firstNonEmptyStr(s.Slug, s.ID, "Session")
	}
	if _, err := fmt.Fprintf(w, "# %s\n\n", title); err != nil {
		return err
	}
	if s.ID != "" {
		if _, err := fmt.Fprintf(w, "**Session ID:** %s\n", s.ID); err != nil {
			return err
		}
	}
	if s.Directory != "" {
		if _, err := fmt.Fprintf(w, "**Directory:** %s\n", s.Directory); err != nil {
			return err
		}
	}
	if !s.TimeCreated.IsZero() {
		if _, err := fmt.Fprintf(w, "**Created:** %s\n", s.TimeCreated.Format(chatTimeFormat)); err != nil {
			return err
		}
	}
	if !s.TimeUpdated.IsZero() {
		if _, err := fmt.Fprintf(w, "**Updated:** %s\n", s.TimeUpdated.Format(chatTimeFormat)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "\n---\n\n"); err != nil {
		return err
	}
	for _, m := range s.Messages {
		if _, err := fmt.Fprintf(w, "## %s\n\n", roleTitle(m.Role)); err != nil {
			return err
		}
		for i := range m.Parts {
			if err := writeChatPart(w, &m.Parts[i]); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "---\n\n"); err != nil {
			return err
		}
	}
	return nil
}

func roleTitle(role string) string {
	if strings.EqualFold(strings.TrimSpace(role), "user") {
		return "User"
	}
	return "Assistant"
}

func writeChatPart(w io.Writer, p *Part) error {
	switch p.Type {
	case "text":
		_, err := fmt.Fprintf(w, "%s\n\n", p.Text)
		return err
	case "reasoning":
		_, err := fmt.Fprintf(w, "_Thinking:_\n\n%s\n\n", p.Text)
		return err
	case "subtask":
		desc := strings.TrimSpace(p.Description)
		if desc == "" {
			desc = firstNonEmptyStr(p.Prompt, "subtask")
		}
		if _, err := fmt.Fprintf(w, "**Subtask:** %s\n\n", desc); err != nil {
			return err
		}
		if strings.TrimSpace(p.Prompt) != "" {
			if _, err := fmt.Fprintf(w, "%s\n\n", strings.TrimSpace(p.Prompt)); err != nil {
				return err
			}
		}
		return nil
	case "tool":
		// handled below
	default:
		_, err := fmt.Fprintf(w, "%s\n\n", p.Text)
		return err
	}

	name := p.ToolName
	if name == "" {
		name = "unknown"
	}
	if _, err := fmt.Fprintf(w, "**Tool: %s**\n\n", name); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "**Input:**\n```json\n"); err != nil {
		return err
	}
	if len(bytes.TrimSpace(p.Input)) == 0 {
		if _, err := fmt.Fprint(w, "{}\n"); err != nil {
			return err
		}
	} else {
		var indented bytes.Buffer
		if json.Valid(p.Input) {
			_ = json.Indent(&indented, p.Input, "", "  ")
		}
		if indented.Len() == 0 {
			indented.Write(p.Input)
		}
		if _, err := w.Write(indented.Bytes()); err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, "\n"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(w, "```\n\n"); err != nil {
		return err
	}

	if len(bytes.TrimSpace(p.Output)) == 0 {
		return nil
	}
	out, ok := plainOutput(p.Output)
	if !ok {
		return nil
	}
	header := "**Output:**"
	if p.Status == "error" {
		header = "**Error:**"
	}
	if _, err := fmt.Fprintf(w, "%s\n```\n", header); err != nil {
		return err
	}
	if _, err := w.Write(out); err != nil {
		return err
	}
	if !bytes.HasSuffix(out, []byte("\n")) {
		if _, err := fmt.Fprint(w, "\n"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(w, "```\n\n"); err != nil {
		return err
	}
	return nil
}

// plainOutput converts a tool part's recorded output to plain text. opencode
// stores tool output in the database as a JSON-encoded string; decode it so
// the exported transcript is human readable. Non-JSON content (e.g. output
// captured from a chat-text transcript) is passed through untouched.
func plainOutput(raw []byte) ([]byte, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []byte(s), true
	}
	return raw, true
}

func firstNonEmptyStr(parts ...string) string {
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			return p
		}
	}
	return ""
}
