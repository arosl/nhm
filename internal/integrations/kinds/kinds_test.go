package kinds

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuiltins(t *testing.T) {
	r := NewRegistry()
	if got := strings.Join(r.Names(), ","); got != "claude,codex,agy,pi,omp,opencode,grok" {
		t.Fatalf("names = %s", got)
	}
	c, _ := r.Get("claude")
	if !c.Builtin || c.ModelFlag != "--model" || !slices.Equal(c.InterruptKeys, []string{"esc"}) || !c.CodeReview || c.Continue == nil {
		t.Fatalf("claude = %+v", c)
	}
	if got := c.Resume("s-1"); !slices.Equal(got, []string{"claude", "--dangerously-skip-permissions", "--resume", "s-1"}) {
		t.Fatalf("resume = %v", got)
	}
	// A session id that is not a plain id never reaches an argv.
	for _, bad := range []string{"-rf", "--help", "", "a b", "x;y", "../x"} {
		if got := c.Resume(bad); got != nil {
			t.Errorf("resume %q = %v", bad, got)
		}
	}
	// ntm's launch flags and effort.
	if !slices.Equal(c.LaunchArgs, []string{"--dangerously-skip-permissions"}) || !slices.Equal(c.Effort(""), []string{"--effort", "xhigh"}) ||
		!slices.Equal(c.Effort("high"), []string{"--effort", "high"}) || c.Effort("x y") != nil || c.SettingsFlag != "--settings" {
		t.Fatalf("claude launch = %+v", c)
	}
	if got := c.Resume("s-1"); got == nil {
		t.Fatalf("resume = %v", got)
	}
	x, _ := r.Get("codex")
	if x.CodeReview || !slices.Equal(x.LaunchArgs, []string{"--no-daemon", "--dangerously-bypass-approvals-and-sandbox"}) ||
		!slices.Equal(x.Effort(""), []string{"-c", "model_reasoning_effort=xhigh"}) || !x.Hooks.Trust || x.NewSession != "/new" {
		t.Fatalf("codex = %+v", x)
	}
	if a, _ := r.Get("agy"); a.NTMType != "antigravity" || a.ModelFlag != "" || a.InterruptKeys != nil || a.Resume("s") != nil || len(a.Cannot) == 0 {
		t.Fatalf("agy = %+v", a)
	}
	if p, _ := r.Get("pi"); p.GuardSupported {
		t.Fatal("pi supports guards")
	}
	for flag, want := range map[string]string{"cc": "claude", "cod": "codex", "oc": "opencode", "agy": "agy", "codex": "codex"} {
		if a, ok := r.ByFlag(flag); !ok || a.Name != want {
			t.Errorf("flag %s = %s %v", flag, a.Name, ok)
		}
	}
	if _, ok := r.ByFlag("zz"); ok {
		t.Fatal("an unknown flag selects a kind")
	}
	// The limit recognisers: each kind's own, and the generic one.
	if !c.LimitPattern().MatchString("You've hit your limit · resets 5pm") || !x.LimitPattern().MatchString("429 Too Many Requests") {
		t.Fatal("a limit was not recognised")
	}
	if g, _ := r.Get("grok"); g.LimitPattern() != GenericLimit {
		t.Fatal("grok has no generic limit")
	}
	if Lookup("weird").Name != "weird" || Lookup("").NTMType != "unknown" {
		t.Fatal("lookup of an unregistered kind")
	}
}

// The built-ins meet the rules that config kinds must meet.
func TestBuiltinsMeetTheConfigRules(t *testing.T) {
	for _, a := range NewRegistry().All() {
		if err := checkResumeArgv(a.ResumeArgv); err != nil {
			t.Errorf("%s resume_argv: %v", a.Name, err)
		}
		for _, k := range a.InterruptKeys {
			if !KeyAllowed(k) {
				t.Errorf("%s interrupt key %q", a.Name, k)
			}
		}
		if a.TrustKeys != nil {
			for _, screen := range []string{
				"Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder\n",
				"Trust this folder?\n› 1. Trust and continue\n",
			} {
				for _, k := range a.TrustKeys(screen) {
					if !KeyAllowed(k) {
						t.Errorf("%s trust key %q", a.Name, k)
					}
				}
			}
		}
		if reserved[a.Name] {
			t.Errorf("%s is reserved", a.Name)
		}
	}
}

func TestTrustKeysAndPrograms(t *testing.T) {
	c, _ := Get("claude")
	x, _ := Get("codex")
	claudeNo := "Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder\n"
	claudeYes := "Is this a project you created or one you trust?\n ❯ Yes, I trust this folder\n   No, exit\n"
	if got := c.TrustKeys(claudeNo); !slices.Equal(got, []string{"down", "enter"}) {
		t.Fatalf("claude, cursor on no = %v", got)
	}
	if got := c.TrustKeys(claudeYes); !slices.Equal(got, []string{"enter"}) {
		t.Fatalf("claude, cursor on yes = %v", got)
	}
	if c.TrustKeys("Something else entirely") != nil || x.TrustKeys(claudeYes) != nil {
		t.Fatal("a screen that is not the dialog was answered")
	}
	if got := x.TrustKeys("Trust this folder?\n› 1. Trust and continue\n  2. Quit\n"); !slices.Equal(got, []string{"enter"}) {
		t.Fatalf("codex = %v", got)
	}
	if c.Program != "claude-code" || x.Program != "codex-cli" {
		t.Fatalf("programs = %s %s", c.Program, x.Program)
	}
	// A kind without its own program records its kind name.
	if p, _ := Get("pi"); p.Program != "pi" {
		t.Fatalf("pi program = %q", p.Program)
	}
}

func TestContinuationRules(t *testing.T) {
	c, _ := Get("claude")
	d := c.Continue(Event{HookEvent: "SessionStart", Source: "compact", SessionID: "s-1", Entrypoint: "cli"}, "s-1", false)
	if d.Verdict != Continue || !d.Compacted {
		t.Fatalf("claude compact = %+v", d)
	}
	if d := c.Continue(Event{HookEvent: "SessionStart", Source: "clear", SessionID: "s-2", Entrypoint: "cli"}, "s-1", false); d.Verdict != Displace {
		t.Fatalf("claude clear = %+v", d)
	}
	x, _ := Get("codex")
	if d := x.Continue(Event{HookEvent: "SessionStart", Source: "resume", SessionID: "s-1"}, "s-1", false); d.Verdict != Continue || !d.Resumed {
		t.Fatalf("codex resume = %+v", d)
	}
	if a, _ := Get("agy"); a.Continue != nil {
		t.Fatal("agy has a continuation rule")
	}
}

func TestRegisterAndConfig(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Adapter{Name: "claude"}); err == nil {
		t.Fatal("a built-in was replaced")
	}
	if err := r.Register(Adapter{Name: "mine", Flags: []string{"cc"}}); err == nil {
		t.Fatal("a flag selects two kinds")
	}
	if err := r.Register(Adapter{Name: "Bad Name"}); err == nil {
		t.Fatal("a bad name was registered")
	}
	if err := r.Register(Adapter{Name: "cc"}); err == nil {
		t.Fatal("a kind took another kind's flag as its name")
	}
	for _, a := range []Adapter{{Name: "force"}, {Name: "all"}, {Name: "help"}, {Name: "mine", Flags: []string{"wait"}}} {
		if err := r.Register(a); err == nil {
			t.Errorf("%+v took a verb flag", a)
		}
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := r.LoadConfig(path); err != nil {
		t.Fatalf("missing file = %v", err)
	}
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`[memory]
cm = "off"
[kinds.mycli]
flags = ["my"]
interrupt_keys = ["C-c"]
resume_argv = ["mycli", "--resume", "{session}"]
limit = "out of credits"
code_review = false
[kinds.mycli.hooks]
session_start = true
`)
	if err := r.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	m, ok := r.ByFlag("my")
	if !ok || m.Name != "mycli" || m.Builtin || m.CodeReview || !m.GuardSupported || m.NTMType != "mycli" || m.QuotaPool != "mycli" ||
		!m.Hooks.SessionStart || m.Continue != nil || !m.LimitPattern().MatchString("OUT OF CREDITS") || m.Resume("x")[2] != "x" {
		t.Fatalf("mycli = %+v", m)
	}
	if r.Names()[len(r.Names())-1] != "mycli" {
		t.Fatalf("names = %v", r.Names())
	}
	// The process registry: the config's kinds, or none when one fails.
	if d, err := withConfig(path); err != nil || len(d.Names()) != 8 {
		t.Fatalf("with config = %v %v", d.Names(), err)
	}
	write("[kinds.good]\n[kinds.zbad]\nlimit = \"(\"\n")
	if d, err := withConfig(path); err == nil || len(d.Names()) != 7 {
		t.Fatalf("half a config = %v %v", d.Names(), err)
	}
	for _, bad := range []string{
		"[kinds.mycli]\ninterupt_keys = [\"x\"]\n",
		"[kinds.claude]\nflags = [\"c2\"]\n",
		"[kinds.x]\nlimit = \"(\"\n",
		"[kinds.x]\nresume_argv = [\"x\", \"--resume\"]\n",
		"[kinds.x]\nguard_settings = \"../etc/passwd\"\n",
		// resume_argv: the program is fixed, not a shell, and the
		// session is a whole argument (acfs-9hkh.52).
		"[kinds.x]\nresume_argv = [\"{session}\"]\n",
		"[kinds.x]\nresume_argv = [\"{session}\", \"x\"]\n",
		"[kinds.x]\nresume_argv = [\"sh\", \"-c\", \"x {session}\"]\n",
		// A launcher anywhere, not only first (the review of 78b3037).
		"[kinds.x]\nresume_argv = [\"nice\", \"sh\", \"-c\", \"{session}\"]\n",
		"[kinds.x]\nresume_argv = [\"timeout\", \"5\", \"x\", \"{session}\"]\n",
		"[kinds.x]\nresume_argv = [\"x\", \"sudo\", \"{session}\"]\n",
		"[kinds.x]\nresume_argv = [\"x\", \"/usr/bin/ssh\", \"{session}\"]\n",
		"[kinds.x]\nresume_argv = [\"x\", \"--resume\", \"{session}\", \"--watch\", \"watch\"]\n",
		// {session} only last, or as a flag's value.
		"[kinds.x]\nresume_argv = [\"x\", \"{session}\", \"run\"]\n",
		"[kinds.x]\neffort_args = [\"--effort\"]\n",
		"[kinds.x]\ndefault_effort = \"high\"\n",
		"[kinds.x]\neffort_args = [\"--effort\", \"{effort}\"]\ndefault_effort = \"x y\"\n",
		"[kinds.x]\nresume_argv = [\"/usr/bin/env\", \"{session}\"]\n",
		"[kinds.x]\nresume_argv = [\"x\", \"--resume={session}\"]\n",
		// Pane-typed fields take no control characters; keys are herdr's.
		"[kinds.x]\nlaunch_args = [\"--a\\n--b\"]\n",
		"[kinds.x]\nlaunch_args = [\"\"]\n",
		"[kinds.x]\nnew_session = \"/new\\r\"\n",
		"[kinds.x]\nexit_command = \"/quit\\n\"\n",
		"[kinds.x]\nmodel_flag = \"model\"\n",
		"[kinds.x]\ninterrupt_keys = [\"Escape\"]\n",
		"[kinds.x]\ninterrupt_keys = [\"esc; rm\"]\n",
		"[kinds.force]\n",
		"[kinds.x]\nflags = [\"dry-run\"]\n",
	} {
		write(bad)
		if err := NewRegistry().LoadConfig(path); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}
