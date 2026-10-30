package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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

// memAgent is an Agent on c with subagents' memory on, as by default.
func memAgent(c *Chat) *Agent {
	return &Agent{chat: c, running: map[string]*proc{}, writing: map[string]chan struct{}{}, mems: newAgentMems(c, defaultAgentJobs)}
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
	a := memAgent(c)
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
	a := memAgent(c)
	bigAgentLog(t, c, "Big", 300, 5000)
	a.mems.setCompact(c.compact)
	sc, err := a.mems.chat("Big")
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
	a.mems.mu.Lock()
	sc.lock.Close()
	a.mems.open = nil
	a.mems.mu.Unlock()
	f, _ := os.OpenFile(filepath.Join(c.dir, "agents", "Big.log"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("thinking: hidden\n\nagent: missed\n\n")
	f.Close()
	sc, err = a.mems.chat("Big")
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
}

// With -agent-memory=false, zoom("Name") gives the whole log as before,
// steps go to the log only, and the prompt and the zoom tool say so.
func TestAgentMemOff(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 100)
	a := &Agent{chat: c, running: map[string]*proc{}, writing: map[string]chan struct{}{}}
	bigAgentLog(t, c, "Big", 3, 10)
	a.agentWrite("Big", "agent", "one more step")
	got := zoomTool(a, "Big", 0)
	if want := agentPlain(c.dir, "Big"); got != want || !strings.HasPrefix(got, "task: fix the build\n\non: opus medium\n\ntool: Bash") || !strings.Contains(got, "agent: one more step") {
		t.Fatalf("zoom(\"Big\"): %.200q", got)
	}
	if got := zoomTool(a, "Big:0", 1); !strings.Contains(got, "-agent-memory=false") {
		t.Fatalf("zoom(\"Big:0\", 1): %q", got)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "agents", "Big.mem")); !os.IsNotExist(err) {
		t.Fatalf("a chat made for Big: %v", err)
	}
	for _, mem := range []bool{true, false} {
		a.mems = nil
		if mem {
			a.mems = newAgentMems(c, 1)
		}
		var zoom string
		for _, tl := range a.mcpList() {
			if tl["name"] == "zoom" {
				zoom = tl["description"].(string)
			}
		}
		prompt := systemPrompt("", false, false, mem)
		if lines := strings.Contains(zoom, `zoom("Name:id", n)`); lines != mem || strings.Contains(prompt, `zoom("Name:id", n)`) != mem {
			t.Errorf("memory %v: the zoom tool says %q; the prompt offers lines: %v", mem, zoom, !lines)
		}
	}
}

// A zoom while a subagent writes a step with blank lines in it must not cut
// the step at one of them: every step goes in whole.
func TestAgentMemTornWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 20 MB with an fsync per step")
	}
	c, _ := fakeChat(t, t.TempDir(), 100)
	a := memAgent(c)
	os.MkdirAll(filepath.Join(c.dir, "agents"), 0o700)
	a.agentWrite("W", "task", "write steps")
	echo := strings.Repeat("a line of output\n\n", 40_000) // ~720 KB, one write
	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			a.ZoomAgent("W")
		}
	}()
	const n = 30
	for i := range n {
		a.agentWrite("W", "echo", fmt.Sprintf("%d\n\n%s", i, echo))
	}
	stop.Store(true)
	wg.Wait()
	sc, err := a.mems.chat("W")
	if err != nil {
		t.Fatal(err)
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	var got strings.Builder
	steps := 0
	for _, m := range sc.msgs {
		if m.Kind == "echo" && m.Part == 0 {
			steps++
		}
		if m.Kind == "echo" {
			got.WriteString(m.Text)
		}
	}
	if want := n * len(echo); steps != n || got.Len() < want {
		t.Fatalf("%d echo steps, %d bytes of them; want %d steps, at least %d bytes", steps, got.Len(), n, want)
	}
}

// The first zoom of a subagent from before, nothing summarized yet, shows
// what it was asked and what it ended with, not bare placeholders.
func TestAgentZoomFirstViewHeads(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 100)
	a := memAgent(c)
	bigAgentLog(t, c, "Big", 300, 5000)
	view := zoomTool(a, "Big", 0)
	for _, want := range []string{"task: fix the build", "agent: done: the build is green"} {
		if !strings.Contains(view, want) {
			t.Errorf("first view lacks %q: %.400q", want, view)
		}
	}
	if len(view) > agentViewHi+1000 {
		t.Errorf("view of %d bytes", len(view))
	}
}

// Subagents' compactions share one pool of -agent-jobs, and none starts
// while the main chat has a line waiting to be built.
func TestAgentMemSharesQueue(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 100)
	a := memAgent(c)
	mainFake := c.compact
	c.mu.Lock()
	c.compact = func(ctx context.Context, l []string, s int, b []block) (string, error) {
		time.Sleep(2 * time.Millisecond)
		return mainFake(ctx, l, s, b)
	}
	c.mu.Unlock()
	var now, most atomic.Int64
	var early atomic.Int64
	sub := func(ctx context.Context, l []string, s int, b []block) (string, error) {
		if v := now.Add(1); v > most.Load() {
			most.Store(v)
		}
		defer now.Add(-1)
		c.mu.Lock()
		if _, ok := c.next(); ok {
			early.Add(1)
		}
		c.mu.Unlock()
		time.Sleep(time.Millisecond)
		return strings.Repeat("s", 100), nil
	}
	a.mems.setCompact(sub)
	var ms []Msg
	for i := range 400 {
		ms = append(ms, Msg{Kind: "user", Text: fmt.Sprintf("message %d %s", i, strings.Repeat("y", 800))})
	}
	c.logMsgs(ms)
	for _, name := range []string{"A", "B", "C"} {
		bigAgentLog(t, c, name, 40, 2000)
		zoomTool(a, name, 0)
	}
	waitIdle(t, c)
	for _, name := range []string{"A", "B", "C"} {
		sc, _ := a.mems.chat(name)
		waitIdle(t, sc)
	}
	if most.Load() > int64(a.mems.gate.max) {
		t.Errorf("%d subagent compactions at once, want at most %d in all", most.Load(), a.mems.gate.max)
	}
	if early.Load() > 0 {
		t.Errorf("%d subagent compactions started while the main chat had lines waiting", early.Load())
	}
}

// A first zoom of one subagent, however long its log takes to read, holds
// up no other subagent's steps. Old's log is a pipe: its zoom stays in the
// read, holding whatever it holds, until the test writes the log and closes
// it.
func TestAgentMemNoGlobalStall(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 100)
	a := memAgent(c)
	os.MkdirAll(filepath.Join(c.dir, "agents"), 0o700)
	a.agentWrite("Run", "task", "work")
	pipe := filepath.Join(c.dir, "agents", "Old.log")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	zoomed := make(chan string)
	go func() { zoomed <- a.ZoomAgent("Old") }()
	w, err := os.OpenFile(pipe, os.O_WRONLY, 0) // returns once the zoom opened it to read
	if err != nil {
		t.Fatal(err)
	}
	stepped := make(chan struct{})
	go func() {
		a.agentWrite("Run", "agent", "a step")
		close(stepped)
	}()
	select {
	case <-stepped:
	case got := <-zoomed:
		t.Fatalf("the zoom ended before its log did: %q", got)
	case <-time.After(time.Minute): // a deadlock, not a slow machine
		w.Close()
		t.Fatal("a step of another subagent waited on Old's first zoom")
	}
	w.WriteString("task: old work\n\nagent: done\n\n")
	w.Close()
	if got := <-zoomed; !strings.Contains(got, "[Old]'s chat, 2 messages") {
		t.Fatalf("zoom(\"Old\"): %q", got)
	}
	if got := zoomTool(a, "Run:1", 1); got != "1+1|agent: a step" {
		t.Fatalf("Run's step: %q", got)
	}
}

// A subagent chat's failed compaction says so on the page, naming the
// subagent.
func TestAgentMemFailureNoticed(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 100)
	c.hub = newHub()
	var mu sync.Mutex
	var notes []string
	c.hub.print = func(v any) {
		if n, ok := v.(noticeUpdate); ok {
			mu.Lock()
			notes = append(notes, n.S)
			mu.Unlock()
		}
	}
	a := memAgent(c)
	a.mems.setCompact(func(context.Context, []string, int, []block) (string, error) {
		return "", fmt.Errorf("model down")
	})
	bigAgentLog(t, c, "Bad", 5, 2000)
	zoomTool(a, "Bad", 0)
	defer a.mems.setCompact(nil)
	waitFor(t, "a notice naming [Bad]", func() bool {
		mu.Lock()
		defer mu.Unlock()
		got := strings.Join(notes, "\n")
		return strings.Contains(got, "[Bad]") && strings.Contains(got, "model down")
	})
}

// Subagents' chats don't stay open for the life of the process: past
// agentOpenMax, idle ones close, and one closed opens again whole.
func TestAgentMemCloses(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 100)
	a := memAgent(c)
	a.mems.setCompact(c.compact)
	const n = agentOpenMax + 8
	for i := range n {
		name := fmt.Sprintf("S%d", i)
		bigAgentLog(t, c, name, 3, 100)
		zoomTool(a, name, 0)
		sc, _ := a.mems.chat(name)
		waitIdle(t, sc)
	}
	a.mems.mu.Lock()
	open := len(a.mems.open)
	a.mems.mu.Unlock()
	if open > agentOpenMax {
		t.Fatalf("%d subagents' chats open, want at most %d", open, agentOpenMax)
	}
	a.agentWrite("S0", "agent", "after the close")
	if got := zoomTool(a, "S0:9", 1); got != "9+1|agent: after the close" {
		t.Fatalf("a closed chat opened again: %q", got)
	}
	sc, _ := a.mems.chat("S0")
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if len(sc.msgs) != 10 {
		t.Fatalf("%d messages after the reopen, want 10", len(sc.msgs))
	}
}

// A main chat line that keeps failing holds up no subagent's memory.
func TestAgentMemMainFailing(t *testing.T) {
	c, _ := fakeChat(t, t.TempDir(), 100)
	a := memAgent(c)
	mainFake := c.compact
	var poisoned atomic.Bool
	poisoned.Store(true)
	c.mu.Lock()
	c.compact = func(ctx context.Context, l []string, s int, b []block) (string, error) {
		if poisoned.Load() && strings.Contains(b[0].Text, "poison") {
			return "", fmt.Errorf("refused")
		}
		return mainFake(ctx, l, s, b)
	}
	c.mu.Unlock()
	a.mems.setCompact(func(context.Context, []string, int, []block) (string, error) {
		return strings.Repeat("s", 100), nil
	})
	c.logMsgs([]Msg{{Kind: "user", Text: "poison " + strings.Repeat("p", 800)}})
	waitFor(t, "the main chat's line to fail and wait to be tried again", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.failing) > 0 && len(c.busy) == 0
	})
	bigAgentLog(t, c, "Sub", 5, 2000)
	sc, err := a.mems.chat("Sub")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, sc)
	poisoned.Store(false)
	c.logMsgs([]Msg{{Kind: "user", Text: "back"}}) // the user is back: it is tried again now
}
