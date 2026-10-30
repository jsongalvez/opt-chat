package main

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	// Compactions running at once in all subagents' chats together, unless
	// -agent-jobs says otherwise.
	defaultAgentJobs = 2
	// Subagents' chats kept open at most: past it, the least used idle ones
	// are closed, and opened again from their files when next needed.
	agentOpenMax = 32
)

// agentMems are the subagents' chats opened so far: a running subagent's
// is fed as it writes; a finished one's, at its first zoom, from its whole
// log. mu guards the map and compact, and is taken before any agentMem's
// chat's Chat.mu, never while holding one, and never over I/O.
type agentMems struct {
	mu      sync.Mutex
	off     bool      // -agent-memory=false: zoom("Name") gives the whole log
	compact compactor // the main chat's; nil while quitting
	open    map[string]*agentMem
	gate    jobGate
}

// agentMem is one subagent's chat. Its mu is held over a step's write to
// the log and its feed into the chat, and over a zoom's feed, so a feed
// never reads a step half written.
type agentMem struct {
	mu   sync.Mutex
	c    *Chat        // set holding both mu and agentMems.mu: read holding either
	read int64        // bytes of the log fed into c
	used atomic.Int64 // when last fed, in Unix nanoseconds: read without mu
	gone bool         // closed and out of the map: take the name's entry again
}

// jobGate shares -agent-jobs compactions among all subagents' chats, and
// opens none while the main chat has a line waiting to be built: the main
// chat's come first. Its mu is taken last, under a Chat.mu.
type jobGate struct {
	mu        sync.Mutex
	max, busy int
	mainWaits bool
	kick      func() // pumps every subagent's chat, without locks held
}

func (g *jobGate) take() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.mainWaits || g.busy >= g.max {
		return false
	}
	g.busy++
	return true
}

func (g *jobGate) give() {
	g.mu.Lock()
	g.busy--
	g.mu.Unlock()
	go g.kick()
}

// setMain is whether the main chat has lines waiting, from its pump.
func (g *jobGate) setMain(waits bool) {
	g.mu.Lock()
	was := g.mainWaits
	g.mainWaits = waits
	g.mu.Unlock()
	if was && !waits {
		go g.kick()
	}
}

// pumpAgents gives the subagents' chats a chance at the gate.
func (c *Chat) pumpAgents() {
	m := c.mems
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, am := range m.open {
		if am.c != nil {
			am.c.mu.Lock()
			am.c.pump()
			am.c.mu.Unlock()
		}
	}
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
		if am.c != nil {
			am.c.mu.Lock()
			am.c.compact = f
			am.c.pump()
			am.c.mu.Unlock()
		}
	}
}

// lockAgent is the subagent name's entry, made if need be, its mu held.
func (c *Chat) lockAgent(name string) *agentMem {
	m := c.mems
	for {
		m.mu.Lock()
		am := m.open[name]
		if am == nil {
			am = &agentMem{}
			if m.open == nil {
				m.open = map[string]*agentMem{}
			}
			m.open[name] = am
		}
		m.mu.Unlock()
		am.mu.Lock()
		if !am.gone {
			return am
		}
		am.mu.Unlock()
	}
}

// agentAppend writes b, a step, to the log of the subagent name and feeds
// it to its chat, holding the subagent's lock over both.
func (c *Chat) agentAppend(name, path string, b []byte) error {
	if c.mems.off {
		return appendRaw(path, b)
	}
	am := c.lockAgent(name)
	err := appendRaw(path, b)
	if err == nil {
		if _, ferr := c.agentFed(name, am); ferr != nil {
			warn("[%s] memory: %v", name, ferr)
		}
	}
	am.mu.Unlock()
	c.closeIdleAgents(name)
	return err
}

// agentMem is the chat of the subagent name, opened if need be, with every
// step of its log in it.
func (c *Chat) agentMem(name string) (*Chat, error) {
	am := c.lockAgent(name)
	sc, err := c.agentFed(name, am)
	am.mu.Unlock()
	c.closeIdleAgents(name)
	return sc, err
}

// agentFed opens the subagent's chat if need be and feeds it what its log
// gained since. am.mu held. Each step goes in once: a chat opened again
// skips the steps it holds, counted as its messages' first parts, so a
// crash between the log and the chat loses none and doubles none.
func (c *Chat) agentFed(name string, am *agentMem) (*Chat, error) {
	m := c.mems
	am.used.Store(time.Now().UnixNano())
	if am.c == nil {
		if _, err := os.Stat(filepath.Join(c.dir, "agents", name+".log")); err != nil {
			return nil, err
		}
		sc, err := openChat(filepath.Join(c.dir, "agents", name+".mem"))
		if err != nil {
			return nil, err
		}
		sc.who, sc.alerts, sc.gate = name, c.hub, &m.gate
		sc.view.lo, sc.view.hi = agentViewLo, agentViewHi
		sc.mu.Lock()
		sc.view.check(sc)
		sc.mu.Unlock()
		if err := c.feedAgentMem(name, am, sc); err != nil { // before its first pump: the context it builds on is whole
			sc.lock.Close()
			return nil, err
		}
		m.mu.Lock()
		sc.mu.Lock()
		sc.compact = m.compact
		sc.pump()
		sc.mu.Unlock()
		am.c = sc
		m.mu.Unlock()
		return sc, nil
	}
	return am.c, c.feedAgentMem(name, am, am.c)
}

// feedAgentMem logs in sc the steps written to the subagent's log since it
// was last fed: whole entries only, each ending in a blank line (one cut
// short by a crash goes in once the next ends it). am.mu held, so no step
// is being written.
func (c *Chat) feedAgentMem(name string, am *agentMem, sc *Chat) error {
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
	if j := bytes.LastIndex(b, []byte("\n\n")); j < 0 {
		b = nil
	} else {
		b = b[:j+2]
	}
	steps := lo.Filter(parseSteps(b), func(s step, _ int) bool { return s.Kind != "thinking" })
	if am.read == 0 { // opened: skip what the chat holds
		sc.mu.Lock()
		held := lo.CountBy(sc.msgs, func(m Msg) bool { return m.Part == 0 })
		sc.mu.Unlock()
		steps = steps[min(held, len(steps)):]
	}
	am.read += int64(len(b))
	if len(steps) == 0 {
		return nil
	}
	sc.logMsgs(lo.Map(steps, func(s step, _ int) Msg {
		m := Msg{Kind: s.Kind, Text: s.Text, Ref: s.Ref}
		if s.At > 0 {
			m.Date = time.UnixMilli(s.At)
		}
		return m
	}))
	return nil
}

// closeIdleAgents closes, past agentOpenMax open, the least used chats with
// nothing to build, but keep's. A closed one is opened again from its
// files when next needed.
func (c *Chat) closeIdleAgents(keep string) {
	m := c.mems
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.open) <= agentOpenMax {
		return
	}
	type entry struct {
		name string
		am   *agentMem
	}
	var es []entry
	for name, am := range m.open {
		if name != keep {
			es = append(es, entry{name, am})
		}
	}
	slices.SortFunc(es, func(a, b entry) int { return cmp.Compare(a.am.used.Load(), b.am.used.Load()) })
	for _, e := range es {
		if len(m.open) <= agentOpenMax {
			return
		}
		if !e.am.mu.TryLock() { // being fed: in use
			continue
		}
		idle := true
		if sc := e.am.c; sc != nil {
			sc.mu.Lock()
			if idle = len(sc.busy) == 0 && len(sc.ready0) == 0 && len(sc.readyM) == 0; idle {
				sc.compact = nil
				sc.lock.Close()
			}
			sc.mu.Unlock()
		}
		if idle {
			e.am.c, e.am.gone = nil, true
			delete(m.open, e.name)
		}
		e.am.mu.Unlock()
	}
}

// agentPlain is a subagent's whole chat, its steps' times and ids left out,
// and its thinking: what zoom("Name") gives with -agent-memory=false.
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
	if name == "" {
		return "No subagent []."
	}
	sc, err := c.agentMem(name)
	if err != nil {
		return fmt.Sprintf("No subagent [%s].", name)
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	head := fmt.Sprintf("[%s]'s chat, %s, as lines: zoom(\"%s:id\", n) opens line id+n into two; zoom(\"%s:id\", 1) gives message id whole.",
		name, count(len(sc.msgs), "message"), name, name)
	if n := len(sc.ready0); n > 0 {
		head += fmt.Sprintf(" %s not summarized yet: their lines show the heads of their first and last messages.", count(n, "message"))
	}
	return head + "\n" + strings.Join(sc.callView(), "\n")
}

// ZoomAgentLine is zoom(id, n) in a subagent's chat.
func (c *Chat) ZoomAgentLine(name string, id, n int) (string, []string) {
	name = agentName(name)
	if c.mems.off {
		return "Subagents' chats have no lines: opt-chat runs with -agent-memory=false; zoom(\"Name\") gives the whole log.", nil
	}
	if name == "" {
		return "No subagent [].", nil
	}
	sc, err := c.agentMem(name)
	if err != nil {
		return fmt.Sprintf("No subagent [%s].", name), nil
	}
	text, _ := sc.Zoom(id, max(n, 1)) // a step has no images
	return text, nil
}
