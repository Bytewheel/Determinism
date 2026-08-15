package session

import (
	"os"
	"testing"
)

// TestOpenDefaultDB verifies read-only access to a real opencode database when
// one is present; skipped otherwise.
func TestOpenDefaultDB(t *testing.T) {
	path := defaultDBPath()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no opencode db at %s", path)
	}
	ld, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ld.Close()
	sessions, err := ld.List(5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	t.Logf("loaded %d sessions", len(sessions))
	if len(sessions) == 0 {
		t.Fatal("expected at least one session")
	}
	s := sessions[0]
	loaded, err := ld.Load(s.ID)
	if err != nil {
		t.Fatalf("load %s: %v", s.ID, err)
	}
	t.Logf("session %s has %d messages", loaded.ID, len(loaded.Messages))
}
