package replay

import (
	"path/filepath"
	"testing"
)

func TestRewriterResolve(t *testing.T) {
	sess, err := NewRewriter("/home/u/proj", "/tmp/sandbox")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		in    string
		want  string
		ok    bool
	}{
		{"/home/u/proj", "/tmp/sandbox", true},
		{"/home/u/proj/src/main.go", "/tmp/sandbox/src/main.go", true},
		{"/home/u/proj/a/b/c.txt", "/tmp/sandbox/a/b/c.txt", true},
		{"rel/file.go", "/tmp/sandbox/rel/file.go", true},
		{"", "/tmp/sandbox", true},
		{"/tmp/sandbox/src/main.go", "", false},  // already sandboxed → outside session dir
		{"/home/u/other/x", "", false},            // outside session dir
		{"/home/u/proj2/x", "", false},            // prefix sibling
		{"/", "", false},
		{"../../etc/passwd", "", false},           // traversal escapes
	}
	for _, tt := range tests {
		got, err := sess.Resolve(tt.in)
		if tt.ok {
			if err != nil {
				t.Errorf("Resolve(%q) unexpected err: %v", tt.in, err)
				continue
			}
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.in, got, tt.want)
			}
		} else if err == nil {
			t.Errorf("Resolve(%q) = %q, want error", tt.in, got)
		}
	}
}

func TestRewriterRelativeToWorkdir(t *testing.T) {
	// The engine's WorkDir is the sandbox TargetDir; a rewriter used there must
	// resolve the session dir and the workdir consistently.
	base := t.TempDir()
	sess, err := NewRewriter(base, base)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sess.Resolve(".")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) != filepath.Clean(base) {
		t.Errorf("Resolve(.) = %q, want %q", got, base)
	}
}
