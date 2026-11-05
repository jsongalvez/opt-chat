package main

import (
	"os"
	"path/filepath"
	"testing"
)

// opt-chat's own hooks.json goes into each call's settings, and only then
// do hooks run in it at all.
func TestOwnHooks(t *testing.T) {
	a := &Agent{chat: &Chat{dir: t.TempDir()}}
	if set := a.ownSettings(0, nil); set != `{"disableAllHooks":true,"autoCompactEnabled":false}` {
		t.Fatalf("no hooks.json: %s", set)
	}
	for _, bad := range []string{"", "{}", "[1]", "not json"} {
		os.WriteFile(filepath.Join(a.chat.dir, "hooks.json"), []byte(bad), 0o600)
		if set := a.ownSettings(0, nil); set != `{"disableAllHooks":true,"autoCompactEnabled":false}` {
			t.Fatalf("hooks.json %q: %s", bad, set)
		}
	}
	os.WriteFile(filepath.Join(a.chat.dir, "hooks.json"), []byte(`{"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "t"}]}]}`), 0o600)
	want := `{"disableAllHooks":false,"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"t"}]}]},"autoCompactEnabled":false}`
	if set := a.ownSettings(0, nil); set != want {
		t.Fatalf("with hooks:\n got %s\nwant %s", set, want)
	}
}
