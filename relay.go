package main

import (
	"bytes"
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
)

// The relay (-experimental-relay) places a cache breakpoint of its own at the
// end of the view's first block, as UniiChat does when it calls the API
// itself. Claude Code marks only the system prompt and the last messages, so
// without it the view's first part is cached by sending it alone and waiting
// for the model's "ok" (ack): a request of its own on every call. With the
// relay, ack sends nothing: the first block goes with the rest of the view in
// one message, and the relay marks it in transit.
//
// Claude Code reaches the relay through ANTHROPIC_BASE_URL, a loopback
// address with a random path. The relay forwards each request to the
// upstream as it came, headers and all, and changes nothing but the body of
// POST /v1/messages. It never reads, keeps or logs the headers.

// relayMark ends the view's first block when the relay is on, in place of
// viewGoesOn: the relay marks the block that ends with it.
const relayMark = "The view goes on in the next block, with the input."

// maxBreakpoints is the API's limit on cache_control marks in one request.
const maxBreakpoints = 4

type relay struct {
	base   string // what ANTHROPIC_BASE_URL gets: http://127.0.0.1:port/<secret>
	trace  io.Writer
	mu     sync.Mutex
	client *http.Client // nil: the default; tests set their own
}

func (r *relay) RoundTrip(q *http.Request) (*http.Response, error) {
	if r.client != nil {
		return r.client.Transport.RoundTrip(q)
	}
	return http.DefaultTransport.RoundTrip(q)
}

// startRelay listens on a loopback port and forwards to upstream. trace, when
// not empty, is a file the relay appends each request's shape to (roles,
// block types and sizes, marks), never its text or headers.
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
	r := &relay{base: "http://" + ln.Addr().String() + prefix}
	if trace != "" {
		f, err := os.OpenFile(trace, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			ln.Close()
			return nil, err
		}
		r.trace = f
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
			if r.trace != nil && resp.StatusCode >= 400 { // the API's error, never a header
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(b))
				r.note(map[string]any{"status": resp.StatusCode, "error": string(b)})
			}
			return nil
		},
		FlushInterval: -1,                         // server-sent events go on as they come
		ErrorLog:      log.New(io.Discard, "", 0), // a failed request reaches Claude Code as a 502, which it retries
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
			if marked, shape, err := markView(body); err == nil {
				r.note(shape)
				body = marked
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

func (r *relay) note(shape any) {
	if r.trace == nil {
		return
	}
	b, _ := json.Marshal(shape)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Write(append(b, '\n'))
}

var errNoMark = errors.New("no block ends with the mark")

// markView puts a cache breakpoint on the first text block that ends with
// relayMark, with the TTL of the marks Claude Code placed, and keeps the
// request within the API's breakpoint limit by dropping the earliest of the
// system prompt's marks, which the later ones cover. It returns the body
// unchanged with an error when it finds nothing to mark or can't parse it.
// shape describes the request without its text.
func markView(body []byte) ([]byte, map[string]any, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return body, nil, err
	}
	var system, tools []map[string]json.RawMessage
	var msgs []map[string]json.RawMessage
	json.Unmarshal(req["system"], &system) // a string, or absent: no marks there
	json.Unmarshal(req["tools"], &tools)
	if err := json.Unmarshal(req["messages"], &msgs); err != nil {
		return body, nil, err
	}
	contents := make([][]map[string]json.RawMessage, len(msgs))
	for i, m := range msgs {
		json.Unmarshal(m["content"], &contents[i]) // a string: one block, never ours
	}
	marks, ttl := 0, json.RawMessage(nil)
	count := func(bs []map[string]json.RawMessage) {
		for _, b := range bs {
			if cc, ok := b["cache_control"]; ok && string(cc) != "null" {
				marks++
				if ttl == nil {
					var c struct {
						TTL json.RawMessage `json:"ttl"`
					}
					json.Unmarshal(cc, &c)
					ttl = c.TTL
				}
			}
		}
	}
	count(system)
	count(tools)
	for _, c := range contents {
		count(c)
	}
	mi, bi := -1, -1
	for i, c := range contents {
		for j, b := range c {
			var t struct{ Type, Text string }
			if json.Unmarshal(mustJSON(b), &t) == nil && t.Type == "text" && strings.HasSuffix(t.Text, relayMark) {
				mi, bi = i, j
				break
			}
		}
		if mi >= 0 {
			break
		}
	}
	if mi < 0 {
		return body, nil, errNoMark
	}
	if _, ok := contents[mi][bi]["cache_control"]; !ok {
		cc := map[string]json.RawMessage{"type": json.RawMessage(`"ephemeral"`)}
		if ttl != nil {
			cc["ttl"] = ttl
		}
		contents[mi][bi]["cache_control"] = mustJSON(cc)
		marks++
	}
	dropped := 0
	for i := 0; marks > maxBreakpoints && i < len(system)-1; i++ {
		if _, ok := system[i]["cache_control"]; ok {
			delete(system[i], "cache_control")
			marks--
			dropped++
		}
	}
	if marks > maxBreakpoints {
		return body, nil, fmt.Errorf("%d marks, over the limit", marks)
	}
	if dropped > 0 {
		req["system"] = mustJSON(system)
	}
	shape := map[string]any{"marked": []int{mi, bi}, "marks": marks, "dropped": dropped}
	if os.Getenv("OPT_CHAT_RELAY_TRACE") != "" { // which part of the prefix changes between calls
		h := func(v any) string { b := sha256.Sum256(mustJSON(v)); return hex.EncodeToString(b[:4]) }
		hs := []string{h(system), h(tools)}
		for _, b := range contents[mi][:bi+1] {
			hs = append(hs, h(b))
		}
		shape["hashes"] = hs
	}
	for i := range msgs {
		if contents[i] != nil {
			msgs[i]["content"] = mustJSON(contents[i])
		}
	}
	// Claude Code may put its environment block (skills, MCP servers:
	// thousands of tokens) in a system message after the first user message,
	// which now holds the whole view and the input: behind them, it would be
	// written to the cache anew on every call. Split the message after the
	// mark and put the block there, followed by the "ok" the ack's model call
	// answered: the layout the ack gave, in the cached prefix. The API
	// refuses a system message followed by a user one.
	if mi+1 < len(msgs) && string(msgs[mi+1]["role"]) == `"system"` && bi+1 < len(contents[mi]) {
		head, tail := maps.Clone(msgs[mi]), maps.Clone(msgs[mi])
		head["content"], tail["content"] = mustJSON(contents[mi][:bi+1]), mustJSON(contents[mi][bi+1:])
		ok := map[string]json.RawMessage{"role": json.RawMessage(`"assistant"`), "content": json.RawMessage(`[{"type":"text","text":"ok"}]`)}
		msgs = slices.Concat(msgs[:mi], []map[string]json.RawMessage{head, msgs[mi+1], ok, tail}, msgs[mi+2:])
		shape["moved"] = true
	}
	var roles []string
	for _, m := range msgs {
		var c []json.RawMessage
		json.Unmarshal(m["content"], &c)
		roles = append(roles, fmt.Sprintf("%s:%d", m["role"], len(c)))
	}
	shape["messages"] = roles
	req["messages"] = mustJSON(msgs)
	out, err := json.Marshal(req)
	if err != nil {
		return body, nil, err
	}
	return out, shape, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
