package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMarkView(t *testing.T) {
	body := `{"model":"m","system":[{"type":"text","text":"a"},{"type":"text","text":"b","cache_control":{"type":"ephemeral","ttl":"1h"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"env"},{"type":"text","text":"view\n` + relayMark + `"},{"type":"text","text":"rest","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],"stream":true}`
	out, shape, err := markView([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Model    string
		Stream   bool
		Messages []struct {
			Content []struct {
				Text         string
				CacheControl *struct{ Type, TTL string } `json:"cache_control"`
			}
		}
	}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	b := req.Messages[0].Content
	if req.Model != "m" || !req.Stream || b[0].CacheControl != nil || b[1].CacheControl == nil || b[1].CacheControl.TTL != "1h" || b[2].CacheControl == nil {
		t.Fatalf("marked wrong: %s", out)
	}
	if shape["marks"] != 3 || shape["dropped"] != 0 {
		t.Fatalf("shape %v", shape)
	}
}

func TestMarkViewKeepsTheLimit(t *testing.T) {
	cc := `"cache_control":{"type":"ephemeral"}`
	body := `{"system":[{"type":"text","text":"a",` + cc + `},{"type":"text","text":"b",` + cc + `}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"v ` + relayMark + `"},{"type":"text","text":"x",` + cc + `}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"y",` + cc + `}]}]}`
	out, shape, err := markView([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out), "cache_control"); n != maxBreakpoints || shape["dropped"] != 1 {
		t.Fatalf("%d marks, shape %v: %s", n, shape, out)
	}
	if !strings.Contains(string(out), `{"text":"a","type":"text"}`) { // the earliest goes
		t.Fatalf("dropped the wrong mark: %s", out)
	}
}

func TestMarkViewLeavesOthersAlone(t *testing.T) {
	for _, body := range []string{`{"messages":[{"role":"user","content":"hi"}]}`, `not json`, `{"messages":[{"role":"user","content":[{"type":"text","text":"no mark"}]}]}`} {
		if out, _, err := markView([]byte(body)); err == nil || string(out) != body {
			t.Errorf("%s: changed to %s, err %v", body, out, err)
		}
	}
}

// The relay forwards headers untouched, marks only POST /v1/messages, and
// answers nothing outside its secret path.
func TestRelayForwards(t *testing.T) {
	var got struct {
		path, auth, beta string
		body             string
	}
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
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"v ` + relayMark + `"}]}]}`
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
	base := r.base[:strings.LastIndex(r.base, "/")]
	if resp, err := http.Post(base+"/v1/messages", "application/json", strings.NewReader(body)); err != nil || resp.StatusCode != 404 {
		t.Fatalf("outside the secret path: %v %v", resp, err)
	}
}

func TestMarkViewMovesTheEnvironment(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"v ` + relayMark + `"},{"type":"text","text":"rest"}]},` +
		`{"role":"system","content":[{"type":"text","text":"env"}]},{"role":"assistant","content":[{"type":"text","text":"a"}]}]}`
	out, _, err := markView([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Messages []struct {
			Role    string
			Content []struct{ Text string }
		}
	}
	json.Unmarshal(out, &req)
	var got []string
	for _, m := range req.Messages {
		got = append(got, m.Role+":"+m.Content[0].Text)
	}
	if want := "user:v " + relayMark + " system:env assistant:ok user:rest assistant:a"; strings.Join(got, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, " "), want)
	}
}

type bufCloser struct{ bytes.Buffer }

func (*bufCloser) Close() error { return nil }

// With the relay, ack sends nothing: the first message goes with the next,
// ending with the relay's mark, and the calls waiting on it go once the
// response to that begins.
func TestAckWithRelay(t *testing.T) {
	a := &Agent{writing: map[string]chan struct{}{}, relay: &relay{}}
	out := `{"type":"stream_event","event":{"type":"message_start"}}` + "\n" + `{"type":"result","result":"line"}`
	in := &bufCloser{}
	p := &proc{prefix: "p", ctx: t.Context(), stdin: in, out: bufio.NewScanner(strings.NewReader(out))}
	if err := a.ack(p, []block{textBlock("view\n" + viewGoesOn)}, &usage{}, nil); err != nil || in.Len() != 0 {
		t.Fatalf("ack sent %q, err %v", in.String(), err)
	}
	p.send(textBlock("rest"))
	var m struct{ Message struct{ Content []block } }
	if err := json.Unmarshal(in.Bytes(), &m); err != nil || len(m.Message.Content) != 2 ||
		m.Message.Content[0].Text != "view\n"+relayMark || m.Message.Content[1].Text != "rest" {
		t.Fatalf("sent %s", in.String())
	}
	key := "p\x00view\n" + viewGoesOn
	held := func() bool { a.mu.Lock(); defer a.mu.Unlock(); _, ok := a.writing[key]; return ok }
	if !held() {
		t.Fatal("not held before the response")
	}
	p.next()
	if held() || !a.inCache(t.Context(), key) {
		t.Fatal("not released and cached as the response began")
	}
}
