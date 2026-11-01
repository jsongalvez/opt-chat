package main

import (
	"bufio"
	"encoding/json/v2"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/samber/lo"
)

// -replay measures a memory design on a copy of a chat (tools/memmeasure).
// It feeds a file of messages into the chat as if they came in, one by one,
// and the real summarizer builds their lines. A line {"probe": "..."}
// marks a point in the chat's time: once every line before it is built, the
// view a call would see is kept. After the last message, -replay-agents'
// chats are built too (their logs must be in agents/), then each kept view
// is asked its probe as a side question, back to back, so the cache sees
// the views in the order a chat would, with no wait between them. Each
// probe, then the final view's shape, goes to -replay-out as a JSON line.

// stepsInMain is the prompt's zoom line when subagents' steps are in the
// chat itself (-agent-steps-in-main), and the kind they have there.
const (
	stepsInMainZoom = "- a subagent's own steps are in this chat, as messages of kind step, each\n  starting \"[Name] tool:\", \"[Name] echo:\" and so on: zoom and search them as\n  any other\n"
	stepsInMainKind = "- step: a subagent's own step, starting \"[Name]\" and the step's kind\n"
	workKindLine    = "- work: an agent's report, starting \"[Name]\", or opt-chat's own note, starting \"[opt-chat]\"\n"
)

func stepsInMainPrompt(s string) string {
	s = strings.Replace(s, zoomAgentLines, stepsInMainZoom, 1)
	s = strings.Replace(s, zoomAgentWhole, stepsInMainZoom, 1)
	return strings.Replace(s, workKindLine, workKindLine+stepsInMainKind, 1)
}

type probePoint struct {
	q      string
	before int
	lines  []string
	bytes  int
}

func runReplay(a *Agent, hb *hub, in, out string, agents []string, jobs int) error {
	c := a.chat
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := os.Create(out)
	if err != nil {
		return err
	}
	defer w.Close()
	put := func(v any) {
		b, _ := json.Marshal(v)
		w.Write(append(b, '\n'))
	}
	c.mu.Lock()
	c.hub, c.jobs, c.compact = hb, jobs, a.Compact
	c.pump()
	c.mu.Unlock()
	if a.mems != nil {
		a.mems.setCompact(a.Compact)
	}
	start := time.Now()
	waitBuilt(c)
	var probes []probePoint
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 4<<20)
	n := 0
	for sc.Scan() {
		var item struct {
			Probe string `json:"probe"`
			Msg
		}
		if err := json.Unmarshal(sc.Bytes(), &item); err != nil {
			return fmt.Errorf("%s: %v", in, err)
		}
		if item.Probe != "" {
			waitBuilt(c)
			c.mu.Lock()
			lines := c.callView()
			p := probePoint{q: item.Probe, before: len(c.msgs), lines: lines, bytes: lo.SumBy(lines, func(l string) int { return len(l) + 1 })}
			c.mu.Unlock()
			probes = append(probes, p)
			dim(fmt.Sprintf("[replay] probe %d at message %d: %d lines, %d bytes, %s", len(probes), p.before, len(lines), p.bytes, time.Since(start).Round(time.Second)))
			continue
		}
		c.logMsgs([]Msg{item.Msg})
		if n++; n%100 == 0 {
			dim(fmt.Sprintf("[replay] %d messages in, %s", n, time.Since(start).Round(time.Second)))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	waitBuilt(c)
	mainSecs := time.Since(start).Seconds()
	agentStart := time.Now()
	var chats []*Chat
	for _, name := range agents { // all at once, as subagents running side by side
		ac, err := a.mems.chat(name)
		if err != nil {
			return fmt.Errorf("%s: %v", name, err)
		}
		chats = append(chats, ac)
	}
	for i, ac := range chats {
		waitBuilt(ac)
		dim(fmt.Sprintf("[replay] %s built, %s", agents[i], time.Since(agentStart).Round(time.Second)))
	}
	agentSecs := time.Since(agentStart).Seconds()

	for i, p := range probes {
		t := time.Now()
		r, err := a.btw(p.lines, nil, message{kind: "user", text: p.q}, p.before)
		row := map[string]any{"probe": i + 1, "before": p.before, "viewLines": len(p.lines), "viewBytes": p.bytes,
			"ack": r.ack, "usage": r.result.Usage, "cost": r.result.Cost, "requests": r.result.Turns,
			"secs": time.Since(t).Seconds(), "answer": r.answer}
		if err != nil {
			row["err"] = err.Error()
		}
		put(row)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	var users []int
	for i := len(c.msgs) - 1; i >= 0 && len(users) < 50; i-- {
		if c.msgs[i].Kind == "user" && c.msgs[i].Part == 0 {
			users = append(users, i)
		}
	}
	levels := map[int]int{}
	covered := 0
	for _, i := range users {
		k := c.viewLine(i)
		levels[k.L]++
		covered += 1 << k.L
	}
	put(map[string]any{"final": true, "msgs": len(c.msgs), "viewLines": len(c.view.keys), "viewBytes": c.view.size(),
		"callViewBytes": lo.SumBy(c.callView(), func(l string) int { return len(l) + 1 }), "last50UserLevels": levels, "last50UserMeanCover": float64(covered) / float64(max(len(users), 1)),
		"failing": len(c.failing), "mainBuildSecs": mainSecs, "agentBuildSecs": agentSecs})
	return nil
}

// waitBuilt waits until the chat has no line waiting to be built or being
// built, for two looks in a row. A line that failed and waits to be tried
// again still counts as waiting: a usage limit must not let a probe see a
// view half built.
func waitBuilt(c *Chat) {
	quiet := 0
	for quiet < 2 {
		time.Sleep(time.Second)
		c.mu.Lock()
		done := c.idle()
		c.mu.Unlock()
		if done {
			quiet++
		} else {
			quiet = 0
		}
	}
}
