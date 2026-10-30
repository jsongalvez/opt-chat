package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/samber/lo"
)

// A subagent's chat is memory too: a Chat of its own in agents/<Name>.mem,
// fed from its log, with the main chat's tree, compactions and zoom. A long
// subagent's log runs to megabytes; its view stays within agentViewHi, and
// zoom opens it down to whole steps, so a fresh agent or Opt learns what it
// did without reading the whole log. (A summary tree per session, subagents
// included, as https://github.com/ahlner/opencode-optchat keeps.)
const (
	agentViewLo = 16_000 // a subagent's view drops back to this...
	agentViewHi = 32_000 // ...once it passes this: one zoom
	// Compactions running at once in each subagent's chat, unless
	// -agent-jobs says otherwise: the main chat's come first.
	defaultAgentJobs = 2
)

// agentMems are the subagents' chats opened so far, kept open: a running
// subagent's is fed as it writes; a finished one's, at its first zoom, from
// its whole log. Guarded by mu, taken before any of their Chat.mu, and
// never while holding one.
type agentMems struct {
	mu      sync.Mutex
	off     bool // -agent-memory=false: zoom("Name") gives the whole log
	jobs    int
	compact compactor // the main chat's; nil while quitting
	open    map[string]*agentMem
}

type agentMem struct {
	c    *Chat
	read int64 // bytes of the log fed into c
}

// setAgentCompact sets the compactor of every subagent's chat, open or to
// open, and sets them building: at the start, at a quit (nil) and around a
// reload.
func (c *Chat) setAgentCompact(f compactor) {
	m := c.mems
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compact = f
	for _, am := range m.open {
		am.c.mu.Lock()
		am.c.compact = f
		am.c.pump()
		am.c.mu.Unlock()
	}
}

// agentMem is the chat of the subagent name, opened if need be, with every
// step of its log in it. Each step goes in once: a chat opened again skips
// the steps it holds, counted as its messages' first parts, so a crash
// between the log and the chat loses none and doubles none.
func (c *Chat) agentMem(name string) (*Chat, error) {
	m := c.mems
	m.mu.Lock()
	defer m.mu.Unlock()
	am := m.open[name]
	if am == nil {
		if _, err := os.Stat(filepath.Join(c.dir, "agents", name+".log")); err != nil {
			return nil, err
		}
		sc, err := openChat(filepath.Join(c.dir, "agents", name+".mem"))
		if err != nil {
			return nil, err
		}
		sc.mu.Lock()
		sc.view.lo, sc.view.hi = agentViewLo, agentViewHi
		sc.view.check(sc)
		sc.jobs, sc.compact = m.jobs, m.compact
		sc.pump() // what a last run left unbuilt
		sc.mu.Unlock()
		am = &agentMem{c: sc}
		if m.open == nil {
			m.open = map[string]*agentMem{}
		}
		m.open[name] = am
	}
	return am.c, c.feedAgentMem(name, am)
}

// feedAgentMem logs in the subagent's chat the steps written to its log
// since it was last fed: whole entries only, each ending in a blank line.
// m.mu held.
func (c *Chat) feedAgentMem(name string, am *agentMem) error {
	f, err := os.Open(filepath.Join(c.dir, "agents", name+".log"))
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(am.read, io.SeekStart); err != nil {
		return err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	// Whole entries only: one being written goes in at the next feed.
	if j := bytes.LastIndex(b, []byte("\n\n")); j < 0 {
		b = nil
	} else {
		b = b[:j+2]
	}
	steps := lo.Filter(parseSteps(b), func(s step, _ int) bool { return s.Kind != "thinking" })
	if am.read == 0 { // opened: skip what the chat holds
		am.c.mu.Lock()
		held := lo.CountBy(am.c.msgs, func(m Msg) bool { return m.Part == 0 })
		am.c.mu.Unlock()
		steps = steps[min(held, len(steps)):]
	}
	am.read += int64(len(b))
	if len(steps) == 0 {
		return nil
	}
	am.c.logMsgs(lo.Map(steps, func(s step, _ int) Msg {
		m := Msg{Kind: s.Kind, Text: s.Text, Ref: s.Ref}
		if s.At > 0 {
			m.Date = time.UnixMilli(s.At)
		}
		return m
	}))
	return nil
}

// agentStepped feeds a running subagent's chat the step it just logged, so
// its lines are built as it works.
func (c *Chat) agentStepped(name string) {
	if c.mems.off {
		return
	}
	if _, err := c.agentMem(name); err != nil {
		warn("[%s] memory: %v", name, err)
	}
}

// agentPlain is a subagent's whole chat, its steps' times and ids left out,
// and its thinking.
func (c *Chat) agentPlain(name string) string {
	name = agentName(name)
	b, err := os.ReadFile(filepath.Join(c.dir, "agents", name+".log"))
	if name == "" || err != nil {
		return fmt.Sprintf("No subagent [%s].", name)
	}
	return string(plainSteps(b))
}

// ZoomAgent gives a subagent's chat as its view: lines that zoom opens as
// zoom("Name:id", n), within agentViewHi however long it ran.
func (c *Chat) ZoomAgent(name string) string {
	name = agentName(name)
	if c.mems.off {
		return c.agentPlain(name)
	}
	sc, err := c.agentMem(name)
	if name == "" || err != nil {
		return fmt.Sprintf("No subagent [%s].", name)
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	head := fmt.Sprintf("[%s]'s chat, %s, as lines: zoom(\"%s:id\", n) opens line id+n into two; zoom(\"%s:id\", 1) gives message id whole.",
		name, count(len(sc.msgs), "message"), name, name)
	if n := len(sc.ready0); n > 0 {
		head += fmt.Sprintf(" %s not summarized yet show their heads.", count(n, "message"))
	}
	return head + "\n" + strings.Join(sc.callView(), "\n")
}

// ZoomAgentLine is zoom(id, n) in a subagent's chat.
func (c *Chat) ZoomAgentLine(name string, id, n int) (string, []string) {
	name = agentName(name)
	if c.mems.off {
		return "Subagents' chats have no lines: opt-chat runs with -agent-memory=false; zoom(\"Name\") gives the whole log.", nil
	}
	sc, err := c.agentMem(name)
	if name == "" || err != nil {
		return fmt.Sprintf("No subagent [%s].", name), nil
	}
	text, _ := sc.Zoom(id, max(n, 1)) // a step has no images
	return text, nil
}
