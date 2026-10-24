package send

import (
	"context"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/privacy"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/cass"
)

// ntm skips the prompt history, silently, for a session in privacy mode
// (nhm-2p6.4.16): a send that reaches an agent of a private workspace
// still goes out, and writes no history line; --allow-persist lets it.
func TestSendSkipsHistoryInPrivacyMode(t *testing.T) {
	prev := privacy.GetDefaultManager()
	m := privacy.New(privacy.DefaultConfig())
	privacy.SetDefaultManager(m)
	t.Cleanup(func() { privacy.SetDefaultManager(prev) })
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	m.RegisterSession("w1", true, false)
	res, err := Send(ctx, w.env, base(Request{Message: "Fix it.", OperationID: "op-p", WhenAny: true}))
	if err != nil || len(res.Targets) != 1 || res.Targets[0].Status != StatusSubmitted || len(w.hist) != 0 {
		t.Fatalf("private = %+v %v, history %d", res.Targets, err, len(w.hist))
	}
	m.RegisterSession("w1", true, true)
	if _, err := Send(ctx, w.env, base(Request{Message: "Fix it again.", OperationID: "op-q", WhenAny: true})); err != nil ||
		len(w.hist) != 1 || w.hist[0].OperationID != "op-q" {
		t.Fatalf("--allow-persist = %v, history %+v", err, w.hist)
	}
}

// A private session's injections keep no text: no query, which is made of
// the prompt, and no labels; the items' keys stay, so the next send still
// knows what the agent was given (nhm-2p6.4.16).
func TestPrivateInjectionsKeepNoText(t *testing.T) {
	prev := privacy.GetDefaultManager()
	m := privacy.New(privacy.DefaultConfig())
	privacy.SetDefaultManager(m)
	t.Cleanup(func() { privacy.SetDefaultManager(prev) })
	ctx := context.Background()
	rows := func(w *world, op string) (texts []string) {
		t.Helper()
		r, err := w.db.SQL().QueryContext(ctx, `SELECT COALESCE(query, '') || ' ' || items_json FROM injections WHERE operation_id = ?`, op)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for r.Next() {
			var s string
			if err := r.Scan(&s); err != nil {
				t.Fatal(err)
			}
			texts = append(texts, s)
		}
		return texts
	}
	send := func(ws string, private bool) (*world, []string) {
		t.Helper()
		w := newWorld(t)
		w.env.CM = &memCM{rules: twoRules}
		w.env.CASS = &memCASS{hits: []cass.Hit{{SourcePath: "/s/a.jsonl", LineNumber: 7, Content: "The login bug was a CSRF check.", Agent: "claude"}}}
		w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
		m.RegisterSession("w1", private, false)
		req := memoryReq("op-" + ws)
		req.Enrich.CASS = enrich.BestEffort
		if _, err := Send(ctx, w.env, req); err != nil {
			t.Fatal(err)
		}
		return w, rows(w, "op-"+ws)
	}
	// An open session keeps the query and the CASS hit's label.
	if _, got := send("open", false); len(got) != 2 || !strings.Contains(strings.Join(got, "\n"), "login") ||
		!strings.Contains(strings.Join(got, "\n"), `"label":"claude`) {
		t.Fatalf("open = %q", got)
	}
	w, got := send("private", true)
	if all := strings.Join(got, "\n"); len(got) != 2 || strings.Contains(all, "login") || strings.Contains(all, "label") ||
		!strings.Contains(all, `"id":"b-1"`) {
		t.Fatalf("private = %q", got)
	}
	w.fake.add("w1:p1", "claude", "icyknoll", herdr.StatusIdle, 20)
	res, err := Send(ctx, w.env, memoryReq("op-next"))
	if err != nil || res.Injections[0].Items[0].Status != enrich.Suppressed {
		t.Fatalf("the next send = %+v %v", res.Injections, err)
	}
}
