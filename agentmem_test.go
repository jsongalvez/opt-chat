package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bigAgentLog writes the log of a subagent that ran long: n tool calls,
// each with an echo of size bytes.
func bigAgentLog(t *testing.T, c *Chat, name string, n, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(c.dir, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("task: fix the build\n\non: opus medium\n\n")
	for i := range n {
		fmt.Fprintf(&b, "tool@%d#toolu_%d: Bash {\"command\":\"make test %d\"}\n\n", 1761134400000+i, i, i)
		fmt.Fprintf(&b, "echo@%d#toolu_%d: result %d %s\n\n", 1761134400000+i, i, i, strings.Repeat("x", size))
	}
	b.WriteString("agent@1761134499999: done: the build is green\n\n")
	if err := os.WriteFile(filepath.Join(c.dir, "agents", name+".log"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func zoomTool(a *Agent, id any, n int) string {
	b, _ := json.Marshal(id)
	s, _ := callTool(a, false, "zoom", toolArgs{ID: b, N: n})
	return s
}

// zoom("Name") on a subagent that ran long gives a bounded view of its
// chat, not its whole log, which overflowed the caller's context.
func TestAgentZoomBounded(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 100)
	a := &Agent{chat: c, running: map[string]*proc{}, writing: map[string]chan struct{}{}}
	bigAgentLog(t, c, "Big", 300, 5000) // 1.5 MB
	got := zoomTool(a, "Big", 0)
	if len(got) > 40_000 {
		t.Fatalf("zoom(\"Big\") gave %d bytes", len(got))
	}
}

// A subagent's chat is summarized as the chat is: its view's lines are
// built, zoom("Name:id", n) opens them down to a whole step, a running
// subagent's new steps go in as it writes them, and a chat opened again
// after steps it missed takes each once.
func TestAgentMemTree(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 300)
	a := &Agent{chat: c, running: map[string]*proc{}, writing: map[string]chan struct{}{}}
	bigAgentLog(t, c, "Big", 300, 5000)
	c.setAgentCompact(c.compact)
	sc, err := c.agentMem("Big")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, sc)
	steps := 2 + 2*300 + 1
	view := zoomTool(a, "Big", 0)
	if !strings.Contains(view, fmt.Sprintf("[Big]'s chat, %d messages, as lines", steps)) || strings.Contains(view, unbuilt) {
		t.Fatalf("view: %.300q", view)
	}
	if len(view) > agentViewHi+1000 {
		t.Fatalf("view of %d bytes", len(view))
	}
	if got := zoomTool(a, "Big:2", 1); got != "2+1|tool: Bash {\"command\":\"make test 0\"}" {
		t.Fatalf("a step: %q", got)
	}
	first := strings.SplitN(strings.SplitN(view, "\n", 3)[1], "|", 2)[0] // the view's first line, "0+n"
	id, n := 0, 0
	fmt.Sscanf(first, "%d+%d", &id, &n)
	if n < 2 {
		t.Fatalf("the view's first line %q merges nothing", first)
	}
	if got := zoomTool(a, fmt.Sprintf("Big:%d", id), n); !strings.HasPrefix(got, fmt.Sprintf("0+%d|", n/2)) || strings.Count(got, "\n") != 1 {
		t.Fatalf("zoom(Big:0, %d): %.200q", n, got)
	}

	a.agentWrite("Big", "agent", "one more step")
	if got := zoomTool(a, fmt.Sprintf("Big:%d", steps), 1); got != fmt.Sprintf("%d+1|agent: one more step", steps) {
		t.Fatalf("a running subagent's step: %q", got)
	}

	// As after a crash between its log and its chat: the chat is closed,
	// and a step is logged that it never got.
	waitIdle(t, sc)
	c.mems.mu.Lock()
	sc.lock.Close()
	c.mems.open = nil
	c.mems.mu.Unlock()
	f, _ := os.OpenFile(filepath.Join(c.dir, "agents", "Big.log"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("thinking: hidden\n\nagent: missed\n\n")
	f.Close()
	sc, err = c.agentMem("Big")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, sc)
	sc.mu.Lock()
	got := len(sc.msgs)
	sc.mu.Unlock()
	if got != steps+2 {
		t.Fatalf("%d messages after the reopen, want %d", got, steps+2)
	}
	if got := zoomTool(a, fmt.Sprintf("Big:%d", steps+1), 1); got != fmt.Sprintf("%d+1|agent: missed", steps+1) {
		t.Fatalf("the missed step: %q", got)
	}

	// An entry being written goes in once it ends.
	appendLog := func(s string) {
		f, _ := os.OpenFile(filepath.Join(c.dir, "agents", "Big.log"), os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString(s)
		f.Close()
	}
	half := fmt.Sprintf("Big:%d", steps+2)
	appendLog("agent: half")
	if got := zoomTool(a, half, 1); !strings.HasPrefix(got, "No line") {
		t.Fatalf("an entry half written: %q", got)
	}
	appendLog(" and the rest\n\n")
	if got := zoomTool(a, half, 1); got != fmt.Sprintf("%d+1|agent: half and the rest", steps+2) {
		t.Fatalf("the entry once whole: %q", got)
	}

	c.mems.off = true
	if got := zoomTool(a, "Big", 0); !strings.HasPrefix(got, "task: fix the build\n\non: opus medium\n\ntool: Bash") {
		t.Fatalf("-agent-memory=false: %.100q", got)
	}
}
