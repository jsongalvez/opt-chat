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
// log. A nil *agentMems is -agent-memory=false: steps go to the log only,
// and zoom("Name") gives the whole log.
//
// Locks, always taken in this order: an agentMem's mu, then agentMems.mu,
// then a Chat's mu, then jobGate.mu. closeIdleAgents, which holds
// agentMems.mu, only tries an agentMem's mu, so it never waits against the
// order.
type agentMems struct {
	main    *Chat // its dir, and its hub for the subagents' chats' warnings
	mu      sync.Mutex
	compact compactor // the main chat's; nil while quitting
	open    map[string]*agentMem
	gate    jobGate
}

// newAgentMems sets subagents' memory up beside the main chat, which then
// holds their compactions back while its own wait.
func newAgentMems(main *Chat, jobs int) *agentMems {
	m := &agentMems{main: main, gate: jobGate{max: jobs}}
	m.gate.kick = m.pump
	main.mu.Lock()
	main.role = mainRole{&m.gate}
	main.mu.Unlock()
	return m
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

// chatRole is what sets a chat's compactions apart by whose chat it is:
// the main chat's (mainRole) go first; a subagent's (*subChat) share
// -agent-jobs and name it in their warnings. Called holding the chat's mu.
type chatRole interface {
	take() bool        // whether a compaction may start
	done()             // one taken has ended
	pumped(waits bool) // after a pump: whether lines wait to be built
	warn(main *hub, sound bool, s string)
	whose() string     // what a compaction is told of whose chat it is
	mergedHeads() bool // a merged line not built shows its messages' heads
}

// mainRole is the main chat's: gate, if subagents have memory, waits for
// its lines.
type mainRole struct{ gate *jobGate }

func (mainRole) take() bool { return true }
func (mainRole) done()      {}
func (r mainRole) pumped(waits bool) {
	if r.gate != nil {
		r.gate.setMain(waits)
	}
}
func (mainRole) warn(main *hub, sound bool, s string) {
	main.notice("warn", "%s", s)
	if sound {
		main.sound(soundError)
	}
}
func (mainRole) whose() string     { return "" }
func (mainRole) mergedHeads() bool { return false }

// subChat is a subagent's chat's: its name, the main chat's hub for its
// warnings (its messages are not the page's), and the gate its
// compactions share.
type subChat struct {
	name string
	hub  *hub
	gate *jobGate
}

func (s *subChat) take() bool        { return s.gate.take() }
func (s *subChat) done()             { s.gate.give() }
func (s *subChat) pumped(bool)       {}
func (s *subChat) mergedHeads() bool { return true } // its first zoom often finds none built
func (s *subChat) warn(_ *hub, sound bool, msg string) {
	s.hub.notice("warn", "[%s] memory: %s", s.name, msg)
	if sound {
		s.hub.sound(soundError)
	}
}
func (s *subChat) whose() string {
	return fmt.Sprintf("This is subagent [%s]'s own chat, not the main one. Its kinds: task, its task; "+
		"on, its model; agent, its replies; tool and echo, its own tool calls and their results; "+
		"opt and user, messages Opt or the user sent it; cut, compacted and loop, opt-chat's notes.\n", s.name)
}

// jobGate shares -agent-jobs compactions among all subagents' chats, and
// opens none while the main chat has a line waiting to be built: the main
// chat's come first.
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

// pump gives the subagents' chats a chance at the gate.
func (m *agentMems) pump() {
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

// setCompact sets the compactor of every subagent's chat, open or to open,
// and sets them building: at the start, at a quit (nil) and around a
// reload.
func (m *agentMems) setCompact(f compactor) {
	if m == nil {
		return
	}
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

// lock is the subagent name's entry, made if need be, its mu held.
func (m *agentMems) lock(name string) *agentMem {
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

// appendStep writes b, a step, to the log of the subagent name at path
// and feeds it to its chat, holding the subagent's lock over both.
func (m *agentMems) appendStep(name, path string, b []byte) error {
	if m == nil {
		return appendRaw(path, b)
	}
	am := m.lock(name)
	err := appendRaw(path, b)
	if err == nil {
		if _, ferr := m.fed(name, am); ferr != nil {
			warn("[%s] memory: %v", name, ferr)
		}
	}
	am.mu.Unlock()
	m.closeIdle(name)
	return err
}

// chat is the chat of the subagent name, opened if need be, with every
// step of its log in it.
func (m *agentMems) chat(name string) (*Chat, error) {
	am := m.lock(name)
	sc, err := m.fed(name, am)
	am.mu.Unlock()
	m.closeIdle(name)
	return sc, err
}

// fed opens the subagent's chat if need be and feeds it what its log
// gained since. am.mu held. Each step goes in once: a chat opened again
// skips the steps it holds, counted as its messages' first parts, so a
// crash between the log and the chat loses none and doubles none.
func (m *agentMems) fed(name string, am *agentMem) (*Chat, error) {
	am.used.Store(time.Now().UnixNano())
	if am.c != nil {
		return am.c, m.feed(name, am, am.c)
	}
	dir := filepath.Join(m.main.dir, "agents")
	if _, err := os.Stat(filepath.Join(dir, name+".log")); err != nil {
		return nil, err
	}
	sc, err := openChat(filepath.Join(dir, name+".mem"))
	if err != nil {
		return nil, err
	}
	sc.role = &subChat{name: name, hub: m.main.hub, gate: &m.gate}
	sc.view.lo, sc.view.hi = agentViewLo, agentViewHi
	sc.mu.Lock()
	sc.view.check(sc)
	sc.mu.Unlock()
	if err := m.feed(name, am, sc); err != nil { // before its first pump: the context it builds on is whole
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

// feed logs in sc the steps written to the subagent's log since it was
// last fed: whole entries only, each ending in a blank line (one cut short
// by a crash goes in once the next ends it). am.mu held, so no step is
// being written.
func (m *agentMems) feed(name string, am *agentMem, sc *Chat) error {
	f, err := os.Open(filepath.Join(m.main.dir, "agents", name+".log"))
	if err != nil {
		return err
	}
	defer f.Close()
	if am.read > 0 { // a log's first read needs no seek, so it may be a pipe, as in a test
		if _, err := f.Seek(am.read, io.SeekStart); err != nil {
			return err
		}
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

// closeIdle closes, past agentOpenMax open, the least used chats with
// nothing to build, but keep's. A closed one is opened again from its
// files when next needed. It holds agentMems.mu over closing their files.
func (m *agentMems) closeIdle(keep string) {
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
		if !e.am.mu.TryLock() { // being fed: in use. Only tried: see the lock order on agentMems
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

// agentPlain is a subagent's whole chat in the chat dir, its steps' times
// and ids left out, and its thinking: what zoom("Name") gives with
// -agent-memory=false.
func agentPlain(dir, name string) string {
	name = agentName(name)
	b, err := os.ReadFile(filepath.Join(dir, "agents", name+".log"))
	if name == "" || err != nil {
		return fmt.Sprintf("No subagent [%s].", name)
	}
	return string(plainSteps(b))
}

// logStep writes b, a step, to the log of the subagent name, and to its
// memory.
func (a *Agent) logStep(name string, b []byte) error {
	return a.mems.appendStep(name, a.agentLog(name), b)
}

// ZoomAgent gives a subagent's chat as its view: lines that zoom opens as
// zoom("Name:id", n), within agentViewHi however long it ran.
func (a *Agent) ZoomAgent(name string) string {
	if a.mems == nil {
		return agentPlain(a.chat.dir, name)
	}
	name = agentName(name)
	if name == "" {
		return "No subagent []."
	}
	sc, err := a.mems.chat(name)
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
func (a *Agent) ZoomAgentLine(name string, id, n int) (string, []string) {
	if a.mems == nil {
		return "Subagents' chats have no lines: opt-chat runs with -agent-memory=false; zoom(\"Name\") gives the whole log.", nil
	}
	name = agentName(name)
	if name == "" {
		return "No subagent [].", nil
	}
	sc, err := a.mems.chat(name)
	if err != nil {
		return fmt.Sprintf("No subagent [%s].", name), nil
	}
	text, _ := sc.Zoom(id, max(n, 1)) // a step has no images
	return text, nil
}
