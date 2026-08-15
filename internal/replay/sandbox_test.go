package replay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bytewheel/determinism/internal/session"
)

func writeToolInput(path, content string) []byte {
	return []byte(`{"filePath":"` + path + `","content":` + mustJSON(content) + `}`)
}

func mustJSON(s string) string {
	out := make([]byte, 0, len(s)+16)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			out = append(out, '\\', '"')
		case c == '\\':
			out = append(out, '\\', '\\')
		case c == '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, c)
		}
	}
	out = append(out, '"')
	return string(out)
}

// TestReplaySandbox verifies that a session whose tools record absolute paths
// under the session directory are written to the sandbox (rewrite dir) and not
// to the original location.
func TestReplaySandbox(t *testing.T) {
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	origRoot := filepath.Join(t.TempDir(), "orig")

	// The "session" directory is the original project root (which must exist).
	if err := os.MkdirAll(origRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	rw, err := NewRewriter(origRoot, sandbox)
	if err != nil {
		t.Fatal(err)
	}

	s := &session.Session{
		ID:        "ses_sandbox",
		Directory: origRoot,
		Messages: []session.Message{
			{
				Role: "assistant",
				Parts: []session.Part{
					{
						Type:     "tool",
						ToolName: "write",
						Input:    writeToolInput(filepath.Join(origRoot, "a.txt"), "hello sandbox\n"),
					},
					{
						Type:     "tool",
						ToolName: "write",
						Input:    writeToolInput(filepath.Join(origRoot, "sub", "b.txt"), "nested\n"),
					},
				},
			},
		},
	}

	e := &Engine{WorkDir: sandbox, Rewrite: rw, FailGracefully: true}
	rep, err := e.Replay(s)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if fails := rep.Failures(); len(fails) > 0 {
		t.Fatalf("unexpected failures: %+v", fails)
	}

	// Files must land in the sandbox, never in the original tree.
	if _, err := os.Stat(filepath.Join(sandbox, "a.txt")); err != nil {
		t.Errorf("sandbox a.txt missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sandbox, "sub", "b.txt")); err != nil {
		t.Errorf("sandbox sub/b.txt missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(origRoot, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("original a.txt must not exist (sandbox escaped), err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(origRoot, "sub", "b.txt")); !os.IsNotExist(err) {
		t.Errorf("original sub/b.txt must not exist (sandbox escaped), err=%v", err)
	}
}

func bashToolInput(command string) []byte {
	return []byte(`{"command":` + mustJSON(command) + `}`)
}

// TestReplayBashSandbox verifies that bash commands run inside the sandbox,
// can read session files via copy-on-first-access, and that writes to session
// paths land in the sandbox, never in the original tree.
func TestReplayBashSandbox(t *testing.T) {
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	origRoot := filepath.Join(t.TempDir(), "orig")
	if err := os.MkdirAll(origRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origRoot, "PLAN.md"), []byte("plan v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rw, err := NewRewriter(origRoot, sandbox)
	if err != nil {
		t.Fatal(err)
	}

	s := &session.Session{
		ID:        "ses_bash",
		Directory: origRoot,
		Messages: []session.Message{
			{
				Role: "assistant",
				Parts: []session.Part{
					{
						Type:     "tool",
						ToolName: "bash",
						Input:    bashToolInput("cat " + filepath.Join(origRoot, "PLAN.md")),
					},
					{
						Type:     "tool",
						ToolName: "bash",
						Input:    bashToolInput("echo written > " + filepath.Join(origRoot, "out.txt")),
					},
				},
			},
		},
	}

	e := &Engine{WorkDir: sandbox, Rewrite: rw, FailGracefully: true}
	rep, err := e.Replay(s)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if fails := rep.Failures(); len(fails) > 0 {
		t.Fatalf("unexpected failures: %+v", fails)
	}
	if !strings.Contains(rep.Outcomes[0].OutputText, "plan v1") {
		t.Errorf("bash read should see the session file via copy-on-first-access, got %q", rep.Outcomes[0].OutputText)
	}
	if _, err := os.Stat(filepath.Join(sandbox, "out.txt")); err != nil {
		t.Errorf("sandbox out.txt missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(origRoot, "out.txt")); !os.IsNotExist(err) {
		t.Errorf("original out.txt must not exist (sandbox escaped), err=%v", err)
	}
	// Original PLAN.md must be untouched.
	if b, err := os.ReadFile(filepath.Join(origRoot, "PLAN.md")); err != nil || string(b) != "plan v1\n" {
		t.Errorf("original PLAN.md should be untouched, got %q err=%v", b, err)
	}
}


func TestReplaySandboxRejectsOutside(t *testing.T) {
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	origRoot := filepath.Join(t.TempDir(), "orig")
	if err := os.MkdirAll(origRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	rw, err := NewRewriter(origRoot, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere", "evil.txt")

	s := &session.Session{
		ID:        "ses_outside",
		Directory: origRoot,
		Messages: []session.Message{
			{
				Role: "assistant",
				Parts: []session.Part{
					{
						Type:     "tool",
						ToolName: "write",
						Input:    writeToolInput(outside, "nope\n"),
					},
				},
			},
		},
	}

	e := &Engine{WorkDir: sandbox, Rewrite: rw, FailGracefully: true}
	rep, err := e.Replay(s)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	fails := rep.Failures()
	if len(fails) != 1 {
		t.Fatalf("expected 1 failure, got %d: %+v", len(fails), rep.Outcomes)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("outside file must not exist, err=%v", err)
	}
}

// TestReplayPatchSandbox verifies apply_patch paths are remapped.
func TestReplayPatchSandbox(t *testing.T) {
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	origRoot := filepath.Join(t.TempDir(), "orig")
	if err := os.MkdirAll(origRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	rw, err := NewRewriter(origRoot, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	pt := "*** Begin Patch\n*** Add File: " + filepath.Join(origRoot, "patch.txt") + "\n+patched\n*** End Patch\n"
	s := &session.Session{
		ID:        "ses_patch",
		Directory: origRoot,
		Messages: []session.Message{
			{
				Role: "assistant",
				Parts: []session.Part{
					{
						Type:     "tool",
						ToolName: "apply_patch",
						Input:    []byte(`{"patchText":` + mustJSON(pt) + `}`),
					},
				},
			},
		},
	}
	e := &Engine{WorkDir: sandbox, Rewrite: rw, FailGracefully: true}
	rep, err := e.Replay(s)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if fails := rep.Failures(); len(fails) > 0 {
		t.Fatalf("unexpected failures: %+v", fails)
	}
	if _, err := os.Stat(filepath.Join(sandbox, "patch.txt")); err != nil {
		t.Errorf("sandbox patch.txt missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(origRoot, "patch.txt")); !os.IsNotExist(err) {
		t.Errorf("original patch.txt must not exist, err=%v", err)
	}
}
