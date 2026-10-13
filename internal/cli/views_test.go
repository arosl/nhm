package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/views"
)

// tableDump renders every row of every table of the instance's store.
func tableDump(t *testing.T, dir string) string {
	t.Helper()
	sdb, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	db := sdb.SQL()
	ctx := context.Background()
	var tables []string
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, tbl := range tables {
		r, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM %q ORDER BY 1`, tbl))
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for r.Next() {
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "[%s]", tbl)
			for _, v := range vals {
				fmt.Fprintf(&b, " %q", v)
			}
			b.WriteByte('\n')
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return b.String()
}

// The views' loaders run the read-only verbs in process for the popup's
// server, all at once (the race detector watches), and leave the store as
// it was.
func TestViewsEnvLoadsAndChangesNothing(t *testing.T) {
	s, inst := cliInstance(t)
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/") // no Agent Mail: doctor warns
	t.Setenv("HOME", t.TempDir())                         // doctor's guard checks read no real hooks
	// A known pool, whose rows nhm quota would store.
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	codexLimits(t, codexHome)
	env := viewsEnv(ui.Context{SocketPath: s.SocketPath})
	if env.RunsDir != filepath.Join(inst.Dir, store.RunsDir) {
		t.Errorf("RunsDir = %q", env.RunsDir)
	}
	before := tableDump(t, inst.Dir)

	type answer struct {
		text string
		err  error
	}
	loaders := map[string]views.Text{
		"health": env.Health, "capabilities": env.Capabilities, "version": env.Version,
		"compatibility": env.Compatibility, "recover": env.Recover, "feedback": env.Feedback,
		"doctor": func(ctx context.Context) (string, error) {
			checks, err := env.Doctor(ctx)
			var b strings.Builder
			for _, c := range checks {
				fmt.Fprintf(&b, "%s %s: %s\n", c.Result, c.Name, c.Detail)
			}
			return b.String(), err
		},
		"run status": func(ctx context.Context) (string, error) {
			return env.RunStatus(ctx, "ens-20261013T030000-aaaaaa")
		},
		"status": env.Status, "attention": env.Attention, "usage": env.Usage,
	}
	got := map[string]answer{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, fn := range loaders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			text, err := fn(context.Background())
			mu.Lock()
			got[name] = answer{text, err}
			mu.Unlock()
		}()
	}
	wg.Wait()

	for name, want := range map[string]string{
		"capabilities":  "herdr protocol 22, ntm 9d5ed977\n\nyes  --robot-status\n",
		"version":       "nhm ",
		"compatibility": "implemented",
		"doctor":        "ok herdr: herdr 0.9.3, protocol 22",
	} {
		if a := got[name]; a.err != nil || !strings.Contains(a.text, want) {
			t.Errorf("%s = %q, %v; want %q in it", name, a.text, a.err, want)
		}
	}
	if a := got["capabilities"]; !strings.Contains(a.text, "\nno   resume\n     waived: ") {
		t.Errorf("capabilities without a no: %q", a.text)
	}
	if a := got["doctor"]; !strings.Contains(a.text, "warn agent mail") {
		t.Errorf("doctor without Agent Mail: %q", a.text)
	}
	for _, name := range []string{"recover", "feedback"} {
		if a := got[name]; a.err != nil {
			t.Errorf("%s: %v (%q)", name, a.err, a.text)
		}
	}
	if a := got["health"]; a.text == "" && a.err == nil {
		t.Error("health gave neither text nor an error")
	}
	if a := got["run status"]; a.err == nil || !strings.Contains(a.err.Error(), "no run") {
		t.Errorf("status of a missing run = %q, %v", a.text, a.err)
	}
	for _, w := range [][2]string{{"status", "icyknoll"}, {"usage", "limits (a dry run: nothing is recorded)"}, {"usage", "codex (pro)"}} {
		if a := got[w[0]]; !strings.Contains(a.text, w[1]) {
			t.Errorf("%s = %q, %v; want %q in it", w[0], a.text, a.err, w[1])
		}
	}
	if after := tableDump(t, inst.Dir); after != before {
		t.Errorf("the views' loaders changed the store:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// limits --dry-run records no seen lines.
	if _, err := os.Stat(filepath.Join(inst.Dir, "limits-seen.json")); err == nil {
		t.Error("the usage view wrote limits-seen.json")
	}
}

func TestVerbText(t *testing.T) {
	verb := func(code int, out, errOut string) func([]string, io.Writer, io.Writer) int {
		return func(_ []string, stdout, stderr io.Writer) int {
			fmt.Fprint(stdout, out)
			fmt.Fprint(stderr, errOut)
			return code
		}
	}
	for _, tc := range []struct {
		code             int
		out, errOut      string
		wantText, errSub string
	}{
		{exitOK, "a\n", "", "a\n", ""},
		{exitOK, "a\n", "nhm health: warning: mail is stale\n", "a\n\nnhm health: warning: mail is stale\n", ""},
		{exitError, "overall: critical\n", "nhm health: overall status is critical\nmore\n", "overall: critical\n", "overall status is critical"},
		{exitRefused, "", "", "", "exit 2"},
	} {
		text, err := verbText(verb(tc.code, tc.out, tc.errOut))(context.Background())
		if text != tc.wantText || (tc.errSub == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), tc.errSub) ||
			err != nil && strings.Contains(err.Error(), "more") {
			t.Errorf("verbText(%d, %q, %q) = %q, %v", tc.code, tc.out, tc.errOut, text, err)
		}
	}
}

// agent.read reads a bound target's pane only while its binding holds the
// target's generation; a binding that moved is read where it is now.
// agent.inbox always keeps the mail unread.
func TestViewsEnvReadsAFrozenTarget(t *testing.T) {
	s, inst := cliInstance(t)
	s.Handle("pane.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.PaneID, "tab_id": "w1:t1", "workspace_id": "w1",
			"source": "recent", "format": "text", "revision": 1, "text": "screen of " + p.PaneID + "\n", "truncated": false}}, nil
	})
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: "/p",
		ProjectID: "proj-x", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
			PaneID: "w1:p1", HerdrAlias: "icyknoll", Kind: "claude"}})
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	env := viewsEnv(ui.Context{SocketPath: s.SocketPath})
	ctx := context.Background()

	sc, err := env.Read(ctx, ui.Target{BindingID: b.ID, Generation: b.Generation, PaneID: "w1:p1", Kind: "claude"})
	if err != nil || sc.Name != "IcyKnoll" || sc.Pane != "w1:p1" || sc.Text != "screen of w1:p1\n" {
		t.Fatalf("read = %+v, %v", sc, err)
	}
	// Another generation: the agent the target named is gone.
	if sc, err := env.Read(ctx, ui.Target{BindingID: b.ID, Generation: b.Generation + 1, PaneID: "w1:p1"}); err == nil ||
		!strings.Contains(err.Error(), "the agent you picked is gone") || sc.Text != "" {
		t.Errorf("read of another generation = %+v, %v", sc, err)
	}
	if _, err := env.Read(ctx, ui.Target{BindingID: "bnd-none", Generation: 1}); err == nil || !strings.Contains(err.Error(), "no binding") {
		t.Errorf("read of a missing binding: %v", err)
	}
	// An unbound target reads the pane it names.
	if sc, err := env.Read(ctx, ui.Target{PaneID: "w1:p2", Kind: "claude"}); err != nil || sc.Text != "screen of w1:p2\n" {
		t.Errorf("read of an unbound pane = %+v, %v", sc, err)
	}
	if _, err := env.Inbox(ctx, ui.Target{PaneID: "w1:p2"}); err == nil || !strings.Contains(err.Error(), "no Agent Mail inbox") {
		t.Errorf("inbox of an unbound pane: %v", err)
	}
	if args := inboxArgs(b); !slices.Contains(args, "--keep-unread") || !slices.Contains(args, "IcyKnoll") || args[0] != "inbox" {
		t.Errorf("inbox args = %v", args)
	}
}
