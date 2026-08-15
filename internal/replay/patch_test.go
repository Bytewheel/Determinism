package replay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePatchAddDeleteUpdate(t *testing.T) {
	pt := `*** Begin Patch
*** Add File: new.txt
+line one
+line two
*** Update File: old.txt
@@
-lorem
+ipsum
*** Delete File: gone.txt
*** End Patch
`
	hunks, err := parsePatch(pt)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(hunks) != 3 {
		t.Fatalf("hunks = %d, want 3", len(hunks))
	}
	if hunks[0].Type != "add" || hunks[0].Path != "new.txt" {
		t.Errorf("hunk0 = %+v", hunks[0])
	}
	if hunks[0].Contents != "line one\nline two" {
		t.Errorf("hunk0 contents = %q", hunks[0].Contents)
	}
	if hunks[1].Type != "update" || hunks[1].Path != "old.txt" {
		t.Errorf("hunk1 = %+v", hunks[1])
	}
	if hunks[2].Type != "delete" || hunks[2].Path != "gone.txt" {
		t.Errorf("hunk2 = %+v", hunks[2])
	}
}

func TestParsePatchMove(t *testing.T) {
	pt := `*** Begin Patch
*** Update File: a.txt
*** Move to: b.txt
@@
-content
+content v2
*** End Patch
`
	hunks, err := parsePatch(pt)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(hunks) != 1 {
		t.Fatalf("hunks = %d", len(hunks))
	}
	if hunks[0].MovePath != "b.txt" {
		t.Errorf("move = %q", hunks[0].MovePath)
	}
}

func TestApplyPatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("keep\nlorem\ntail\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gone.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pt := `*** Begin Patch
*** Add File: new.txt
+hello
*** Update File: old.txt
@@
-lorem
+ipsum
*** Delete File: gone.txt
*** End Patch
`
	ops, err := ApplyPatch(dir, pt)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("ops = %d: %v", len(ops), ops)
	}
	b, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil || string(b) != "hello" {
		t.Errorf("new.txt = %q err=%v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(dir, "old.txt"))
	if err != nil || string(b) != "keep\nipsum\ntail\n" {
		t.Errorf("old.txt = %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.txt")); !os.IsNotExist(err) {
		t.Errorf("gone.txt should be deleted, err=%v", err)
	}
}

func TestParsePatchMissingMarkers(t *testing.T) {
	if _, err := parsePatch("*** Add File: x\n+1\n"); err == nil {
		t.Fatal("expected error for missing markers")
	}
}
