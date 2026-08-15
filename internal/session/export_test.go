package session

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWriteChatTextRoundTrip(t *testing.T) {
	in := Session{
		ID:        "ses_abc123",
		Slug:      "my-session",
		Title:     "My Session",
		Directory: "/home/user/project",
		Messages: []Message{
			{Role: "user", Parts: []Part{
				{Type: "text", Text: "Build me a thing.\n"},
			}},
			{Role: "assistant", Parts: []Part{
				{Type: "text", Text: "I'll start.\n"},
				{Type: "reasoning", Text: "think step by step\n"},
				{Type: "tool", ToolName: "bash",
					Input:  []byte(`{"command":"ls -la","workdir":"/home/user/project"}`),
					Output: []byte("total 8\nfile.txt\n"),
				},
				{Type: "tool", ToolName: "write",
					Input:  []byte(`{"filePath":"/home/user/project/a.md","content":"# Hi\n"}`),
					Output: []byte(`"Created file successfully: a.md\n"`),
				},
				{Type: "tool", ToolName: "bash",
					Input: []byte(`{"command":"false"}`),
					Output: []byte("exit status 1\n"),
					Status: "error",
				},
			}},
			{Role: "user", Parts: []Part{
				{Type: "text", Text: "Now the implementation.\n"},
			}},
		},
	}

	var buf bytes.Buffer
	if err := WriteChatText(&buf, &in); err != nil {
		t.Fatalf("write: %v", err)
	}

	s, err := parseChatText(buf.Bytes())
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, buf.String())
	}
	if s.ID != in.ID {
		t.Errorf("id = %q, want %q", s.ID, in.ID)
	}
	if s.Directory != in.Directory {
		t.Errorf("directory = %q, want %q", s.Directory, in.Directory)
	}
	if s.Title != in.Title {
		t.Errorf("title = %q, want %q", s.Title, in.Title)
	}
	if len(s.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(s.Messages))
	}
	if s.Messages[0].Role != "user" || s.Messages[2].Role != "user" {
		t.Errorf("roles = %q/%q, want user/user", s.Messages[0].Role, s.Messages[2].Role)
	}
	parts := s.Messages[1].Parts
	if len(parts) != 5 {
		t.Fatalf("assistant parts = %d, want 5 (text,reasoning,bash,write,bash)", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "I'll start.\n" {
		t.Errorf("part0 = %q %q", parts[0].Type, parts[0].Text)
	}
	if parts[1].Type != "reasoning" || !strings.Contains(parts[1].Text, "step by step") {
		t.Errorf("part1 = %q %q", parts[1].Type, parts[1].Text)
	}
	if parts[2].ToolName != "bash" || parts[3].ToolName != "write" || parts[4].ToolName != "bash" {
		t.Errorf("tool names = %q/%q/%q", parts[2].ToolName, parts[3].ToolName, parts[4].ToolName)
	}
	if err := ParseToolPart(&parts[2]); err != nil {
		t.Fatalf("parse tool 2: %v", err)
	}
	var got struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(parts[2].Input, &got); err != nil {
		t.Fatalf("bash input not JSON: %v\n%s", err, parts[2].Input)
	}
	if got.Command != "ls -la" {
		t.Errorf("bash command = %q, want ls -la", got.Command)
	}
	if string(parts[2].Output) != "total 8\nfile.txt\n" {
		t.Errorf("bash output = %q", parts[2].Output)
	}
	if string(parts[3].Output) != "Created file successfully: a.md\n" {
		t.Errorf("write output (JSON-encoded) = %q", parts[3].Output)
	}
	if string(parts[4].Output) != "exit status 1\n" {
		t.Errorf("error output = %q", parts[4].Output)
	}
}

// TestWriteChatTextFencedText ensures a text part containing a markdown code
// block does not desynchronize the parser (a stray ``` must stay text, and
// subsequent tool parts must still be parsed).
func TestWriteChatTextFencedText(t *testing.T) {
	in := Session{
		Title: "fenced-text",
		Messages: []Message{
			{Role: "assistant", Parts: []Part{
				{Type: "text", Text: "Here is a plan:\n\n```\ndir/\n  file.txt\n```\n\nThen run it.\n"},
				{Type: "tool", ToolName: "bash",
					Input:  []byte(`{"command":"ls"}`),
					Output: []byte(`"file.txt\n"`),
				},
			}},
		},
	}
	var buf bytes.Buffer
	if err := WriteChatText(&buf, &in); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := parseChatText(buf.Bytes())
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, buf.String())
	}
	parts := s.Messages[0].Parts
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2 (text,bash):\n%s", len(parts), buf.String())
	}
	if parts[0].Type != "text" || !strings.Contains(parts[0].Text, "```\n") {
		t.Errorf("text part lost its code fence: %q", parts[0].Text)
	}
	if parts[1].ToolName != "bash" {
		t.Errorf("bash tool lost after fenced text: got %+v", parts[1])
	}
	if string(parts[1].Output) != "file.txt\n" {
		t.Errorf("bash output = %q", parts[1].Output)
	}
}

func TestWriteChatTextEmptyInput(t *testing.T) {	in := Session{
		Title: "empty-input",
		Messages: []Message{
			{Role: "assistant", Parts: []Part{
				{Type: "tool", ToolName: "bash"},
			}},
		},
	}
	var buf bytes.Buffer
	if err := WriteChatText(&buf, &in); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := parseChatText(buf.Bytes())
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, buf.String())
	}
	parts := s.Messages[0].Parts
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(parts))
	}
	if parts[0].ToolName != "bash" {
		t.Errorf("tool name = %q", parts[0].ToolName)
	}
	if err := ParseToolPart(&parts[0]); err != nil {
		t.Fatalf("parse tool part: %v", err)
	}
	if string(parts[0].Input) != "{}\n" {
		t.Errorf("empty input should round-trip as {}, got %q", parts[0].Input)
	}
}
