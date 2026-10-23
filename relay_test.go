package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestViewBlocks(t *testing.T) {
	for n, want := range map[int]int{0: -1, 3: -1, 4: 1, 9: 2, 12: 3} {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "%d+1|line\n", i)
		}
		text := "<chat>\n" + b.String() + "</chat>"
		blocks, last := viewBlocks(text)
		if strings.Join(blocks, "") != text || last != want || len(blocks) != n/viewBlock+2 {
			t.Errorf("%d lines: last %d, want %d, blocks %q", n, last, want, blocks)
		}
	}
}

// shapeOf renders a request's messages as role:text, a block's mark as *ttl.
func shapeOf(t *testing.T, body []byte) []string {
	t.Helper()
	var req struct {
		System []struct {
			CacheControl *struct{ TTL string } `json:"cache_control"`
		}
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range req.System {
		if s.CacheControl != nil {
			got = append(got, "system*")
		}
	}
	for _, m := range req.Messages {
		var bs []struct {
			Text         string
			CacheControl *struct{ TTL string } `json:"cache_control"`
		}
		if json.Unmarshal(m.Content, &bs) != nil {
			got = append(got, m.Role+":(string)")
			continue
		}
		for _, b := range bs {
			s := m.Role + ":" + b.Text
			if b.CacheControl != nil {
				s += "*" + b.CacheControl.TTL
			}
			got = append(got, s)
		}
	}
	return got
}

const cc1h = `"cache_control":{"type":"ephemeral","ttl":"1h"}`

// The view comes back in blocks, its last whole one marked with Claude
// Code's TTL, and Claude Code's environment block moves before it, with an
// "ok" after it. Four marks, within the limit: all stay.
func TestMarkView(t *testing.T) {
	body := `{"model":"m","stream":true,"system":[{"type":"text","text":"a",` + cc1h + `},{"type":"text","text":"b",` + cc1h + `}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"reminder"},{"type":"text","text":"<chat>\n1\n2\n3\n4\n5\n6\n7\n8\n9\n</chat>"},{"type":"text","text":"input"}]},` +
		`{"role":"system","content":[{"type":"text","text":"env",` + cc1h + `}]}]}`
	m, err := markView([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"system*", "system*", "user:reminder", "system:env*1h", "assistant:ok", "user:<chat>\n", "user:1\n2\n3\n4\n", "user:5\n6\n7\n8\n*1h", "user:9\n</chat>", "user:input"}
	if got := shapeOf(t, m.body); !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if len(m.keys) != 1 || m.ttl < 50*time.Minute || !strings.Contains(string(m.body), `"stream":true`) {
		t.Fatalf("keys %v, ttl %v", m.keys, m.ttl)
	}
	// The next call, its view a line longer and its input another: the same key
	// up to the same block, so it reads what this one wrote.
	next := strings.Replace(strings.Replace(body, `9\n</chat>`, `9\n10\n</chat>`, 1), `"input"`, `"other"`, 1)
	n, _ := markView([]byte(next))
	if n.keys[0] != m.keys[0] {
		t.Fatal("the same prefix keyed apart")
	}
}

// Over the limit, Claude Code's marks after the view stay, and the view's.
func TestMarkViewKeepsTheLimit(t *testing.T) {
	body := `{"system":[{"type":"text","text":"a",` + cc1h + `},{"type":"text","text":"b",` + cc1h + `}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"r",` + cc1h + `},{"type":"text","text":"<chat>\n1\n2\n3\n4\n</chat>"},{"type":"text","text":"x",` + cc1h + `}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"y",` + cc1h + `}]}]}`
	m, err := markView([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user:r*1h", "user:<chat>\n", "user:1\n2\n3\n4\n*1h", "user:</chat>", "user:x*1h", "assistant:y*1h"}
	if got := shapeOf(t, m.body); !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestMarkViewLeavesOthersAlone(t *testing.T) {
	for _, body := range []string{`{"messages":[{"role":"user","content":"hi"}]}`, `not json`, `{"messages":[{"role":"user","content":[{"type":"text","text":"no view"}]}]}`} {
		if _, err := markView([]byte(body)); err == nil {
			t.Errorf("%s: marked", body)
		}
	}
}

// One call writes a prefix; another on it waits until that one's response
// begins, then goes on without writing; one that fails lets the next lead.
func TestRelayWait(t *testing.T) {
	r := &relay{cache: map[string]*entry{}}
	lead := r.wait(t.Context(), []string{"k"}, time.Minute)
	waited := make(chan func(bool))
	go func() { waited <- r.wait(t.Context(), []string{"k"}, time.Minute) }()
	select {
	case <-waited:
		t.Fatal("did not wait for the leader")
	case <-time.After(50 * time.Millisecond):
	}
	lead(true)
	f := <-waited
	f(true)
	r.mu.Lock()
	e := r.cache["k"]
	r.mu.Unlock()
	if e == nil || e.ready != nil || !e.until.After(time.Now()) {
		t.Fatalf("not cached after the response began: %+v", e)
	}
	failed := r.wait(t.Context(), []string{"k", "k2"}, time.Minute)
	failed(false)
	r.mu.Lock()
	_, kept := r.cache["k2"]
	r.mu.Unlock()
	if kept {
		t.Fatal("a failed lead left its key")
	}
}

// The relay forwards headers untouched, marks only POST /v1/messages, and
// answers nothing outside its secret path.
func TestRelayForwards(t *testing.T) {
	var got struct{ path, auth, beta, body string }
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		b, _ := io.ReadAll(q.Body)
		got.path, got.auth, got.beta, got.body = q.URL.RequestURI(), q.Header.Get("Authorization"), q.Header.Get("Anthropic-Beta"), string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: x\n\n")
	}))
	defer up.Close()
	r, err := startRelay(up.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	r.client = up.Client()
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"<chat>\n1\n2\n3\n4\n</chat>"}]}]}`
	q, _ := http.NewRequest("POST", r.base+"/v1/messages?beta=true", strings.NewReader(body))
	q.Header.Set("Authorization", "Bearer secret")
	q.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	resp, err := http.DefaultClient.Do(q)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "event: x\n\n" || got.path != "/v1/messages?beta=true" || got.auth != "Bearer secret" ||
		got.beta != "oauth-2025-04-20" || !strings.Contains(got.body, "cache_control") {
		t.Fatalf("status %d %q, upstream got %+v", resp.StatusCode, b, got)
	}
	r.mu.Lock()
	n := len(r.cache)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d entries after a response began, want 1", n)
	}
	base := r.base[:strings.LastIndex(r.base, "/")]
	if resp, err := http.Post(base+"/v1/messages", "application/json", strings.NewReader(body)); err != nil || resp.StatusCode != 404 {
		t.Fatalf("outside the secret path: %v %v", resp, err)
	}
}

// The view's marks sit on its last whole block and 20 and 40 blocks before.
func TestMarkViewBackMarks(t *testing.T) {
	var b strings.Builder
	for i := range 45 * viewBlock {
		fmt.Fprintf(&b, "%d\n", i)
	}
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"r"},{"type":"text","text":` + string(mustJSON("<chat>\n"+b.String()+"</chat>")) + `}]}]}`
	m, err := markView([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.shape["viewMarks"]; !slices.Equal(got.([]int), []int{5, 25, 45}) || len(m.keys) != 3 {
		t.Fatalf("marks at %v, keys %d", got, len(m.keys))
	}
}
