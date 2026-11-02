package main

import (
	"strings"
	"testing"
)

// The owed list: Opt adds an item only citing a user's message, a subagent
// none; the user's /done closes one, or Opt citing the user's confirming message; each turn opens with the list,
// and it outlasts a restart.
func TestOwed(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 100)
	c.Log("user", "make the build green")
	c.Log("opt", "on it")
	a := &Agent{chat: c, running: map[string]*proc{}, writing: map[string]chan struct{}{}}
	off := len(a.mcpList())
	call := func(fromAgent bool, x toolArgs) string { s, _ := callTool(a, fromAgent, "owe", x); return s }
	if got := call(false, toolArgs{From: 0, Text: "x"}); got != "The owed list is off: opt-chat runs with -owed=false." {
		t.Fatalf("off: %q", got)
	}
	o, err := loadOwed(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	a.owed = o
	if n := len(a.mcpList()); n != off+1 {
		t.Fatalf("tools when on: %d, off: %d", n, off)
	}
	if a.owedNote() != nil {
		t.Fatal("an empty list opened a turn")
	}
	if got := call(false, toolArgs{From: 1, Text: "make it green"}); !strings.Contains(got, "no message of the user's") {
		t.Fatalf("cited Opt's own message: %q", got)
	}
	if got := call(false, toolArgs{From: 9, Text: "x"}); !strings.Contains(got, "no message of the user's") {
		t.Fatalf("cited no message: %q", got)
	}
	if got := call(true, toolArgs{From: 0, Text: "x"}); got != "Only Opt keeps the owed list." {
		t.Fatalf("a subagent's: %q", got)
	}
	if got := call(false, toolArgs{From: 0, Text: " make the build green "}); got != "Owed #1 added (asked in 0)." {
		t.Fatalf("add: %q", got)
	}
	if got := call(false, toolArgs{}); got != "#1 (asked in 0): make the build green" {
		t.Fatalf("list: %q", got)
	}
	if note := a.owedNote(); len(note) != 1 || !strings.Contains(note[0].Text, "owe(close, from)):\n#1 (asked in 0): make the build green") {
		t.Fatalf("turn note: %v", note)
	}
	again, err := loadOwed(c.dir)
	if err != nil || again.text(false) != o.text(false) {
		t.Fatalf("after a restart: %q, %v", again.text(false), err)
	}
	if got, _, _ := command(a, "/done 2"); got != "No owed item #2." {
		t.Fatalf("done 2: %q", got)
	}
	if got, _, _ := command(a, "/done x"); !strings.Contains(got, "takes an item's number") {
		t.Fatalf("done x: %q", got)
	}
	if got, _, _ := command(a, "/done #1"); got != "Closed #1: make the build green" {
		t.Fatalf("done 1: %q", got)
	}
	if got, _, _ := command(a, "/owed"); got != "Nothing owed." {
		t.Fatalf("owed after done: %q", got)
	}
}

// Opt closes an item only citing a user's message after the one that asked
// for it; states tag items, and the turn's list cuts those not Opt's to do.
func TestOwedCloseAndStates(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 100)
	c.Log("user", "audit the tool") // 0
	c.Log("opt", "it's done")       // 1
	c.Log("user", "looks good")     // 2
	a := &Agent{chat: c, running: map[string]*proc{}, writing: map[string]chan struct{}{}}
	o, _ := loadOwed(c.dir)
	a.owed = o
	call := func(x toolArgs) string { s, _ := callTool(a, false, "owe", x); return s }
	call(toolArgs{From: 0, Text: "audit the tool"})
	call(toolArgs{From: 0, Text: strings.Repeat("flash the chip ", 10)})
	for _, from := range []int{0, 1, 9} { // the asking message, Opt's own, none
		if got := call(toolArgs{Close: 1, From: from}); !strings.Contains(got, "Ask the user first") {
			t.Fatalf("closed citing %d: %q", from, got)
		}
	}
	if got := call(toolArgs{Close: 3, From: 2}); got != "No owed item #3." {
		t.Fatalf("close 3: %q", got)
	}
	if got := call(toolArgs{Item: 2, State: "later"}); !strings.Contains(got, "one of mine") {
		t.Fatalf("bad state: %q", got)
	}
	if got := call(toolArgs{Item: 2, State: "parked"}); got != "Owed #2 is parked." {
		t.Fatalf("park: %q", got)
	}
	note := a.owedNote()[0].Text
	if !strings.Contains(note, "#1 (asked in 0): audit the tool\n") || !strings.Contains(note, "#2 (asked in 0, parked): flash the chip") || !strings.Contains(note, "…") || strings.Count(note, "flash the chip") > 7 {
		t.Fatalf("turn note: %q", note)
	}
	if got := call(toolArgs{}); strings.Contains(got, "…") {
		t.Fatalf("owe() list cut: %q", got)
	}
	if got := call(toolArgs{Close: 1, From: 2}); got != "Closed #1 (confirmed in 2): audit the tool" {
		t.Fatalf("close: %q", got)
	}
	again, _ := loadOwed(c.dir)
	if t1 := again.text(false); strings.Contains(t1, "#1 ") || !strings.Contains(t1, "parked") {
		t.Fatalf("after a restart: %q", t1)
	}
	if got := call(toolArgs{Item: 2, State: "mine"}); got != "Owed #2 is mine." || strings.Contains(o.text(true), "…") {
		t.Fatalf("back to mine: %q %q", got, o.text(true))
	}
}
