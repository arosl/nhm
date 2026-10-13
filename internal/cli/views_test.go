package cli

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	t.Setenv("CODEX_HOME", t.TempDir())
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
	if after := tableDump(t, inst.Dir); after != before {
		t.Errorf("the views' loaders changed the store:\nbefore:\n%s\nafter:\n%s", before, after)
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
