package agentmail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// overseerFake is Agent Mail at /mcp/ and its web UI's overseer route.
// refuse names the tools that answer isError; httpStatus is the HTTP
// route's status.
type overseerFake struct {
	mu         sync.Mutex
	refuse     map[string]bool
	httpStatus int
	calls      []string
	args       map[string]map[string]any
	httpBody   map[string]any
	httpPath   string
}

func newOverseerFake(t *testing.T, refuse ...string) (*Client, *overseerFake) {
	t.Helper()
	f := &overseerFake{refuse: map[string]bool{}, httpStatus: http.StatusOK, args: map[string]map[string]any{}}
	for _, r := range refuse {
		f.refuse[r] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/mail/") {
			f.httpPath = r.URL.Path
			_ = json.Unmarshal(body, &f.httpBody)
			w.WriteHeader(f.httpStatus)
			if f.httpStatus == http.StatusBadRequest {
				_, _ = io.WriteString(w, `{"detail":"unknown recipient: Nobody"}`)
				return
			}
			_, _ = io.WriteString(w, `{"success":true,"message_id":77,"recipients":["BlueLake"],"sent_at":"2026-10-24T06:00:00Z"}`)
			return
		}
		var call struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &call)
		f.calls = append(f.calls, call.Params.Name)
		f.args[call.Params.Name] = call.Params.Arguments
		text, isErr := "{}", f.refuse[call.Params.Name]
		switch {
		case isErr:
			text = "refused"
		case call.Params.Name == "ensure_project":
			text = `{"slug":"data-p"}`
		case call.Params.Name == "send_message":
			text = `{"deliveries":[{"project":"/data/p","payload":{"id":5812,"created_ts":"2026-10-24T01:49:27.655272Z"}}],"count":1}`
		}
		res, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"isError": isErr, "content": []any{map[string]any{"type": "text", "text": text}}}})
		_, _ = w.Write(res)
	}))
	t.Cleanup(srv.Close)
	return &Client{URL: srv.URL + "/mcp/", Token: "tok", HTTP: srv.Client()}, f
}

func overseerMessage() OverseerMessage {
	return OverseerMessage{ProjectKey: "/data/p", ProjectSlug: "data-p", Recipients: []string{"BlueLake", "IcyKnoll"},
		Subject: "Stop", BodyMD: "Checkpoint now.", ThreadID: "FEAT-1"}
}

// The MCP route: HumanOverseer registers, then sends with high
// importance, the preamble and the thread.
func TestSendOverseerMCP(t *testing.T) {
	c, f := newOverseerFake(t)
	r, err := c.SendOverseer(context.Background(), overseerMessage())
	if err != nil {
		t.Fatal(err)
	}
	want := OverseerResult{Success: true, MessageID: 5812, Recipients: []string{"BlueLake", "IcyKnoll"},
		SentAt: time.Date(2026, 10, 24, 1, 49, 27, 655272000, time.UTC)}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("result = %+v", r)
	}
	if fmt.Sprint(f.calls) != "[ensure_project register_agent send_message]" {
		t.Fatalf("calls = %v", f.calls)
	}
	reg, send := f.args["register_agent"], f.args["send_message"]
	if reg["name"] != HumanOverseer || reg["program"] != "nhm" || reg["model"] != "human" {
		t.Fatalf("register_agent = %v", reg)
	}
	if send["sender_name"] != HumanOverseer || send["importance"] != "high" || send["thread_id"] != "FEAT-1" ||
		send["body_md"] != HumanOverseerPreamble+"Checkpoint now." || fmt.Sprint(send["to"]) != "[BlueLake IcyKnoll]" {
		t.Fatalf("send_message = %v", send)
	}
	if f.httpPath != "" {
		t.Fatalf("the HTTP route was used: %s", f.httpPath)
	}
}

// A refused registration or send falls back to the web UI's route, which
// gets the body without the preamble (the server adds its own).
func TestSendOverseerFallsBackToHTTP(t *testing.T) {
	for _, refused := range []string{"register_agent", "send_message"} {
		c, f := newOverseerFake(t, refused)
		r, err := c.SendOverseer(context.Background(), overseerMessage())
		if err != nil || r.MessageID != 77 || !r.Success {
			t.Fatalf("%s refused: %+v %v", refused, r, err)
		}
		if f.httpPath != "/mail/data-p/overseer/send" || f.httpBody["body_md"] != "Checkpoint now." || f.httpBody["thread_id"] != "FEAT-1" {
			t.Fatalf("%s refused: HTTP %s %v", refused, f.httpPath, f.httpBody)
		}
	}
	// Both routes fail: the MCP error for a refused send.
	c, f := newOverseerFake(t, "send_message")
	f.httpStatus = http.StatusBadRequest
	if _, err := c.SendOverseer(context.Background(), overseerMessage()); err == nil || !strings.Contains(err.Error(), "agentmail send_message: refused") {
		t.Fatalf("both fail: %v", err)
	}
	// The HTTP route alone: its 400 detail.
	m := overseerMessage()
	m.ProjectKey = ""
	if _, err := c.SendOverseer(context.Background(), m); err == nil || err.Error() != "agentmail overseer_send: unknown recipient: Nobody" {
		t.Fatalf("HTTP 400: %v", err)
	}
	m.ProjectSlug = ""
	if _, err := c.SendOverseer(context.Background(), m); err == nil || !strings.Contains(err.Error(), "project_slug required") {
		t.Fatalf("no slug: %v", err)
	}
}

func TestProjectSlugFromPath(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/jemanuel/projects/ntm": "users-jemanuel-projects-ntm",
		"/data/projects/My Repo.v2/":   "data-projects-my-repo-v2",
		"/":                            "root",
		"":                             "",
	} {
		if got := ProjectSlugFromPath(in); got != want {
			t.Errorf("ProjectSlugFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMarkReadAndAcknowledge(t *testing.T) {
	c, f := newOverseerFake(t)
	if err := c.MarkRead(context.Background(), "/data/p", "BlueLake", 12); err != nil {
		t.Fatal(err)
	}
	if err := c.Acknowledge(context.Background(), "/data/p", "BlueLake", 13); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.args["mark_message_read"]) != "map[agent_name:BlueLake message_id:12 project_key:/data/p]" ||
		fmt.Sprint(f.args["acknowledge_message"]) != "map[agent_name:BlueLake message_id:13 project_key:/data/p]" {
		t.Fatalf("args = %v", f.args)
	}
	if slug, err := c.EnsureProject(context.Background(), "/data/p"); err != nil || slug != "data-p" {
		t.Fatalf("EnsureProject = %q, %v", slug, err)
	}
}
