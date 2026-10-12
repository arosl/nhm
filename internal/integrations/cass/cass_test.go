package cass

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// script answers each call with the next output.
type script struct {
	outs []string
	got  [][]string
}

func (s *script) run(_ context.Context, args ...string) ([]byte, error) {
	s.got = append(s.got, args)
	out := s.outs[0]
	s.outs = s.outs[1:]
	return []byte(out), nil
}

const searchAnswer = `{"query":"q","count":1,"hits":[{"title":"T","snippet":"s","content":"the content",
"score":2.7,"source_path":"/h/.codex/sessions/2026/10/12/rollout-x-abc.jsonl","agent":"codex","workspace":"/p",
"created_at":1791777605000,"conversation_id":2,"line_number":7,"match_type":"exact"}]}`

func TestSearchArgsAndHits(t *testing.T) {
	s := &script{outs: []string{searchAnswer}}
	c := Client{Run: s.run, DataDir: "/d"}
	hits, err := c.Search(context.Background(), Search{Query: "q", Workspace: "/p", Limit: 5, Days: 7, MaxContent: 1024})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"search", "q", "--robot", "--limit", "5", "--workspace", "/p", "--days", "7",
		"--max-content-length", "1024", "--data-dir", "/d"}
	if !reflect.DeepEqual(s.got[0], want) {
		t.Fatalf("args = %q", s.got[0])
	}
	if len(hits) != 1 || hits[0].ID() != "/h/.codex/sessions/2026/10/12/rollout-x-abc.jsonl:7" {
		t.Fatalf("hits = %+v", hits)
	}
	if got := hits[0].CreatedAt(); !got.Equal(time.UnixMilli(1791777605000)) {
		t.Fatalf("created = %s", got)
	}
}

func TestIndexSessionConfirmsByExactPath(t *testing.T) {
	const path = "/h/.claude/projects/-p/abc.jsonl"
	listed := `{"sessions":[{"path":"` + path + `","workspace":"/p","agent":"claude_code"}]}`
	s := &script{outs: []string{`{"success":true,"conversations":1}`, listed}}
	c := Client{Run: s.run}
	state, err := c.IndexSession(context.Background(), path, "/p", "b1-abc")
	if err != nil || state != Indexed {
		t.Fatalf("state = %q, %v", state, err)
	}
	if want := []string{"index", "--watch-once", path, "--json", "--idempotency-key", "b1-abc"}; !reflect.DeepEqual(s.got[0], want) {
		t.Fatalf("args = %q", s.got[0])
	}
	// cass answers success for a path it ignored (experiment 5): the
	// session is not listed, so it is "not found yet", not an error.
	s = &script{outs: []string{`{"success":true,"conversations":0}`, `{"sessions":[{"path":"/other.jsonl"}]}`}}
	c = Client{Run: s.run}
	state, err = c.IndexSession(context.Background(), path, "/p", "")
	if err != nil || state != NotFoundYet {
		t.Fatalf("state = %q, %v", state, err)
	}
	s = &script{outs: []string{`{"success":false}`}}
	c = Client{Run: s.run}
	if _, err := c.IndexSession(context.Background(), path, "", ""); err == nil {
		t.Fatal("no error for success false")
	}
}
