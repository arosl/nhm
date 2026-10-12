package send

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/templates"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/cass"
	"github.com/arosl/nhm/internal/integrations/cm"
)

type memCM struct {
	mu    sync.Mutex
	rules []cm.Rule
	err   error
	calls int
}

func (f *memCM) Context(context.Context, string, string) (cm.Context, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return cm.Context{Rules: f.rules}, f.err
}
func (f *memCM) Mark(context.Context, string, bool, string) error                { return nil }
func (f *memCM) Outcome(context.Context, string, []string, string, string) error { return nil }
func (f *memCM) OutcomeApply(context.Context, string) error                      { return nil }

type memCASS struct {
	hits     []cass.Hit
	searches int
}

func (f *memCASS) Search(context.Context, cass.Search) ([]cass.Hit, error) {
	f.searches++
	return f.hits, nil
}
func (f *memCASS) IndexSession(context.Context, string, string, string) (string, error) {
	return cass.Indexed, nil
}

var twoRules = []cm.Rule{{ID: "b-1", Content: "Run the login tests."}, {ID: "b-2", Content: "Keep the CSRF check."}}

func memoryReq(op string) Request {
	return base(Request{Message: "Fix the login bug.", OperationID: op, Caller: "w1:p9",
		Enrich: Enrichment{CM: enrich.BestEffort}})
}

func TestSendWithMemoryFramesOncePerContext(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	f := &memCM{rules: twoRules}
	w.env.CM = f
	b := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)

	res, err := Send(ctx, w.env, memoryReq("op-1"))
	if err != nil {
		t.Fatal(err)
	}
	text := w.fake.prompts[0].text
	if !strings.Contains(text, "[nhm context:") || !strings.Contains(text, "[b-1] Run the login tests.") ||
		!strings.HasSuffix(text, "\n\nFix the login bug.") {
		t.Fatalf("prompt = %q", text)
	}
	if !res.MemoryInjection.Enabled || len(res.Injections) != 1 || res.Injections[0].Items[0].Status != enrich.Included {
		t.Fatalf("res = %+v", res)
	}
	if res.Targets[0].PayloadSHA256 != mustDigest(text) {
		t.Fatal("the receipt's hash is not of the framed payload")
	}
	// The stored outcome replays the injection.
	stored, err := Stored(ctx, w.db, "op-1")
	if err != nil || !stored.MemoryInjection.Enabled || len(stored.Injections) != 1 || len(stored.Injections[0].Items) != 2 {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	out := SendOutput(stored, "json", time.Now())
	if out.MemoryInjection == nil || len(out.MemoryInjection.RulesInjected) != 2 || out.MemoryInjection.TokensAdded == 0 {
		t.Fatalf("robot = %+v", out.MemoryInjection)
	}
	if out.Redaction.Mode != "redact" || out.Redaction.Findings != 0 {
		t.Fatalf("redaction = %+v", out.Redaction)
	}

	// The same context has both rules: no block, an exposure all the same.
	w.fake.add("w1:p1", "claude", "icyknoll", herdr.StatusIdle, 20)
	res, err = Send(ctx, w.env, memoryReq("op-2"))
	if err != nil {
		t.Fatal(err)
	}
	if text := w.fake.prompts[1].text; text != "Fix the login bug." {
		t.Fatalf("second prompt = %q", text)
	}
	if res.Injections[0].Items[0].Status != enrich.Suppressed {
		t.Fatalf("second = %+v", res.Injections)
	}

	// A compaction starts a new context: the rules go again.
	at := time.Now()
	if _, err := w.env.Bindings.Continue(ctx, b.ID, b.Generation, bindings.Continuation{CompactedAt: &at}); err != nil {
		t.Fatal(err)
	}
	w.fake.add("w1:p1", "claude", "icyknoll", herdr.StatusIdle, 30)
	if _, err := Send(ctx, w.env, memoryReq("op-3")); err != nil {
		t.Fatal(err)
	}
	if text := w.fake.prompts[2].text; !strings.Contains(text, "[b-1]") {
		t.Fatalf("after compaction = %q", text)
	}
}

func TestRobotReportsRedactions(t *testing.T) {
	w := newWorld(t)
	secret := "gh" + "p_" + strings.Repeat("Z9y8", 9)
	w.env.CM = &memCM{rules: []cm.Rule{{ID: "b-9", Content: "Clone with " + secret}}}
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	res, err := Send(context.Background(), w.env, memoryReq("op-r"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.fake.prompts[0].text, secret) || !strings.Contains(w.fake.prompts[0].text, "[REDACTED:GITHUB_TOKEN]") {
		t.Fatalf("prompt = %q", w.fake.prompts[0].text)
	}
	if out := SendOutput(res, "json", time.Now()); out.Redaction.Mode != "redact" || out.Redaction.Findings != 1 {
		t.Fatalf("redaction = %+v", out.Redaction)
	}
	plain := base(Request{Message: "hi", DryRun: true})
	if res, _ := Send(context.Background(), w.env, plain); SendOutput(res, "json", time.Now()).Redaction.Mode != "off" {
		t.Fatal("a send without enrichment reports redaction")
	}
}

func mustDigest(s string) string {
	d, _ := Digest(s)
	return d
}

func TestRequiredFailureRefusesBeforeTheClaim(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.env.CM = &memCM{err: errors.New("cm down")}
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	req := memoryReq("op-1")
	req.Enrich.CM = enrich.Required
	if _, err := Send(ctx, w.env, req); !errors.Is(err, enrich.ErrRequired) {
		t.Fatalf("err = %v", err)
	}
	if w.fake.promptCount() != 0 {
		t.Fatal("a prompt went out")
	}
	if _, err := w.db.Operation(ctx, "op-1"); err == nil {
		t.Fatal("the operation was claimed")
	}
	// Best-effort: the prompt goes out as typed, with a warning.
	res, err := Send(ctx, w.env, memoryReq("op-2"))
	if err != nil || w.fake.prompts[0].text != "Fix the login bug." || len(res.Warnings) != 1 {
		t.Fatalf("res = %+v, %v, prompts %+v", res, err, w.fake.prompts)
	}
}

func TestPrecedenceFlagTemplateDefault(t *testing.T) {
	tmpl, err := templates.Parse("---\nname: kick\nenrichment:\n  cm: required\n---\nDo the bead.\n")
	if err != nil || tmpl.Enrichment["cm"] != "required" {
		t.Fatalf("frontmatter = %+v, %v", tmpl, err)
	}
	cmM, cassM, err := Enrichment{DefaultCM: enrich.BestEffort, DefaultCASS: enrich.BestEffort}.resolve(tmpl)
	if err != nil || cmM != enrich.Required || cassM != enrich.BestEffort {
		t.Fatalf("template over default: %s %s %v", cmM, cassM, err)
	}
	cmM, _, _ = Enrichment{CM: enrich.Off, DefaultCM: enrich.BestEffort}.resolve(tmpl)
	if cmM != enrich.Off {
		t.Fatalf("flag over template: %s", cmM)
	}
	if _, _, err := (Enrichment{}).resolve(&templates.Template{Enrichment: map[string]string{"cass": "maybe"}}); err == nil {
		t.Fatal("a bad template mode was accepted")
	}
}

func TestCassCheckWarnsAndDryRunShowsTheBlock(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	cs := &memCASS{hits: []cass.Hit{{SourcePath: "/old.jsonl", Title: "Fix the login bug", Agent: "codex"}}}
	w.env.CM, w.env.CASS = &memCM{rules: twoRules}, cs
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	req := memoryReq("")
	req.DryRun = true
	req.Enrich.Check = true
	res, err := Send(ctx, w.env, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Duplicates) != 1 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "cass-check") {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.Rendered[0], "[b-2]") {
		t.Fatalf("rendered = %q", res.Rendered[0])
	}
	// --force skips the check.
	req.Force = true
	searches := cs.searches
	if _, err := Send(ctx, w.env, req); err != nil || cs.searches != searches {
		t.Fatalf("force ran the check: %v", err)
	}
	// No enrichment and no check: no call at all.
	plain := base(Request{Message: "hi", DryRun: true})
	if _, err := Send(ctx, w.env, plain); err != nil || cs.searches != searches {
		t.Fatalf("plain send called cass: %v", err)
	}
}
