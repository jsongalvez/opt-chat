package main

import (
	"context"
	"strings"
	"testing"
)

// Every result's spend since the last one is kept in usage/, a resumed
// session's counted from where its last run left off; /usage shows the
// plan from get_usage, then the calls' spend by model and by caller.
func TestUsage(t *testing.T) {
	t.Parallel()
	c, _ := fakeChat(t, t.TempDir(), 100)
	a, _ := fakeAgent(t, c)
	if err := (&Turn{agent: a}).Run(context.Background(), []message{{kind: "user", text: "hello"}}); err != nil {
		t.Fatal(err)
	}
	all, err := readSpend(c.dir, started)
	if err != nil {
		t.Fatal(err)
	}
	var cost float64
	for _, s := range all {
		if s.Role != "turn" || s.Requests != 1 || s.Models["claude-big"].Output != 10 {
			t.Fatalf("%+v", s)
		}
		cost += s.Cost
	}
	if len(all) == 0 || cost != 0.25*float64(len(all)) {
		t.Fatalf("%d results, $%v", len(all), cost)
	}
	p, err := a.start(a.life, "big", "", roleAgent, "--resume", "s1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.close(); p.wait() })
	if p.spent.Cost != 0.5 || p.spent.Models["claude-big"].Output != 500 {
		t.Fatalf("resumed from %+v", p.spent)
	}

	out, ok, err := command(a, "/usage")
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	for _, want := range []string{
		"Your plan (max):\n",
		"  Current session             ██░░░░░░░░░░░░░░░░░░   9% used, resets ",
		"  Current week (Fable)        █████░░░░░░░░░░░░░░░  27% used",
		"  Usage credits               $1.25 of $50.00 spent\n",
		"  The week by where it was used: Claude Code 96%, Chats 1%\n",
		"All of Claude Code on this machine, last 24h (2,947 requests, 23 sessions):\n",
		"  Usage came from: long context 67%\n",
		"  MCP servers:    plugin:playwright:playwright 11%\n",
		"opt-chat's calls today: $",
		"% of input read from the cache\n",
		"  claude-big:          ",
		"  The chat's turns:    $",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}
	if _, _, err := command(a, "/usage month"); err == nil {
		t.Fatal("/usage month taken")
	}
	if out, _, _ := command(a, "/usage week"); strings.Contains(out, "All of Claude Code") || !strings.Contains(out, "opt-chat's calls in the last 7 days: $") {
		t.Fatal(out)
	}
}

// Each record names what the cache reuse hangs on (prefix, first message)
// and how long since the same pair last had a result, so a report can tell
// an expired entry from a changed prompt or view.
func TestUsageMissCause(t *testing.T) {
	t.Parallel()
	c, _ := fakeChat(t, t.TempDir(), 100)
	a, _ := fakeAgent(t, c)
	result := func(cost float64) event {
		return event{Turns: 1, Cost: cost, Models: map[string]modelSpend{"claude-big": {Write: int(cost * 1000), Cost: cost}}}
	}
	call := func(first string) *proc {
		p := &proc{prefix: "big medium " + t.Name(), stdin: nopWriter{}}
		p.send(block{Type: "text", Text: first})
		return p
	}
	p := call("view A")
	a.noteSpend(p, roleTurn, result(0.1))
	a.noteSpend(call("view A"), roleTurn, result(0.2)) // same pair, a new call
	a.noteSpend(call("view B"), roleTurn, result(0.3)) // view changed
	all, err := readSpend(c.dir, started)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("%d records", len(all))
	}
	if all[0].Prefix == "" || all[0].First == "" || all[0].GapS != 0 {
		t.Fatalf("first call: %+v", all[0])
	}
	if all[1].Prefix != all[0].Prefix || all[1].First != all[0].First || all[1].GapS < 1 {
		t.Fatalf("same pair again: %+v", all[1])
	}
	if all[2].First == all[0].First || all[2].GapS != 0 {
		t.Fatalf("changed view: %+v", all[2])
	}
}

type nopWriter struct{}

func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nopWriter) Close() error                { return nil }
