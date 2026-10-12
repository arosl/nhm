package quota

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/store"
)

var now = time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)

func write(t *testing.T, path, content string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func epoch(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func codexLine(ts time.Time, primaryPct float64, primaryReset time.Time, extra string) string {
	return `{"timestamp":"` + ts.Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"token_count","rate_limits":{"plan_type":"pro","primary":{"used_percent":` +
		fmtFloat(primaryPct) + `,"window_minutes":300,"resets_at":` + epoch(primaryReset) + `},"secondary":{"used_percent":20.0,"window_minutes":10080,"resets_at":` +
		epoch(now.Add(72*time.Hour)) + `}` + extra + `}}}`
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }

func TestReadCodexTakesTheNewestRecord(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions", "2026", "10", "12")
	write(t, filepath.Join(sessions, "rollout-a.jsonl"),
		codexLine(now.Add(-2*time.Hour), 40, now.Add(time.Hour), "")+"\nnot json \"rate_limits\"\n", now.Add(-2*time.Hour))
	write(t, filepath.Join(sessions, "rollout-b.jsonl"),
		`{"type":"event_msg","payload":{"type":"agent_message","message":"rate_limits are fun"}}`+"\n"+
			codexLine(now.Add(-time.Hour), 93, now.Add(2*time.Hour), ""), now.Add(-time.Hour))
	// Older than a week: ignored even with a newer timestamp inside.
	write(t, filepath.Join(sessions, "rollout-old.jsonl"), codexLine(now, 1, now.Add(time.Hour), ""), now.Add(-8*24*time.Hour))
	o, err := ReadCodex(home, now)
	if err != nil || o == nil {
		t.Fatalf("ReadCodex = %+v %v", o, err)
	}
	if *o.FiveHour.UsedPct != 93 || o.PlanType != "pro" || o.FiveHour.Reset || *o.Weekly.UsedPct != 20 || !o.ObservedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("codex = %+v %+v", o, o.FiveHour)
	}
	ok, why := Check([]Observation{*o}, "codex", 90)
	if ok || !strings.Contains(why, "93% used (limit 90%)") {
		t.Fatalf("check = %v %s", ok, why)
	}
	if ok, _ := Check([]Observation{*o}, "codex", 95); !ok {
		t.Fatal("93% refused at a 95% limit")
	}
}

func TestCodexResetAndLimitReached(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	// The window reset an hour ago; a limit record from three hours ago.
	write(t, filepath.Join(sessions, "rollout-x.jsonl"),
		codexLine(now.Add(-3*time.Hour), 100, now.Add(-time.Hour), `,"rate_limit_reached_type":"primary"`), now.Add(-3*time.Hour))
	o, err := ReadCodex(home, now)
	if err != nil {
		t.Fatal(err)
	}
	if !o.FiveHour.Reset || o.LimitReached == nil || !o.LimitReached.Stale {
		t.Fatalf("reset = %+v limit %+v", o.FiveHour, o.LimitReached)
	}
	if ok, _ := Check([]Observation{*o}, "codex", 90); !ok {
		t.Fatal("a reset window with a stale limit refused")
	}
	// A fresh limit record refuses whatever the window says.
	write(t, filepath.Join(sessions, "rollout-y.jsonl"),
		codexLine(now.Add(-10*time.Minute), 10, now.Add(time.Hour), `,"rate_limit_reached_type":"secondary"`), now.Add(-10*time.Minute))
	o, _ = ReadCodex(home, now)
	if ok, why := Check([]Observation{*o}, "codex", 90); ok || !strings.Contains(why, "limit reached: secondary") {
		t.Fatalf("fresh limit = %v %s", ok, why)
	}
}

func TestClaudeRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	nhmFile := filepath.Join(dir, "nhm", "quota", "claude.json")
	in := `{"model":{"display_name":"Opus 5.5"},"rate_limits":{"five_hour":{"used_percentage":91.5,"resets_at":` + epoch(now.Add(time.Hour)) +
		`},"seven_day":{"used_percentage":40,"resets_at":` + epoch(now.Add(96*time.Hour)) + `}}}`
	line, err := RecordClaude([]byte(in), nhmFile, now)
	if err != nil || line != "Opus 5.5 · 5h 91% · 7d 40%" {
		t.Fatalf("line = %q %v", line, err)
	}
	// An older ACFS record loses to nhm's newer one.
	acfs := filepath.Join(dir, "acfs", "state", "quota", "claude.json")
	write(t, acfs, `{"recorded_at":"2026-10-11T00:00:00Z","rate_limits":{"five_hour":{"used_percentage":5}}}`, now)
	o := ReadClaude(now.Add(time.Minute), nhmFile, acfs)
	if !o.Known() || *o.FiveHour.UsedPct != 91.5 || *o.Weekly.UsedPct != 40 || !strings.Contains(o.Source, nhmFile) {
		t.Fatalf("claude = %+v", o)
	}
	if ok, _ := Check([]Observation{*o}, "claude", 90); ok {
		t.Fatal("91.5% passed a 90% limit")
	}
	// A bad input saves nothing and still prints nothing harmful.
	if line, err := RecordClaude([]byte("garbage"), filepath.Join(dir, "x.json"), now); line != "" || err != nil {
		t.Fatalf("garbage = %q %v", line, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.json")); err == nil {
		t.Fatal("garbage was saved")
	}
	if o := ReadClaude(now, filepath.Join(dir, "none.json")); o.Known() || o.Note == "" {
		t.Fatalf("no record = %+v", o)
	}
}

func TestCollectAndUnknownPools(t *testing.T) {
	obs := Collect(Env{CodexHome: t.TempDir(), RecordFile: filepath.Join(t.TempDir(), "c.json")}, now, map[string]int{"claude": 2, "agy": 1})
	kinds := map[string]Observation{}
	for _, o := range obs {
		kinds[o.Kind] = o
	}
	if len(obs) != 4 || kinds["claude"].Agents != 2 || kinds["agy"].Agents != 1 || kinds["agy"].Known() || kinds["codex"].Note == "" {
		t.Fatalf("collect = %+v", obs)
	}
	// An unknown pool passes the check: the caller decides (2.2).
	if ok, _ := Check(obs, "agy", 90); !ok {
		t.Fatal("an unknown pool refused")
	}
}

func TestStoreWritesRows(t *testing.T) {
	inst, _, err := store.Enroll(t.TempDir(), "/run/h.sock", "default", now)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pct, reset := 50.0, now.Add(time.Hour)
	obs := []Observation{
		{Pool: "codex", Kind: "codex", ObservedAt: &now, Source: "codex session logs",
			FiveHour: &Window{UsedPct: &pct, ResetsAt: &reset}, LimitReached: &Limit{Type: "primary", ObservedAt: now}},
		{Pool: "agy", Kind: "agy"},
	}
	for i := 0; i < 2; i++ { // twice: the second run updates
		if err := Store(context.Background(), db.SQL(), obs); err != nil {
			t.Fatal(err)
		}
	}
	var n, limited int
	var used float64
	if err := db.SQL().QueryRow(`SELECT count(*), max(limited), max(used_pct) FROM quota`).Scan(&n, &limited, &used); err != nil {
		t.Fatal(err)
	}
	if n != 1 || limited != 1 || used != 50 {
		t.Fatalf("rows %d limited %d used %v", n, limited, used)
	}
}

func TestLimits(t *testing.T) {
	screen := "working...\n  ⎿  You've hit your session limit · resets 3pm (UTC)\n❯ \n"
	if got := FindLimit("claude", screen); !strings.Contains(got, "hit your session limit") {
		t.Fatalf("claude = %q", got)
	}
	if got := FindLimit("claude", "I will avoid the rate limit by batching."); got != "" {
		t.Fatalf("prose matched: %q", got)
	}
	if got := FindLimit("agy", "Error: RESOURCE_EXHAUSTED"); got == "" {
		t.Fatal("agy limit missed")
	}
	if got := FindLimit("pi", "HTTP 429 Too Many Requests"); got == "" {
		t.Fatal("generic limit missed")
	}
	s, err := LoadSeen(filepath.Join(t.TempDir(), "limits", "seen.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Update("w1:p1", "limit A") || s.Update("w1:p1", "limit A") || !s.Update("w1:p1", "limit B") {
		t.Fatal("each line once")
	}
	if s.Update("w1:p1", "") || !s.Update("w1:p1", "limit B") {
		t.Fatal("a line that left the screen is reported again when it comes back")
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := LoadSeen(s.path)
	if err != nil || again.Lines["w1:p1"] != "limit B" {
		t.Fatalf("reload = %+v %v", again, err)
	}
}
