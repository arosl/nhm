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
	if !c.Builtin || c.ModelFlag != "--model" || !slices.Equal(c.InterruptKeys, []string{"Escape"}) || !c.CodeReview || c.Continue == nil {
		t.Fatalf("claude = %+v", c)
	}
	if got := c.Resume("s-1"); !slices.Equal(got, []string{"claude", "--resume", "s-1"}) {
		t.Fatalf("resume = %v", got)
	}
	x, _ := r.Get("codex")
	if x.CodeReview || !slices.Equal(x.LaunchArgs, []string{"--no-daemon"}) || !x.Hooks.Trust || x.NewSession != "/new" {
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
	} {
		write(bad)
		if err := NewRegistry().LoadConfig(path); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}
