package cass

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// ACFS's E06 (nhm-gne): a cass wrapper whose child holds stdout (sh:
// sleep 30; exec cass) does not keep the call past its context.
func TestExecEndsWhenAChildHoldsStdout(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cass")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\necho '{\"hits\":[]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := WaitDelay
	WaitDelay = 200 * time.Millisecond
	defer func() { WaitDelay = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := (Client{Run: Exec(bin)}).Search(ctx, Search{Query: "q"}); err == nil {
		t.Fatal("a killed cass returned no error")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the call took %v after its context ended", took)
	}
}

// The E06 kickoff (nhm-vbx): as one query cass needs every word and
// finds nothing; its keywords, any of them, find the earlier session.
func TestKeywordQuery(t *testing.T) {
	task := "You are a new agent on the e06 plan. Read AGENTS.md, then pick the e06 plan's next bead; reply slow."
	if got, want := KeywordQuery(task), "agent OR e06 OR plan OR agents OR then OR pick OR next OR bead OR reply OR slow"; got != want {
		t.Errorf("KeywordQuery = %q, want %q", got, want)
	}
	if got := KeywordQuery("fix the bug in it"); got != "" {
		t.Errorf("only stop words = %q, want none", got)
	}
}

func TestSearchArgsAndHits(t *testing.T) {
	s := &script{outs: []string{searchAnswer}}
	c := Client{Run: s.run, DataDir: "/d"}
	hits, err := c.Search(context.Background(), Search{Query: "--help q", Workspace: "/p", Limit: 5, Days: 7, MaxContent: 1024})
	if err != nil {
		t.Fatal(err)
	}
	// The query goes after "--": a prompt that starts with "--" is no flag.
	want := []string{"search", "--robot", "--limit=5", "--workspace=/p", "--days=7",
		"--max-content-length=1024", "--data-dir=/d", "--", "--help q"}
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
	if want := []string{"index", "--watch-once=" + path, "--json", "--idempotency-key=b1-abc"}; !reflect.DeepEqual(s.got[0], want) {
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

// failing answers each call with the next output and exit error.
type failing struct {
	outs []string
	errs []error
}

func (f *failing) run(_ context.Context, _ ...string) ([]byte, error) {
	out, err := f.outs[0], f.errs[0]
	f.outs, f.errs = f.outs[1:], f.errs[1:]
	return []byte(out), err
}

// cass 0.10 exits 7 with kind index-busy while another run holds its lock,
// as ACFS's `cass index --watch` always does (acfs-9hkh.92). That run
// indexes the original itself: the session is queued, not a failure.
func TestIndexSessionLeavesTheSessionToTheRunningIndexer(t *testing.T) {
	const path = "/h/.claude/projects/-p/abc.jsonl"
	busy := `{"success":false,"error":"another 'cass index' run is already active for data dir /h/.local/share/coding-agent-search (pid 9, kind lexical_refresh, phase watch)","code":7,"kind":"index-busy","retryable":true}`
	exit7 := errors.New("cass index: exit status 7: ")
	f := &failing{outs: []string{busy, `{"sessions":[{"path":"/other.jsonl"}]}`}, errs: []error{exit7, nil}}
	state, err := Client{Run: f.run}.IndexSession(context.Background(), path, "/p", "b1-abc")
	if err != nil || state != Queued {
		t.Fatalf("state = %q, %v; want %q", state, err, Queued)
	}
	// The watcher indexed it already: indexed.
	f = &failing{outs: []string{busy, `{"sessions":[{"path":"` + path + `"}]}`}, errs: []error{exit7, nil}}
	state, err = Client{Run: f.run}.IndexSession(context.Background(), path, "/p", "b1-abc")
	if err != nil || state != Indexed {
		t.Fatalf("state = %q, %v; want %q", state, err, Indexed)
	}
	// Any other refusal is an error that says what cass said.
	other := `{"success":false,"error":"no such data dir","code":4,"kind":"io"}`
	f = &failing{outs: []string{other}, errs: []error{errors.New("cass index: exit status 4: ")}}
	if _, err := (Client{Run: f.run}).IndexSession(context.Background(), path, "/p", ""); err == nil ||
		!strings.Contains(err.Error(), "no such data dir (io)") {
		t.Fatalf("err = %v", err)
	}
}
