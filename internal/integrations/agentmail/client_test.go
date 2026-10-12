package agentmail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type rpcCall struct {
	Method string `json:"method"`
	Params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"params"`
}

func mailServer(t *testing.T, handle func(rpcCall) (string, bool)) (*httptest.Server, *[]http.Header) {
	t.Helper()
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Clone())
		body, _ := io.ReadAll(r.Body)
		var call rpcCall
		_ = json.Unmarshal(body, &call)
		text, isErr := handle(call)
		res, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"isError": isErr, "content": []any{map[string]any{"type": "text", "text": text}}}})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: " + string(res) + "\n\n"))
	}))
	t.Cleanup(srv.Close)
	return srv, &headers
}

func TestFetchInboxPeeksWithoutMarkingRead(t *testing.T) {
	var got rpcCall
	srv, headers := mailServer(t, func(c rpcCall) (string, bool) {
		got = c
		return `[{"id":7,"subject":"[acfs-1] Start","from":"BlueLake","created_ts":"2026-10-12T04:00:00Z","importance":"urgent","ack_required":true},
			{"id":8,"subject":"fyi","from":"RedStone","created_ts":"2026-10-12T04:01:00Z","importance":"normal","ack_required":false}]`, false
	})
	c := &Client{URL: srv.URL, Token: "tok-1"}
	msgs, err := c.FetchInbox(context.Background(), "/p", "IcyKnoll", 50)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "tools/call" || got.Params.Name != "fetch_inbox" || got.Params.Arguments["mark_read"] != false ||
		got.Params.Arguments["unread_only"] != true || got.Params.Arguments["agent_name"] != "IcyKnoll" {
		t.Fatalf("call = %+v", got)
	}
	if (*headers)[0].Get("Authorization") != "Bearer tok-1" {
		t.Fatalf("auth header = %q", (*headers)[0].Get("Authorization"))
	}
	u := Summarize(msgs)
	if len(msgs) != 2 || u.Unread != 2 || u.Urgent != 1 || u.PendingAcks != 1 {
		t.Fatalf("msgs %d, summary %+v", len(msgs), u)
	}
}

func TestToolErrors(t *testing.T) {
	srv, _ := mailServer(t, func(c rpcCall) (string, bool) {
		return "Agent 'Ghost' not found in project", true
	})
	c := &Client{URL: srv.URL}
	if _, err := c.FetchInbox(context.Background(), "/p", "Ghost", 5); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("not registered: %v", err)
	}
	var te *ToolError
	if err := c.Call(context.Background(), "whois", map[string]any{}, nil); !errors.As(err, &te) {
		t.Fatalf("tool error: %v", err)
	}
}

func TestHTTPFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	if err := (&Client{URL: srv.URL}).Call(context.Background(), "x", nil, nil); err == nil {
		t.Fatal("HTTP 401 accepted")
	}
	if err := (&Client{URL: "http://127.0.0.1:1/mcp/"}).Call(context.Background(), "x", nil, nil); err == nil {
		t.Fatal("a refused connection accepted")
	}
}

func TestNewClientReadsTheToken(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "mcp-agent-mail"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "mcp-agent-mail", "config.env"),
		[]byte("OTHER=1\nHTTP_BEARER_TOKEN=\"old\"\nHTTP_BEARER_TOKEN='abc'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("ACFS_AGENT_MAIL_PORT", "9999")
	c, err := NewClient(home)
	if err != nil || c.Token != "abc" || c.URL != "http://127.0.0.1:9999/mcp/" {
		t.Fatalf("client = %+v %v", c, err)
	}
	c, err = NewClient(t.TempDir())
	if err != nil || c.Token != "" {
		t.Fatalf("no config = %+v %v", c, err)
	}
}

func TestLastJSONLine(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`:                                `{"a":1}`,
		"event: message\ndata: {\"b\":2}\n\n":    `{"b":2}`,
		"data: {\"x\":1}\r\ndata: {\"y\":2}\r\n": `{"y":2}`,
	} {
		if got := string(lastJSONLine([]byte(in))); got != want {
			t.Errorf("lastJSONLine(%q) = %q, want %q", in, got, want)
		}
	}
}
