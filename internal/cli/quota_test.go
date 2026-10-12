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

// quotaInstance is swarmInstance plus agent.list, a Codex session log at
// 93 %, and an agent screen that shows a Claude limit.
func quotaInstance(t *testing.T) *herdrtest.Server {
	t.Helper()
	s := swarmInstance(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
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

func TestQuotaRecordClaude(t *testing.T) {
	quotaInstance(t)
	in := `{"model":{"display_name":"Opus 5.5"},"rate_limits":{"five_hour":{"used_percentage":95,"resets_at":` +
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
	if code != 0 || out != "Opus 5.5 · 5h 95%\n" {
		t.Fatalf("record-claude = %d %q", code, out)
	}
	if code, _, errOut := runVerb(runQuota, "check", "claude"); code != 1 || !strings.Contains(errOut, "95% used") {
		t.Fatalf("check after record = %d %s", code, errOut)
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
