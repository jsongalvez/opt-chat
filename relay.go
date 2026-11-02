package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// The relay (-experimental-relay) marks the view for the cache the way the
// recipe does (gist §3.3), which Claude Code can't be told to: the view in
// blocks of viewBlock lines from its start, a mark on the last whole one.
// The API looks back up to 20 blocks from a mark for an entry, so the next
// call, its view a few lines longer, reads this one's and pays only for the
// lines after it. Without the relay, opt-chat caches the view up to its last
// multiple of 64 lines by sending that part alone and waiting for an "ok"
// (ack): a request of its own, a rewrite of the whole view at each 64th
// line, and the lines past it read anew by every call.
//
// Claude Code reaches the relay through ANTHROPIC_BASE_URL, a loopback
// address with a random path. The relay forwards each request to the
// upstream as it came, headers and all, and changes nothing but the body of
// POST /v1/messages. It never reads, keeps or logs the headers.

// viewBlock is how many view lines go in each block.
const viewBlock = 4

// backMarks are how many blocks before the view's last whole one its marks
// sit. The recipe has one; pi-optchat's two more, 20 and 40 back, still find
// the last call's entry after a turn that added up to 60 blocks (240 lines,
// tool calls and results): with one, a step of 100 lines rewrote the view
// (24-44k tokens), with three it wrote the new lines (3.1k), in our A/B.
var backMarks = []int{0, 20, 40}

// maxBreakpoints is the API's limit on cache_control marks in one request.
const maxBreakpoints = 4

// viewBlocks splits a view's text, "<chat>\n" lines "</chat>", into
// "<chat>\n", blocks of viewBlock lines, and the lines left with
// "</chat>". last is the index of the last whole block; -1: none.
func viewBlocks(text string) (blocks []string, last int) {
	lines := strings.SplitAfter(strings.TrimSuffix(strings.TrimPrefix(text, "<chat>\n"), "</chat>"), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	blocks = []string{"<chat>\n"}
	for len(lines) >= viewBlock {
		blocks = append(blocks, strings.Join(lines[:viewBlock], ""))
		lines = lines[viewBlock:]
	}
	last = len(blocks) - 1
	if last == 0 {
		last = -1
	}
	return append(blocks, strings.Join(lines, "")+"</chat>"), last
}

type relay struct {
	base   string // what ANTHROPIC_BASE_URL gets: http://127.0.0.1:port/<secret>
	trace  io.Writer
	client *http.Client // nil: the default; tests set their own

	mu    sync.Mutex
	cache map[string]*entry // the prefixes cached or being written, by key (marked.keys)
}

// entry is a prefix in the cache until a time, or, while ready is not nil,
// being written by a call whose response has not begun.
type entry struct {
	until time.Time
	ready chan struct{}
}

func (r *relay) RoundTrip(q *http.Request) (*http.Response, error) {
	if r.client != nil {
		return r.client.Transport.RoundTrip(q)
	}
	return http.DefaultTransport.RoundTrip(q)
}

type flightKey struct{}

// startRelay listens on a loopback port and forwards to upstream. trace, when
// not empty, is a file the relay appends each request's shape to (roles,
// block counts, marks), and the API's errors, never text or headers.
func startRelay(upstream, trace string) (*relay, error) {
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if os.Getenv(k) != "" { // another route than the subscription's: leave it alone
			return nil, fmt.Errorf("%s is set", k)
		}
	}
	up, err := url.Parse(upstream)
	if err != nil || up.Scheme != "https" || up.Host == "" {
		return nil, fmt.Errorf("upstream must be an https URL: %q", upstream)
	}
	secret := make([]byte, 24)
	rand.Read(secret)
	prefix := "/" + hex.EncodeToString(secret)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &relay{base: "http://" + ln.Addr().String() + prefix, cache: map[string]*entry{}}
	if trace != "" {
		f, err := os.OpenFile(trace, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			ln.Close()
			return nil, err
		}
		r.trace = f
	}
	ended := func(q *http.Request, begun bool) {
		if f, ok := q.Context().Value(flightKey{}).(func(bool)); ok {
			f(begun)
		}
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(up) // no X-Forwarded-* headers: those come with SetXForwarded only
			pr.Out.URL.Path = strings.TrimSuffix(up.Path, "/") + strings.TrimPrefix(pr.In.URL.Path, prefix)
			pr.Out.URL.RawPath = ""
			pr.Out.Header.Set("Accept-Encoding", "identity") // an error's body, readable for the trace
		},
		Transport: r,
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode >= 400 {
				ended(resp.Request, false)
				if r.trace != nil { // the API's error, never a header
					b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
					resp.Body.Close()
					resp.Body = io.NopCloser(bytes.NewReader(b))
					r.note(map[string]any{"status": resp.StatusCode, "error": string(b)})
				}
				return nil
			}
			resp.Body = &begins{ReadCloser: resp.Body, begun: func(b bool) { ended(resp.Request, b) }}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, q *http.Request, err error) {
			ended(q, false)
			w.WriteHeader(http.StatusBadGateway) // Claude Code retries it like any network error
		},
		FlushInterval: -1, // server-sent events go on as they come
		ErrorLog:      log.New(io.Discard, "", 0),
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if !strings.HasPrefix(q.URL.Path, prefix+"/") || q.Header.Get("Origin") != "" {
			http.NotFound(w, q)
			return
		}
		if q.Method == http.MethodPost && strings.TrimPrefix(q.URL.Path, prefix) == "/v1/messages" {
			body, err := io.ReadAll(q.Body)
			q.Body.Close()
			if err != nil {
				http.Error(w, "relay: reading the request", http.StatusBadRequest)
				return
			}
			if m, err := markView(body); err == nil {
				r.note(m.shape)
				body = m.body
				q = q.WithContext(context.WithValue(q.Context(), flightKey{}, r.wait(q.Context(), m.keys, m.ttl)))
			} else { // as it came: the call goes on uncached past the system prompt
				r.note(map[string]any{"unmarked": err.Error()})
			}
			q.Body = io.NopCloser(bytes.NewReader(body))
			q.ContentLength = int64(len(body))
			q.Header.Del("Content-Length")
		}
		proxy.ServeHTTP(w, q)
	})}
	go srv.Serve(ln)
	return r, nil
}

// begins tells once whether a response's body began (true) or ended first.
type begins struct {
	io.ReadCloser
	once  sync.Once
	begun func(bool)
}

func (b *begins) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 || err != nil {
		b.once.Do(func() { b.begun(n > 0) })
	}
	return n, err
}

func (b *begins) Close() error {
	b.once.Do(func() { b.begun(false) })
	return b.ReadCloser.Close()
}

// wait is single flight, as UniiChat's (AskAI.ts wait): a call whose prefix
// another call is writing waits until that call's response begins, and then
// reads the entry instead of paying to write it too; compactions start up to
// 8 at once on one view. keys are the request's prefixes at its view marks,
// shortest first. The call leads, writing its longest, unless every one is
// cached already. The returned func, told whether its response began, ends
// its lead.
func (r *relay) wait(ctx context.Context, keys []string, ttl time.Duration) func(bool) {
	none := func(bool) {}
	if len(keys) == 0 {
		return none
	}
	for {
		r.mu.Lock()
		now := time.Now()
		k := -1
		for i := len(keys) - 1; i >= 0; i-- {
			if e := r.cache[keys[i]]; e != nil && (e.ready != nil || e.until.After(now)) {
				k = i
				break
			}
		}
		if k >= 0 && r.cache[keys[k]].ready != nil {
			ready := r.cache[keys[k]].ready
			r.mu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return none
			}
		}
		if k >= 0 {
			r.cache[keys[k]].until = now.Add(ttl) // read: kept there
		}
		if k == len(keys)-1 {
			r.mu.Unlock()
			return none
		}
		ready := make(chan struct{})
		r.cache[keys[len(keys)-1]] = &entry{ready: ready}
		maps.DeleteFunc(r.cache, func(_ string, e *entry) bool { return e.ready == nil && !e.until.After(now) })
		r.mu.Unlock()
		var once sync.Once
		return func(begun bool) {
			once.Do(func() {
				r.mu.Lock()
				defer r.mu.Unlock()
				if begun { // every mark of the request wrote an entry
					for _, key := range keys {
						r.cache[key] = &entry{until: time.Now().Add(ttl)}
					}
				} else {
					delete(r.cache, keys[len(keys)-1])
				}
				close(ready)
			})
		}
	}
}

func (r *relay) note(shape any) {
	if r.trace == nil {
		return
	}
	b, _ := json.Marshal(shape)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Write(append(b, '\n'))
}

var errNoView = errors.New("no view")

// marked is a request as the relay sends it on.
type marked struct {
	body  []byte
	keys  []string      // its prefixes at its view marks, shortest first (wait)
	ttl   time.Duration // how long the API keeps them
	shape map[string]any
}

// Where the marks go, in the order they are kept within the API's limit:
// Claude Code's after the view (its turn's tool calls read them back), the
// view's last whole block, the view's earlier marks, those before the view
// (its environment block), then the system prompt's, which every later one
// covers.
const (
	rankTail = iota
	rankView
	rankBack
	rankBefore
	rankSystem
)

type markAt struct {
	block map[string]json.RawMessage
	rank  int
	view  bool
}

// markView lays out a request the recipe's way. It splits the view, the
// first text block that is "<chat>\n" ... "</chat>" in a user message, into
// blocks (viewBlocks) and marks the last whole one, with the TTL of Claude
// Code's marks. When Claude Code put its environment block in a system
// message right after that user message, it moves it before the view, with
// the "ok" the API needs after a system message: there, with the system
// prompt and tools, it is the same in every call. It keeps the request within
// the API's limit on marks by rank. It returns an error, and the body
// unchanged, when it finds no view or can't parse the body.
func markView(body []byte) (marked, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return marked{}, err
	}
	var system, tools []map[string]json.RawMessage
	var msgs []map[string]json.RawMessage
	json.Unmarshal(req["system"], &system) // a string, or absent: no marks there
	json.Unmarshal(req["tools"], &tools)
	if err := json.Unmarshal(req["messages"], &msgs); err != nil {
		return marked{}, err
	}
	contents := make([][]map[string]json.RawMessage, len(msgs))
	for i, m := range msgs {
		json.Unmarshal(m["content"], &contents[i]) // a string: one block, never the view
	}
	textOf := func(b map[string]json.RawMessage) (string, bool) {
		var t struct{ Type, Text string }
		json.Unmarshal(mustJSON(b), &t)
		return t.Text, t.Type == "text"
	}
	mi, vi := -1, -1
	for i, c := range contents {
		if string(msgs[i]["role"]) != `"user"` {
			continue
		}
		for j, b := range c {
			if t, ok := textOf(b); ok && strings.HasPrefix(t, "<chat>\n") && strings.HasSuffix(t, "</chat>") {
				mi, vi = i, j
				break
			}
		}
		if mi >= 0 {
			break
		}
	}
	if mi < 0 {
		return marked{}, errNoView
	}
	var ttlRaw json.RawMessage
	var marks []markAt
	note := func(bs []map[string]json.RawMessage, rank int) {
		for _, b := range bs {
			if cc, ok := b["cache_control"]; ok && string(cc) != "null" {
				marks = append(marks, markAt{block: b, rank: rank})
				if ttlRaw == nil {
					var c struct {
						TTL json.RawMessage `json:"ttl"`
					}
					json.Unmarshal(cc, &c)
					ttlRaw = c.TTL
				}
			}
		}
	}
	note(system, rankSystem)
	note(tools, rankSystem)
	text, _ := textOf(contents[mi][vi])
	pieces, last := viewBlocks(text)
	view := make([]map[string]json.RawMessage, len(pieces))
	for k, p := range pieces {
		view[k] = map[string]json.RawMessage{"type": json.RawMessage(`"text"`), "text": mustJSON(p)}
	}
	if c, ok := contents[mi][vi]["cache_control"]; ok {
		view[len(view)-1]["cache_control"] = c
	}
	head, tail := contents[mi][:vi], slices.Concat(view, contents[mi][vi+1:])
	moved := mi+1 < len(msgs) && string(msgs[mi+1]["role"]) == `"system"` && len(head) > 0
	for i := range contents {
		switch {
		case i < mi:
			note(contents[i], rankBefore)
		case i == mi:
			note(head, rankBefore)
			note(tail, rankTail)
		case i == mi+1 && moved:
			note(contents[i], rankBefore)
		default:
			note(contents[i], rankTail)
		}
	}
	cc := map[string]json.RawMessage{"type": json.RawMessage(`"ephemeral"`)}
	if ttlRaw != nil {
		cc["ttl"] = ttlRaw
	}
	var viewMarks []int // indices in view, shortest prefix first
	for _, back := range slices.Backward(backMarks) {
		if j := last - back; last >= 0 && j >= 1 {
			viewMarks = append(viewMarks, j)
			rank := rankView
			if back > 0 {
				rank = rankBack
			}
			view[j]["cache_control"] = mustJSON(cc)
			marks = append(marks, markAt{block: view[j], rank: rank, view: true})
		}
	}
	slices.SortStableFunc(marks, func(a, b markAt) int { return a.rank - b.rank })
	for _, m := range marks[min(len(marks), maxBreakpoints):] {
		delete(m.block, "cache_control")
	}
	viewMarks = slices.DeleteFunc(viewMarks, func(j int) bool { _, ok := view[j]["cache_control"]; return !ok })

	// The keys: the prefix's bytes, marks aside, up to each view mark.
	h := sha256.New()
	strip := func(b map[string]json.RawMessage) []byte {
		c := maps.Clone(b)
		delete(c, "cache_control")
		return mustJSON(c)
	}
	h.Write(req["model"])
	for _, b := range system {
		h.Write(strip(b))
	}
	for _, b := range tools {
		h.Write(strip(b))
	}
	for i := range mi {
		h.Write(msgs[i]["role"])
		for _, b := range contents[i] {
			h.Write(strip(b))
		}
	}
	for _, b := range head {
		h.Write(strip(b))
	}
	if moved {
		for _, b := range contents[mi+1] {
			h.Write(strip(b))
		}
	}
	var keys []string
	for j, b := range view {
		h.Write(strip(b))
		if slices.Contains(viewMarks, j) {
			keys = append(keys, hex.EncodeToString(h.Sum(nil)))
		}
	}

	if moved {
		env := msgs[mi+1]
		first, rest := maps.Clone(msgs[mi]), maps.Clone(msgs[mi])
		first["content"], rest["content"] = mustJSON(head), mustJSON(tail)
		if contents[mi+1] != nil {
			env["content"] = mustJSON(contents[mi+1])
		}
		ok := map[string]json.RawMessage{"role": json.RawMessage(`"assistant"`), "content": json.RawMessage(`[{"type":"text","text":"ok"}]`)}
		for i := range msgs {
			if i != mi && i != mi+1 && contents[i] != nil {
				msgs[i]["content"] = mustJSON(contents[i])
			}
		}
		msgs = slices.Concat(msgs[:mi], []map[string]json.RawMessage{first, env, ok, rest}, msgs[mi+2:])
	} else {
		contents[mi] = slices.Concat(head, tail)
		for i := range msgs {
			if contents[i] != nil {
				msgs[i]["content"] = mustJSON(contents[i])
			}
		}
	}
	if system != nil {
		req["system"] = mustJSON(system)
	}
	if tools != nil {
		req["tools"] = mustJSON(tools)
	}
	req["messages"] = mustJSON(msgs)
	out, err := json.Marshal(req)
	if err != nil {
		return marked{}, err
	}
	ttl := 4 * time.Minute // the API's 5, with room
	if string(ttlRaw) == `"1h"` {
		ttl = 55 * time.Minute
	}
	shape := map[string]any{"view": []int{mi, vi, len(pieces), last}, "marks": len(marks[:min(len(marks), maxBreakpoints)]),
		"dropped": max(0, len(marks)-maxBreakpoints), "viewMarks": viewMarks, "moved": moved, "ttl": string(ttlRaw)}
	if os.Getenv("OPT_CHAT_RELAY_TRACE") != "" { // which part of the prefix changes between calls
		hash := func(v any) string { b := sha256.Sum256(mustJSON(v)); return hex.EncodeToString(b[:4]) }
		var sys, tls, hd []string
		for _, b := range system {
			sys = append(sys, hash(strip(b)))
		}
		for _, b := range tools {
			var n struct{ Name string }
			json.Unmarshal(mustJSON(b), &n)
			tls = append(tls, n.Name+":"+hash(strip(b)))
		}
		for _, b := range head {
			hd = append(hd, hash(strip(b)))
		}
		shape["system"], shape["tools"], shape["head"] = sys, tls, hd
		if len(keys) > 0 {
			shape["key"] = keys[len(keys)-1][:8]
		}
	}
	return marked{body: out, keys: keys, ttl: ttl, shape: shape}, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
