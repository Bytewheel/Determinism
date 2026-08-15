package session

import "testing"

func TestParseChatText(t *testing.T) {
	input := `# Steganographic crypto-funded VPN design and setup

**Session ID:** ses_ff9a47e03ffee4kBk6jjJuj3G5
**Created:** 4/15/2026, 10:30:00 AM
**Updated:** 4/15/2026, 11:45:00 AM

---

## User

Build me a VPN design doc.

---

## Assistant

I'll get started on that.

**Tool: bash**

**Input:**
` + "```json" + `
{
  "command": "ls -la",
  "workdir": "/home/burningserenity/Documents/hawksnestsoftware"
}
` + "```" + `

**Output:**
` + "```" + `
total 8
drwxr-xr-x 3 user user 4096 Apr 15 10:31 .
` + "```" + `

**Tool: write**

**Input:**
` + "```json" + `
{
  "filePath": "/home/burningserenity/Documents/hawksnestsoftware/vpn.md",
  "content": "# VPN\n\nDesign doc.\n"
}
` + "```" + `

**Output:**
` + "```" + `
Created file successfully: vpn.md
` + "```" + `

---

## User

Now write the implementation.

---

## Assistant

Writing it now.

**Tool: apply_patch**

**Input:**
` + "```json" + `
{
  "patchText": "*** Begin Patch\n*** Update File: /home/burningserenity/Documents/hawksnestsoftware/vpn.md\n*** End Patch\n"
}
` + "```" + `

**Output:**
` + "```" + `
Applied patch.
` + "```" + `

---
`

	s, err := parseChatText([]byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.ID != "ses_ff9a47e03ffee4kBk6jjJuj3G5" {
		t.Errorf("id = %q", s.ID)
	}
	if s.Title != "Steganographic crypto-funded VPN design and setup" {
		t.Errorf("title = %q", s.Title)
	}
	if len(s.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(s.Messages))
	}
	if s.Messages[0].Role != "user" {
		t.Errorf("msg0 role = %q", s.Messages[0].Role)
	}
	if got := s.Messages[0].Parts[0].Text; got != "Build me a VPN design doc.\n" {
		t.Errorf("msg0 text = %q", got)
	}
	parts := s.Messages[1].Parts
	if len(parts) != 3 {
		t.Fatalf("msg1 parts = %d, want 3 (text,bash,write)", len(parts))
	}
	if parts[0].Type != "text" || parts[1].ToolName != "bash" || parts[2].ToolName != "write" {
		t.Errorf("msg1 part types = %q/%q/%q", parts[0].Type, parts[1].ToolName, parts[2].ToolName)
	}
	if err := ParseToolPart(&parts[1]); err != nil {
		t.Fatalf("parse tool part: %v", err)
	}
	// bash input should be JSON
	if len(parts[1].Input) == 0 {
		t.Error("bash part has no input")
	}
}
