package snapshot

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/guards"
)

// dumpStore renders every row of every table, in a fixed order.
func dumpStore(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
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
		r, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM %q ORDER BY rowid`, tbl))
		if err != nil {
			// A WITHOUT ROWID table: its primary key orders it.
			if r, err = db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM %q ORDER BY 1`, tbl)); err != nil {
				t.Fatal(err)
			}
		}
		cols, err := r.Columns()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "[%s] %v\n", tbl, cols)
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for r.Next() {
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				if bs, ok := v.([]byte); ok {
					v = string(bs)
				}
				fmt.Fprintf(&b, "%v\t", v)
			}
			b.WriteString("\n")
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return b.String()
}

// countingMail counts inbox peeks; the read model's Mail interface has
// no call that marks mail read.
type countingMail struct {
	fakeMail
	calls atomic.Int64
}

func (c *countingMail) FetchInbox(ctx context.Context, project, agent string, limit int) ([]agentmail.Message, error) {
	c.calls.Add(1)
	return c.fakeMail.FetchInbox(ctx, project, agent, limit)
}

// The board's refreshes are reads: N builds leave the store, the limit
// notices file and the mail read state as they were (plan-v4 section 4).
func TestBuildIsReadOnly(t *testing.T) {
	db, h := setup(t)
	limits := filepath.Join(t.TempDir(), "limits-seen.json")
	seen := []byte(`{"lines":{"w1:p1":"You've hit your limit"}}`)
	if err := os.WriteFile(limits, seen, 0o600); err != nil {
		t.Fatal(err)
	}
	mail := &countingMail{fakeMail: fakeMail{inbox: map[string][]agentmail.Message{
		"IcyKnoll": {{ID: 7, Subject: "hi", From: "BlueLake", CreatedTS: t0.Format("2006-01-02T15:04:05Z")}}}}}
	deps := Deps{Herdr: h, DB: db, Mail: mail, LimitsFile: limits,
		Guards: func(string, string) []guards.Result { return nil },
		Beads: func(context.Context, string) (map[string][]string, error) {
			return map[string][]string{"IcyKnoll": {"x-1"}}, nil
		}}
	before := dumpStore(t, db.SQL())
	if !strings.Contains(before, "IcyKnoll") {
		t.Fatalf("the dump misses the bindings:\n%s", before)
	}
	const n = 25
	for range n {
		m := Build(context.Background(), deps, Options{})
		Snapshot(m, Attention(m), "json", t0)
	}
	if after := dumpStore(t, db.SQL()); after != before {
		t.Fatalf("the store changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if got, err := os.ReadFile(limits); err != nil || string(got) != string(seen) {
		t.Fatalf("limits file = %q %v", got, err)
	}
	if mail.calls.Load() == 0 {
		t.Fatal("the mail source was never read; the test proves nothing about it")
	}
}
