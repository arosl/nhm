package quota

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/store"
)

// fakeCaam is a caam whose `caam status [tool] --json` prints what set
// gives it last, with an identity block as caam's own holds (an email and
// an account id, which nhm must never read). It logs its argv.
func fakeCaam(t *testing.T) (bin string, set func(claude, codex string), argv func() string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "caam")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >>'" + dir + "/argv'\ncat '" + dir + "/status.json'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	set = func(claude, codex string) {
		var tools []map[string]any
		for _, p := range []struct{ tool, profile string }{{"claude", claude}, {"codex", codex}, {"gemini", ""}} {
			tool := map[string]any{"tool": p.tool, "logged_in": true}
			if p.profile != "" {
				tool["active_profile"] = p.profile
				tool["identity"] = map[string]any{"email": p.profile + "@example.invalid", "account_id": "caam-account-" + p.profile}
			}
			tools = append(tools, tool)
		}
		data, _ := json.Marshal(map[string]any{"tools": tools})
		if err := os.WriteFile(filepath.Join(dir, "status.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	argv = func() string {
		data, _ := os.ReadFile(filepath.Join(dir, "argv"))
		return string(data)
	}
	return bin, set, argv
}

// statusLine is Claude Code's statusLine input with the five-hour window
// at pct.
func statusLine(pct float64) []byte {
	return []byte(`{"model":{"display_name":"Opus"},"rate_limits":{"five_hour":{"used_percentage":` + fmtFloat(pct) +
		`,"resets_at":` + epoch(now.Add(3*time.Hour)) + `}}}`)
}

// codexSession writes a session log as Codex does: its session_meta names
// the account that made the session (none when account is ""), then a
// token_count record at pct, written at at.
func codexSession(t *testing.T, home, name, account string, at time.Time, pct float64) {
	t.Helper()
	meta := `{"timestamp":"` + at.Format(time.RFC3339Nano) + `","type":"session_meta","payload":{"id":"s-` + name +
		`","base_instructions":{"text":"mind the rate_limits"},"creator_account_id":"` + account + `"}}`
	if account == "" {
		meta = ""
	}
	write(t, filepath.Join(home, "sessions", "2026", "10", "12", "rollout-"+name+".jsonl"),
		meta+"\n"+codexLine(at, pct, now.Add(time.Hour), "")+"\n", at)
}

func pool(t *testing.T, obs []Observation, kind string) Observation {
	t.Helper()
	for _, o := range obs {
		if o.Kind == kind {
			return o
		}
	}
	t.Fatalf("no %s pool in %+v", kind, obs)
	return Observation{}
}

// noSecrets fails when any of the secrets shows in what nhm keeps or
// prints.
func noSecrets(t *testing.T, what string, data []byte, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(strings.ToLower(string(data)), strings.ToLower(s)) {
			t.Fatalf("%s holds %q: %s", what, s, data)
		}
	}
}

var shortFingerprint = regexp.MustCompile(`^[0-9a-f]{12}$`)

// ACFS E13 with caam: a record at 92 % refuses Claude; after `caam
// activate claude e13b` the record is another profile's, so the pool is
// unknown (plan-v8 2.9: a caam switch invalidates that pool's
// observation), until a record of e13b comes.
func TestCaamProfileNamesTheCredential(t *testing.T) {
	caam, set, argv := fakeCaam(t)
	dir := t.TempDir()
	env := Env{RecordFile: filepath.Join(dir, "nhm", "quota", "claude.json"), CodexHome: filepath.Join(dir, "codex"), Caam: caam}
	set("e13a", "")
	if line, err := RecordClaude(statusLine(92), env, now); err != nil || line != "Opus · 5h 92%" {
		t.Fatalf("record = %q %v", line, err)
	}
	rec, err := os.ReadFile(env.RecordFile)
	if err != nil || !strings.Contains(string(rec), `"credential":{"profile":"e13a"}`) {
		t.Fatalf("record = %s %v", rec, err)
	}
	noSecrets(t, "the record", rec, "example.invalid", "caam-account")
	// The statusLine asks caam about Claude only.
	if got := argv(); got != "status claude --json\n" {
		t.Fatalf("caam argv = %q", got)
	}

	obs := Collect(env, now.Add(time.Minute), nil)
	claude := pool(t, obs, "claude")
	if !claude.Known() || *claude.FiveHour.UsedPct != 92 || claude.Credential == nil || claude.Credential.Profile != "e13a" || claude.Superseded {
		t.Fatalf("before the switch = %+v", claude)
	}
	if ok, why := Check(obs, "claude", 90); ok || !strings.Contains(why, "92% used") {
		t.Fatalf("check before the switch = %v %s", ok, why)
	}

	set("e13b", "")
	obs = Collect(env, now.Add(2*time.Minute), nil)
	claude = pool(t, obs, "claude")
	if claude.Known() || claude.FiveHour != nil || !claude.Superseded || claude.Credential.Profile != "e13b" ||
		claude.Note != "the newest record is of profile e13a, not of the active profile e13b: a credential switch invalidates it" {
		t.Fatalf("after the switch = %+v", claude)
	}
	// An unknown pool passes the check; admission warns or refuses (2.2).
	if ok, _ := Check(obs, "claude", 90); !ok {
		t.Fatal("the other profile's 92% refused")
	}
	out, _ := json.Marshal(obs)
	noSecrets(t, "nhm quota --json", out, "example.invalid", "caam-account")
	// The unknown pool has no observed_at, as E13 reads it.
	var pools []map[string]any
	_ = json.Unmarshal(out, &pools)
	if _, has := pools[0]["observed_at"]; has || pools[0]["superseded"] != true {
		t.Fatalf("claude json = %v", pools[0])
	}
	if !strings.Contains(argv(), "status --json\n") {
		t.Fatalf("Collect asks caam once for every tool: %q", argv())
	}

	// A record of e13b counts.
	if _, err := RecordClaude(statusLine(10), env, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if claude = pool(t, Collect(env, now.Add(4*time.Minute), nil), "claude"); !claude.Known() || *claude.FiveHour.UsedPct != 10 {
		t.Fatalf("a record of e13b = %+v", claude)
	}
	// Back to e13a: its 92 % was overwritten, and e13b's 10 % is not its.
	set("e13a", "")
	if claude = pool(t, Collect(env, now.Add(5*time.Minute), nil), "claude"); claude.Known() || !claude.Superseded {
		t.Fatalf("back on e13a = %+v", claude)
	}
}

// Without caam, a fingerprint of the account in the kind's auth file
// names the credential.
func TestAuthFileFingerprintNamesTheCredential(t *testing.T) {
	dir := t.TempDir()
	env := Env{RecordFile: filepath.Join(dir, "nhm", "quota", "claude.json"), CodexHome: filepath.Join(dir, ".codex"),
		ClaudeDir: filepath.Join(dir, ".claude"), ClaudeState: filepath.Join(dir, ".claude.json")}
	creds := filepath.Join(env.ClaudeDir, ".credentials.json")
	write(t, env.ClaudeState, `{"numStartups":3,"oauthAccount":{"accountUuid":"UUID-A","emailAddress":"a@example.invalid"}}`, now)
	write(t, creds, `{"claudeAiOauth":{"accessToken":"tok-1","refreshToken":"ref-1"}}`, now)
	if _, err := RecordClaude(statusLine(92), env, now); err != nil {
		t.Fatal(err)
	}
	rec, _ := os.ReadFile(env.RecordFile)
	var r struct{ Credential *Credential }
	if json.Unmarshal(rec, &r) != nil || r.Credential == nil || r.Credential.Profile != "" || !shortFingerprint.MatchString(r.Credential.Fingerprint) {
		t.Fatalf("record = %s", rec)
	}
	noSecrets(t, "the record", rec, "uuid-a", "example.invalid", "tok-1", "ref-1")
	fpA := r.Credential.Fingerprint
	if claude := pool(t, Collect(env, now.Add(time.Minute), nil), "claude"); !claude.Known() || *claude.FiveHour.UsedPct != 92 {
		t.Fatalf("claude = %+v", claude)
	}
	// Claude Code rotates its tokens every few hours: the account stays,
	// and so does the observation.
	write(t, creds, `{"claudeAiOauth":{"accessToken":"tok-2","refreshToken":"ref-2"}}`, now)
	if claude := pool(t, Collect(env, now.Add(time.Minute), nil), "claude"); !claude.Known() {
		t.Fatalf("after a token refresh = %+v", claude)
	}
	// A login to another account.
	write(t, env.ClaudeState, `{"numStartups":4,"oauthAccount":{"accountUuid":"UUID-B","emailAddress":"b@example.invalid"}}`, now)
	obs := Collect(env, now.Add(time.Minute), nil)
	claude := pool(t, obs, "claude")
	if claude.Known() || !claude.Superseded || !strings.Contains(claude.Note, "of fingerprint "+fpA+", not of the active fingerprint "+claude.Credential.Fingerprint) ||
		claude.Credential.Fingerprint == fpA {
		t.Fatalf("after a login = %+v", claude)
	}
	out, _ := json.Marshal(obs)
	noSecrets(t, "nhm quota --json", out, "uuid-a", "uuid-b", "example.invalid", "tok-", "ref-")

	// No account in .claude.json (an API key; E13's stand-ins): the
	// credentials file names the credential.
	write(t, env.ClaudeState, `{"numStartups":5}`, now)
	write(t, creds, `{"claudeAiOauth":{"accessToken":"e13-account-a"}}`, now)
	if _, err := RecordClaude(statusLine(92), env, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if claude := pool(t, Collect(env, now.Add(3*time.Minute), nil), "claude"); !claude.Known() {
		t.Fatalf("by the file = %+v", claude)
	}
	write(t, creds, `{"claudeAiOauth":{"accessToken":"e13-account-b"}}`, now)
	if claude := pool(t, Collect(env, now.Add(3*time.Minute), nil), "claude"); claude.Known() || !claude.Superseded {
		t.Fatalf("another file = %+v", claude)
	}

	// Codex: auth.json's account against the one each log's session_meta
	// names.
	auth := filepath.Join(env.CodexHome, "auth.json")
	write(t, auth, `{"tokens":{"account_id":"ACCT-A","access_token":"ctok-1"},"last_refresh":"2026-10-12T00:00:00Z"}`, now)
	codexSession(t, env.CodexHome, "a", "acct-a", now.Add(-time.Hour), 93)
	codex := pool(t, Collect(env, now, nil), "codex")
	if !codex.Known() || *codex.FiveHour.UsedPct != 93 || !shortFingerprint.MatchString(codex.Credential.Fingerprint) {
		t.Fatalf("codex = %+v", codex)
	}
	// A token refresh keeps the account.
	write(t, auth, `{"tokens":{"account_id":"ACCT-A","access_token":"ctok-2"},"last_refresh":"2026-10-12T03:00:00Z"}`, now)
	if codex = pool(t, Collect(env, now, nil), "codex"); !codex.Known() {
		t.Fatalf("codex after a refresh = %+v", codex)
	}
	write(t, auth, `{"tokens":{"account_id":"acct-b","access_token":"ctok-3"}}`, now)
	obs = Collect(env, now, nil)
	if codex = pool(t, obs, "codex"); codex.Known() || !codex.Superseded || !strings.Contains(codex.Note, "a credential switch") {
		t.Fatalf("codex after a switch = %+v", codex)
	}
	out, _ = json.Marshal(obs)
	noSecrets(t, "nhm quota --json", out, "acct-a", "acct-b", "ctok")
	// The log of the active account counts, though another's is newer
	// (2.9: the session log of the intended profile).
	codexSession(t, env.CodexHome, "b", "acct-b", now.Add(-2*time.Hour), 10)
	if codex = pool(t, Collect(env, now, nil), "codex"); !codex.Known() || *codex.FiveHour.UsedPct != 10 {
		t.Fatalf("codex of the active account = %+v", codex)
	}
	// A log without session_meta (an older Codex) names no account: it
	// counts, as before, unless a newer log names another account.
	codexSession(t, env.CodexHome, "c", "", now.Add(-30*time.Minute), 50)
	if codex = pool(t, Collect(env, now, nil), "codex"); !codex.Known() || *codex.FiveHour.UsedPct != 50 {
		t.Fatalf("a log that names no account = %+v", codex)
	}
	codexSession(t, env.CodexHome, "c", "", now.Add(-90*time.Minute), 50)
	if codex = pool(t, Collect(env, now, nil), "codex"); !codex.Known() || *codex.FiveHour.UsedPct != 10 {
		t.Fatalf("a log that names no account, older than another account's = %+v", codex)
	}
}

// A failing caam names no profile: the fingerprint decides.
func TestFailingCaamFallsBackToTheFingerprint(t *testing.T) {
	dir := t.TempDir()
	caam := filepath.Join(dir, "caam")
	write(t, caam, "#!/bin/sh\necho 'Error: vault locked' >&2\nexit 1\n", now)
	if err := os.Chmod(caam, 0o755); err != nil {
		t.Fatal(err)
	}
	env := Env{Caam: caam, ClaudeDir: filepath.Join(dir, ".claude"), ClaudeState: filepath.Join(dir, ".claude.json")}
	write(t, env.ClaudeState, `{"oauthAccount":{"accountUuid":"uuid-a"}}`, now)
	c := ActiveCredential(env, "claude")
	if c == nil || c.Profile != "" || c.Fingerprint != accountFingerprint("claude", "UUID-A") {
		t.Fatalf("credential = %+v", c)
	}
	// A .claude.json or auth.json caught in a write names nothing: the
	// file's fingerprint would never match the account's.
	write(t, filepath.Join(env.ClaudeDir, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"tok"}}`, now)
	write(t, env.ClaudeState, `{"oauthAccount":{"accountUu`, now)
	if c := ActiveCredential(env, "claude"); c != nil {
		t.Fatalf("a torn .claude.json = %+v", c)
	}
	env.CodexHome = filepath.Join(dir, ".codex")
	write(t, filepath.Join(env.CodexHome, "auth.json"), `{"tokens":{"acc`, now)
	if c := ActiveCredential(env, "codex"); c != nil {
		t.Fatalf("a torn auth.json = %+v", c)
	}
	// The kinds' fingerprints differ for one id.
	if accountFingerprint("claude", "x") == accountFingerprint("codex", "x") {
		t.Fatal("one fingerprint for two kinds")
	}
	if ActiveCredential(Env{}, "claude") != nil || ActiveCredential(Env{}, "agy") != nil {
		t.Fatal("a credential from nothing")
	}
}

// A caam that hangs, with a child that holds its stdout, holds the
// statusLine no longer than caamTimeout and WaitDelay (nhm-fty).
func TestCaamThatHangsNamesNoProfile(t *testing.T) {
	dir := t.TempDir()
	caam := filepath.Join(dir, "caam")
	write(t, caam, "#!/bin/sh\nsleep 30\n", now)
	if err := os.Chmod(caam, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if p := caamProfiles(caam, "claude"); len(p) != 0 {
		t.Fatalf("profiles = %v", p)
	}
	if d := time.Since(start); d > caamTimeout+5*time.Second {
		t.Fatalf("caam held the call %v", d)
	}
}

func TestOfActive(t *testing.T) {
	a, b := &Credential{Profile: "a", Fingerprint: "fa"}, &Credential{Profile: "b", Fingerprint: "fb"}
	cases := []struct {
		name   string
		active *Credential
		creds  []*Credential // newest first
		kept   string
		other  *Credential
	}{
		{"no active credential: all count", nil, []*Credential{a, nil, b}, "yyy", nil},
		{"the active one's", a, []*Credential{a, b}, "yn", b},
		{"another's, then the active one's", a, []*Credential{b, nil, a}, "nny", b},
		{"none named, newest", a, []*Credential{nil, b, a}, "yny", b},
		{"by fingerprint without a profile", a, []*Credential{{Fingerprint: "fa"}, {Fingerprint: "fb"}}, "yn", &Credential{Fingerprint: "fb"}},
		{"profiles decide over fingerprints", a, []*Credential{{Profile: "c", Fingerprint: "fa"}}, "n", &Credential{Profile: "c", Fingerprint: "fa"}},
		{"nothing to compare: as none named", &Credential{Fingerprint: "fa"}, []*Credential{{Profile: "a"}}, "y", nil},
	}
	for _, c := range cases {
		kept, other := ofActive(c.active, c.creds)
		got := ""
		for _, k := range kept {
			got += map[bool]string{true: "y", false: "n"}[k]
		}
		if got != c.kept || (other == nil) != (c.other == nil) || (other != nil && *other != *c.other) {
			t.Errorf("%s: kept %s other %+v, want %s %+v", c.name, got, other, c.kept, c.other)
		}
	}
}

// A superseded pool's rows are another credential's: Store deletes them,
// so status and attention no longer show the old account's fill or limit.
func TestStoreDeletesASupersededPool(t *testing.T) {
	inst, _, err := store.Enroll(t.TempDir(), "/run/h.sock", "default", now)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pct := 100.0
	known := []Observation{
		{Pool: "codex", Kind: "codex", ObservedAt: &now, FiveHour: &Window{UsedPct: &pct}, Weekly: &Window{UsedPct: &pct},
			LimitReached: &Limit{Type: "primary", ObservedAt: now}},
		{Pool: "claude", Kind: "claude", ObservedAt: &now, FiveHour: &Window{UsedPct: &pct}},
	}
	if err := Store(context.Background(), db.SQL(), known); err != nil {
		t.Fatal(err)
	}
	gone := []Observation{*superseded("codex", &Credential{Profile: "b"}, &Credential{Profile: "a"}), {Pool: "claude", Kind: "claude"}}
	if err := Store(context.Background(), db.SQL(), gone); err != nil {
		t.Fatal(err)
	}
	var codex, claude int
	if err := db.SQL().QueryRow(`SELECT count(*) FILTER (WHERE pool_id = 'codex'), count(*) FILTER (WHERE pool_id = 'claude') FROM quota`).
		Scan(&codex, &claude); err != nil {
		t.Fatal(err)
	}
	// An unknown pool that is not superseded keeps its rows, as before.
	if codex != 0 || claude != 1 {
		t.Fatalf("rows: codex %d claude %d", codex, claude)
	}
}
