package replay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bytewheel/determinism/internal/cache"
)

func cacheForTest(t *testing.T) (*cache.Cache, error) {
	t.Helper()
	return cache.New(filepath.Join(t.TempDir(), "cachedir"))
}

// fakeRewriter builds a Rewriter with a sandbox and a sibling root laid out on
// disk. sibling is the real sibling repo dir (e.g. .../hawksnestsoftware/sib).
func fakeRewriter(t *testing.T, sessionDir, sib string) (*Rewriter, string) {
	t.Helper()
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if sessionDir != "" {
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if sib != "" {
		if err := os.MkdirAll(sib, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rw, err := NewRewriter(sessionDir, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	return rw, sandbox
}

func TestRewriteCommandPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hawksnest")
	sessionDir := filepath.Join(root, "dvpn")
	sib := filepath.Join(root, "princess-vpn")
	rw, sandbox := fakeRewriter(t, sessionDir, sib)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"cd into session dir",
			"cd " + sessionDir + " && git status",
			"cd " + sandbox + " && git status",
		},
		{
			"absolute session path",
			"find " + sessionDir + " -type f",
			"find " + sandbox + " -type f",
		},
		{
			"absolute path under session dir",
			"find " + sessionDir + "/docs -type f",
			"find " + filepath.Join(sandbox, "docs") + " -type f",
		},
		{
			"relative path untouched",
			"mkdir -p docs && ls docs",
			"mkdir -p docs && ls docs",
		},
		{
			"system path untouched",
			"echo hi > /dev/null",
			"echo hi > /dev/null",
		},
		{
			"quoted path",
			"grep -l foo '" + sessionDir + "/docs/*'",
			"grep -l foo '" + filepath.Join(sandbox, "docs") + "/*'",
		},
		{
			"assignment path",
			"FOO=" + sessionDir + " && echo $FOO",
			"FOO=" + sandbox + " && echo $FOO",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rw.RewriteCommand(tc.in)
			if err != nil {
				t.Fatalf("RewriteCommand: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestRewriteCommandHeredoc(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hawksnest")
	sessionDir := filepath.Join(root, "dvpn")
	rw, sandbox := fakeRewriter(t, sessionDir, "")
	in := "cat <<EOF\n" + sessionDir + "/inside.txt\nEOF\n"
	want := "cat <<EOF\n" + sessionDir + "/inside.txt\nEOF\n"
	got, err := rw.RewriteCommand(in)
	if err != nil {
		t.Fatalf("RewriteCommand: %v", err)
	}
	if got != want {
		t.Errorf("heredoc body must be untouched:\ngot  %q\nwant %q", got, want)
	}
	if strings.Contains(got, sandbox) {
		t.Errorf("heredoc body leaked sandbox path: %q", got)
	}
}

func TestResolveSiblingRejected(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hawksnest")
	sessionDir := filepath.Join(root, "dvpn")
	sib := filepath.Join(root, "princess-vpn")
	rw, _ := fakeRewriter(t, sessionDir, sib)

	// No cache, no dangerous: sibling access must be rejected.
	_, err := rw.Resolve(filepath.Join(sib, "src", "x.c"))
	if err == nil {
		t.Fatal("expected sibling rejection without --cache/--dangerous")
	}
	if !strings.Contains(err.Error(), "--cache") || !strings.Contains(err.Error(), "--dangerous") {
		t.Errorf("error should mention --cache and --dangerous, got: %v", err)
	}
}

func TestResolveSiblingCached(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hawksnest")
	sessionDir := filepath.Join(root, "dvpn")
	sib := filepath.Join(root, "princess-vpn")
	if err := os.MkdirAll(filepath.Join(sib, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sib, "src", "x.c"), []byte("int x;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rw, _ := fakeRewriter(t, sessionDir, sib)
	c, err := cacheForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	rw.Cache = c

	resolved, err := rw.Resolve(filepath.Join(sib, "src", "x.c"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasPrefix(resolved, rw.MountsDir) {
		t.Errorf("resolved sibling should live under mounts dir, got %q", resolved)
	}
	// Content must have been copied from the real path.
	b, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatalf("read resolved file: %v", err)
	}
	if string(b) != "int x;\n" {
		t.Errorf("got %q", string(b))
	}
	// The real sibling must be untouched.
	if fi, err := os.Stat(filepath.Join(sib, "src", "x.c")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("real sibling file should still exist, err=%v", err)
	}
}

func TestResolveSiblingDangerous(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hawksnest")
	sessionDir := filepath.Join(root, "dvpn")
	sib := filepath.Join(root, "princess-vpn")
	rw, _ := fakeRewriter(t, sessionDir, sib)
	rw.Dangerous = true
	rw.Allowed = map[string]bool{}

	// Not approved: rejected.
	if _, err := rw.Resolve(filepath.Join(sib, "x")); err == nil {
		t.Fatal("expected rejection for unapproved sibling")
	}

	// Approved: real path is used, and tracked for sync.
	rw.Allowed[sib] = true
	resolved, err := rw.Resolve(filepath.Join(sib, "x"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != filepath.Join(sib, "x") {
		t.Errorf("dangerous mode should return the real path, got %q", resolved)
	}
}

func TestSyncSiblings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hawksnest")
	sessionDir := filepath.Join(root, "dvpn")
	sib := filepath.Join(root, "princess-vpn")
	if err := os.MkdirAll(sib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sib, "a.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rw, _ := fakeRewriter(t, sessionDir, sib)
	c, err := cacheForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	rw.Cache = c
	rw.Dangerous = true
	rw.Allowed = map[string]bool{sib: true}

	// Touch the sibling at its real path, then modify the real file.
	if _, err := rw.Resolve(filepath.Join(sib, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sib, "a.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without a sync, the cached snapshot is stale.
	key := cache.SiblingKey(sib)
	if !c.Exists(key) {
		t.Fatal("expected a sibling snapshot to exist")
	}

	if err := rw.SyncSiblings(); err != nil {
		t.Fatalf("SyncSiblings: %v", err)
	}
	// Verify the cache now has v2 by restoring from the same cache dir.
	fresh := filepath.Join(t.TempDir(), "fresh")
	if err := c.Restore(key, fresh); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(fresh, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "v2\n" {
		t.Errorf("cache should reflect real-time sync, got %q", string(b))
	}
}

func TestRewriteRelativeSibling(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hawksnest")
	sessionDir := filepath.Join(root, "dvpn")
	sib := filepath.Join(root, "princess-vpn")
	if err := os.MkdirAll(sib, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := "cd ../princess-vpn && git status"
	wantErr := "references sibling"

	// Without cache/dangerous the escaping reference must fail gracefully.
	rw, _ := fakeRewriter(t, sessionDir, sib)
	if _, err := rw.RewriteCommand(cmd); err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Errorf("expected graceful rejection, got %v", err)
	}

	// With cache it rewrites to the extracted sibling mount.
	rw, _ = fakeRewriter(t, sessionDir, sib)
	c, err := cacheForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	rw.Cache = c
	got, err := rw.RewriteCommand(cmd)
	if err != nil {
		t.Fatalf("RewriteCommand with cache: %v", err)
	}
	want := "cd " + filepath.Join(rw.MountsDir, "princess-vpn") + " && git status"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestReferencedSiblingsRelative(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hawksnest")
	sessionDir := filepath.Join(root, "dvpn")
	sib := filepath.Join(root, "princess-vpn")
	rw, _ := fakeRewriter(t, sessionDir, sib)
	got := rw.ReferencedSiblings("cat ../princess-vpn/src/x.c; cd /usr/local; ls ..")
	if len(got) != 1 || got[0] != sib {
		t.Errorf("expected sibling %q, got %v", sib, got)
	}
}

// TestCopyOnFirstAccess verifies that resolving a session path copies the real
// file into the sandbox mirror (so replay reads work) without touching the
// original.
func TestCopyOnFirstAccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hawksnest")
	sessionDir := filepath.Join(root, "dvpn")
	if err := os.MkdirAll(filepath.Join(sessionDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "PLAN.md"), []byte("plan v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rw, sandbox := fakeRewriter(t, sessionDir, "")

	abs, err := rw.Resolve(filepath.Join(sessionDir, "PLAN.md"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(abs)
	if err != nil || string(b) != "plan v1\n" {
		t.Errorf("mirror should contain the original content, got %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(sandbox, "docs")); !os.IsNotExist(err) {
		t.Errorf("docs dir should not be created until accessed, err=%v", err)
	}

	rel, err := rw.Resolve("docs/modules.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rel); !os.IsNotExist(err) {
		t.Errorf("relative path to a missing file should not be created, err=%v", err)
	}

	// A relative path to an existing file mirrors it.
	if err := os.WriteFile(filepath.Join(sessionDir, "docs", "modules.md"), []byte("mod v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err = rw.Resolve("docs/modules.md")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(rel); err != nil || string(b) != "mod v1\n" {
		t.Errorf("relative mirror should be populated, got %q err=%v", b, err)
	}

	// The original tree must be untouched.
	if b, err := os.ReadFile(filepath.Join(sessionDir, "PLAN.md")); err != nil || string(b) != "plan v1\n" {
		t.Errorf("original PLAN.md should be untouched, got %q err=%v", b, err)
	}
}

// TestCopyOnFirstAccessSkippedInDryRun verifies dry runs never read or copy
// real files.
func TestCopyOnFirstAccessSkippedInDryRun(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hawksnest")
	sessionDir := filepath.Join(root, "dvpn")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "PLAN.md"), []byte("plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rw, sandbox := fakeRewriter(t, sessionDir, "")
	rw.Dry = true
	resolved, err := rw.Resolve(filepath.Join(sessionDir, "PLAN.md"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved != filepath.Join(sandbox, "PLAN.md") {
		t.Errorf("dry run should still map to the sandbox, got %q", resolved)
	}
	if _, err := os.Stat(resolved); !os.IsNotExist(err) {
		t.Errorf("dry run must not copy files, err=%v", err)
	}
}
