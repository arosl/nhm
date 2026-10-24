package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// codexLimits writes a Codex session log into codexHome whose rate_limits
// record, a minute old, is at 93 % for five hours and 20 % for the week.
func codexLimits(t *testing.T, codexHome string) {
	t.Helper()
	now := time.Now().UTC()
	line := `{"timestamp":"` + now.Add(-time.Minute).Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"token_count","rate_limits":{"plan_type":"pro","primary":{"used_percent":93.0,"window_minutes":300,"resets_at":` +
		strconv.FormatInt(now.Add(time.Hour).Unix(), 10) + `},"secondary":{"used_percent":20.0,"window_minutes":10080,"resets_at":` +
		strconv.FormatInt(now.Add(72*time.Hour).Unix(), 10) + `}}}}`
	path := filepath.Join(codexHome, "sessions", "2026", "rollout-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// quotaInstance is swarmInstance plus agent.list, a Codex session log at
// 93 %, and an agent screen that shows a Claude limit.
func quotaInstance(t *testing.T) *herdrtest.Server {
	t.Helper()
	s := swarmInstance(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	codexLimits(t, codexHome)
	s.Handle("agent.list", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "agent_list", "agents": []any{
			map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "t1", "agent": "claude", "agent_status": "idle", "focused": false, "revision": 1},
			map[string]any{"pane_id": "w1:p2", "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "t2", "agent": "codex", "agent_status": "blocked", "focused": false, "revision": 1},
		}}, nil
	})
	s.Handle("agent.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct{ Target string }
		_ = json.Unmarshal(r.Params, &p)
		text := "all fine\n❯ \n"
		if p.Target == "w1:p1" {
			text = "Working\n  ⎿  You've hit your session limit · resets 3pm\n❯ \n"
		}
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.Target, "tab_id": "w1:t1", "workspace_id": "w1",
			"source": "visible", "format": "text", "revision": 1, "text": text, "truncated": false}}, nil
	})
	return s
}

func TestQuotaVerb(t *testing.T) {
	s := quotaInstance(t)
	code, out, errOut := runVerb(runQuota, "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "codex (pro)") || !strings.Contains(out, "93%") || !strings.Contains(out, "claude: no record") {
		t.Fatalf("quota = %d\n%s%s", code, out, errOut)
	}
	code, out, _ = runVerb(runQuota, "--json", "--socket", s.SocketPath)
	pools := envelopeOf(t, out)["result"].(map[string]any)["pools"].([]any)
	codex := pools[1].(map[string]any)
	if code != 0 || codex["agents"].(float64) != 1 || codex["pool_id"] != "codex" {
		t.Fatalf("json = %d %v", code, codex)
	}
	// The enrolled instance stored the codex rows for status and attention.
	root, _ := store.StateRoot()
	inst, err := store.Lookup(root, s.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM quota WHERE pool_id = 'codex'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("stored rows = %d %v", n, err)
	}
}

func TestQuotaCheck(t *testing.T) {
	quotaInstance(t)
	code, _, errOut := runVerb(runQuota, "check", "codex")
	if code != 1 || !strings.Contains(errOut, "93% used (limit 90%)") {
		t.Fatalf("check codex = %d %s", code, errOut)
	}
	if code, _, _ := runVerb(runQuota, "check", "codex", "--limit", "95"); code != 0 {
		t.Fatalf("check at 95 = %d", code)
	}
	if code, _, _ := runVerb(runQuota, "check", "agy"); code != 0 {
		t.Fatalf("unknown pool = %d", code)
	}
	if code, _, _ := runVerb(runQuota, "check"); code != 2 {
		t.Fatalf("no kind = %d", code)
	}
	if code, _, _ := runVerb(runQuota, "check", "x", "--limit", "101"); code != 2 {
		t.Fatalf("bad limit = %d", code)
	}
}

// recordClaude runs nhm quota record-claude, Claude Code's statusLine
// command, with Claude's five-hour window at pct on stdin.
func recordClaude(t *testing.T, pct int) (int, string) {
	t.Helper()
	in := `{"model":{"display_name":"Opus 5.5"},"rate_limits":{"five_hour":{"used_percentage":` + strconv.Itoa(pct) + `,"resets_at":` +
		strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + `}}}`
	stdin := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(stdin, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(stdin)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	code, out, _ := runVerb(runQuota, "record-claude")
	return code, out
}

func TestQuotaRecordClaude(t *testing.T) {
	quotaInstance(t)
	code, out := recordClaude(t, 95)
	if code != 0 || out != "Opus 5.5 · 5h 95%\n" {
		t.Fatalf("record-claude = %d %q", code, out)
	}
	if code, _, errOut := runVerb(runQuota, "check", "claude"); code != 1 || !strings.Contains(errOut, "95% used") {
		t.Fatalf("check after record = %d %s", code, errOut)
	}
}

// ACFS E13 (nhm-kyt): at 92 % on caam's profile e13a a Claude spawn is
// refused; after `caam activate claude e13b` that record is e13a's, so
// the pool is unknown (plan-v8 2.9), and the dry run is admitted with a
// warning for the operator and refused for a robot (2.2).
func TestQuotaCaamSwitchMakesThePoolUnknown(t *testing.T) {
	s, _, _ := newSendInstance(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	bin := t.TempDir()
	t.Setenv("PATH", bin) // a fake caam, and no dcg: --allow-unguarded
	status := filepath.Join(bin, "status.json")
	// Shell builtins only: PATH holds nothing else.
	writeAll(t, filepath.Join(bin, "caam"), "#!/bin/sh\nIFS= read -r line <'"+status+"'\nprintf '%s\\n' \"$line\"\n")
	if err := os.Chmod(filepath.Join(bin, "caam"), 0o755); err != nil {
		t.Fatal(err)
	}
	activate := func(profile string) {
		writeAll(t, status, `{"tools":[{"tool":"claude","logged_in":true,"active_profile":"`+profile+`"}]}`)
	}
	project := t.TempDir()
	dryRun := func(extra ...string) (int, map[string]any) {
		t.Helper()
		args := append([]string{"spawn", "swarm", "--cc=1", "--project", project, "--prompt", "say hi", "--allow-unguarded",
			"--dry-run", "--json", "--socket", s.SocketPath}, extra...)
		code, out, errOut := run(args...)
		ad, _ := spawnNHMResult(t, out)["admission"].(map[string]any)
		if ad == nil || ad["decision"] == "" {
			t.Fatalf("%v = %d %s %s", extra, code, out, errOut)
		}
		return code, ad
	}

	activate("e13a")
	if code, _ := recordClaude(t, 92); code != 0 {
		t.Fatalf("record-claude = %d", code)
	}
	// spawn's admission refusal exits 1, ntm's code (nhm-2p6.4.5).
	if code, ad := dryRun(); code != exitError || ad["decision"] != "refuse" || !strings.Contains(ad["reason"].(string), "92% used") {
		t.Fatalf("on e13a = %d %v", code, ad)
	}

	activate("e13b")
	code, out, _ := runVerb(runQuota, "--json", "--socket", s.SocketPath)
	claude := envelopeOf(t, out)["result"].(map[string]any)["pools"].([]any)[0].(map[string]any)
	if _, has := claude["observed_at"]; code != 0 || has || claude["superseded"] != true ||
		claude["credential"].(map[string]any)["profile"] != "e13b" {
		t.Fatalf("quota after the switch = %d %v", code, claude)
	}
	code, ad := dryRun()
	warnings, _ := json.Marshal(ad["warnings"])
	if code != 0 || ad["decision"] != "admit" || !strings.Contains(string(warnings),
		"claude quota unknown (the newest record is of profile e13a, not of the active profile e13b: a credential switch invalidates it): admitted") {
		t.Fatalf("on e13b = %d %v", code, ad)
	}
	if code, ad := dryRun("--require-observations"); code != exitError || ad["decision"] != "refuse" ||
		!strings.Contains(ad["reason"].(string), "a robot call needs an observation") {
		t.Fatalf("a robot on e13b = %d %v", code, ad)
	}
}

func TestLimitsVerb(t *testing.T) {
	s := quotaInstance(t)
	code, out, errOut := runVerb(runLimits, "--dry-run", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "IcyKnoll (claude, w1:p1) would be reported") || strings.Contains(out, "w1:p2") {
		t.Fatalf("dry run = %d\n%s%s", code, out, errOut)
	}
	var calls []map[string]any
	mail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &call)
		calls = append(calls, call.Params.Arguments)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`))
	}))
	defer mail.Close()
	t.Setenv("AGENT_MAIL_URL", mail.URL)
	code, out, errOut = runVerb(runLimits, "--mail-from", "SageOak", "--json", "--socket", s.SocketPath)
	reports := envelopeOf(t, out)["result"].(map[string]any)["reports"].([]any)
	if code != 0 || len(reports) != 1 || reports[0].(map[string]any)["mailed"] != true {
		t.Fatalf("pass = %d %v %s", code, reports, errOut)
	}
	if len(calls) != 1 || calls[0]["sender_name"] != "SageOak" || calls[0]["project_key"] != "/p" ||
		!strings.Contains(calls[0]["subject"].(string), "IcyKnoll stopped at a usage or rate limit") {
		t.Fatalf("mail = %v", calls)
	}
	// The same line on the next pass is not reported again.
	code, out, _ = runVerb(runLimits, "--mail-from", "SageOak", "--json", "--socket", s.SocketPath)
	reports = envelopeOf(t, out)["result"].(map[string]any)["reports"].([]any)
	if code != 0 || len(reports) != 0 || len(calls) != 1 {
		t.Fatalf("second pass = %d %v, %d mails", code, reports, len(calls))
	}
	if code, _, _ := runVerb(runLimits, "--mail-to", "X", "--socket", s.SocketPath); code != 2 {
		t.Fatalf("--mail-to without --mail-from = %d", code)
	}
}

// shortScreen is a new Claude's screen at a limit (ACFS E13, nhm-1q7): the
// limit reply at the top, then a gap of blank lines above the prompt box,
// so the screen's last 15 lines show no limit.
func shortScreen() string {
	rule := strings.Repeat("─", 80)
	rows := []string{"❯ Go on.", "  ⎿  You've hit your limit · resets 5pm (UTC)", "", "✻ Churned for 1s · done 3:40 PM"}
	rows = append(rows, make([]string, 14)...)
	rows = append(rows, rule, "❯ ", rule, "  ⏵⏵ bypass permissions on (shift+tab to cycle)")
	return strings.Join(rows, "\n") + "\n"
}

// visibleRead answers agent.read with the screen as herdr does: a lines
// value keeps only the screen's last lines.
func visibleRead(r herdrtest.Request, screen string) map[string]any {
	var p struct {
		Target string
		Lines  *int
	}
	_ = json.Unmarshal(r.Params, &p)
	if p.Lines != nil {
		rows := strings.Split(strings.TrimSuffix(screen, "\n"), "\n")
		screen = strings.Join(rows[max(0, len(rows)-*p.Lines):], "\n") + "\n"
	}
	return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.Target, "tab_id": "w1:t1", "workspace_id": "w1",
		"source": "visible", "format": "text", "revision": 1, "text": screen, "truncated": false}}
}

// nhm limits finds a limit line above a blank gap and the prompt box: it
// reads the whole screen and counts only the lines that are not blank
// (nhm-1q7).
func TestLimitsShortScreen(t *testing.T) {
	s := quotaInstance(t)
	s.Handle("agent.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		return visibleRead(r, shortScreen()), nil
	})
	code, out, errOut := runVerb(runLimits, "--dry-run", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "IcyKnoll (claude, w1:p1) would be reported: ⎿  You've hit your limit · resets 5pm (UTC)") {
		t.Fatalf("dry run = %d\n%s%s", code, out, errOut)
	}
	// --lines still bounds the window: the limit is the 6th line from the
	// bottom that is not blank.
	if code, out, errOut := runVerb(runLimits, "--dry-run", "--lines", "5", "--socket", s.SocketPath); code != 0 || strings.Contains(out, "w1:p1") {
		t.Fatalf("--lines 5 = %d\n%s%s", code, out, errOut)
	}
}
