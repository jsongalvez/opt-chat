package main

import (
	"context"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// Each record names what the cache reuse hangs on (prefix, first message),
// so a report can tell an expired entry from a changed prompt or view.
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
	a.noteSpend(call("view A"), roleTurn, result(0.1))
	a.noteSpend(call("view A"), roleTurn, result(0.2)) // same pair, a new call
	a.noteSpend(call("view B"), roleTurn, result(0.3)) // view changed
	all, err := readSpend(c.dir, started)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("%d records", len(all))
	}
	if all[0].Prefix == "" || all[0].First == "" {
		t.Fatalf("first call: %+v", all[0])
	}
	if all[1].Prefix != all[0].Prefix || all[1].First != all[0].First {
		t.Fatalf("same pair again: %+v", all[1])
	}
	if all[2].Prefix != all[0].Prefix || all[2].First == all[0].First {
		t.Fatalf("changed view: %+v", all[2])
	}
}

// tools/perf/usage_report.py names each big cache write's cause from the
// records, across restarts, by the life of the cache that was written.
func TestUsageReportCauses(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	var b strings.Builder
	rec := func(min int, prefix, first string, m5, h1 int) {
		s := spend{Date: at.Add(time.Duration(min) * time.Minute), Role: "turn", Requests: 1, Prefix: prefix, First: first,
			Models: map[string]modelSpend{"claude-big": {Write: m5 + h1, Write5m: m5, Write1h: h1, Read: 1000}}}
		j, _ := json.Marshal(s)
		b.Write(append(j, '\n'))
	}
	rec(0, "p1", "v1", 0, 50000)  // new prompt
	rec(10, "p1", "v1", 0, 50000) // within the hour: unexplained
	rec(80, "p1", "v1", 0, 50000) // past it: expired
	rec(81, "p1", "v2", 0, 50000) // new view
	rec(90, "p2", "v2", 50000, 0) // new prompt
	rec(97, "p2", "v2", 50000, 0) // past 5 minutes: expired
	rec(99, "p2", "v2", 50000, 0) // within 5 minutes: unexplained
	if err := os.MkdirAll(filepath.Join(dir, "usage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage", "2026-11-01.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("tools/perf/usage_report.py") // read here, so go test reruns when it changes
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("python3", "-I", "-c", string(script), dir).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"new system prompt, tools, model or effort: 2,",
		"same pair, past the cache's life (expired): 2,",
		"same pair within the cache's life (unexplained): 2,",
		"new first message (view changed): 1,",
		"read/write 0.02, writes 1h 57% 5m 43%",
	} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}
}

type nopWriter struct{}

func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nopWriter) Close() error                { return nil }
