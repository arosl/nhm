package kinds

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// realProgramHead reads real files, for the content tests.
var realProgramHead = ProgramHead

// The tests need no agent CLI on PATH: each kind resolves to its own
// file, an ELF binary.
func init() {
	ResolveProgram = func(name string) (string, error) { return "/opt/" + name + "/bin/" + name, nil }
	ProgramHead = func(string) ([]byte, error) { return []byte("\x7fELF\x02\x01\x01"), nil }
}

// By content, the kind's CLI is an ELF binary, a node script, or ACFS's
// bun wrapper around a node script (as for codex); a shell script, a
// python script, or a wrapper around anything else is not (the review of
// ce21b11).
func TestProgramContent(t *testing.T) {
	old := ProgramHead
	ProgramHead = realProgramHead
	t.Cleanup(func() { ProgramHead = old })
	root := t.TempDir()
	file := func(rel, content string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	codexJS := file("bun/node_modules/@openai/codex/bin/codex.js", "#!/usr/bin/env node\nimport x\n")
	pyCodex := file("py/codex/bin/codex", "#!/usr/bin/env python3\nimport x\n")
	bunDir := filepath.Dir(file("dotbun/bin/bun", "\x7fELF\x02\x01\x01\x00"))
	node := file("nodejs/bin/node", "\x7fELF\x02\x01\x01\x00")
	otherNode := file("tmp/x/node", "\x7fELF\x02\x01\x01\x00")
	notBun := file("shbin/sh", "\x7fELF\x02\x01\x01\x00")
	if err := os.MkdirAll(filepath.Join(root, "linkbun"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(notBun, filepath.Join(root, "linkbun", "bun")); err != nil {
		t.Fatal(err)
	}
	wrapper := func(bun, script string) string {
		return "#!/bin/bash\nexec \"" + bun + "/bun\" \"" + script + "\" \"$@\"\n"
	}
	for path, want := range map[string]bool{
		file("a/claude/versions/2.1.296", "\x7fELF\x02\x01\x01\x00"): true,
		codexJS: true,
		file("b/codex/bin/codex", "#!"+node+"\n"):                                    true,
		file("b2/codex/bin/codex", "#!"+node+" --no-warnings\n"):                     false,
		file("b3/codex/bin/codex", "#!"+otherNode+"\n"):                              false,
		file("c/codex/bin/codex", "#!/usr/bin/env -S node --flag\n"):                 false,
		file("c2/codex/bin/codex", "#!/usr/bin/env -S node --require /tmp/x\n"):      false,
		file("d/codex/bin/codex", wrapper(bunDir, codexJS)):                          true,
		file("d2/codex/bin/codex", wrapper("/h/.bun/bin", codexJS)):                  false, // no bun there
		file("d3/codex/bin/codex", wrapper("$(evil)", codexJS)):                      false,
		file("d4/codex/bin/codex", wrapper("`evil`", codexJS)):                       false,
		file("d5/codex/bin/codex", wrapper(filepath.Join(root, "linkbun"), codexJS)): false, // resolves to sh
		file("e/codex/bin/codex", wrapper(bunDir, pyCodex)):                          false,
		file("f/codex/bin/codex", wrapper(bunDir, codexJS)+"rm -r ~\n"):              false,
		file("g/codex/bin/codex", "#!/bin/sh\necho hi\n"):                            false,
		pyCodex: false,
		file("h/codex/bin/codex", "#!/usr/bin/env bun\n"):              false,
		file("i/codex/bin/codex", "MZ\x90\x00"):                        false,
		file("j/codex/bin/ksh93", "\x7fELF\x02\x01\x01\x00"):           false,
		file("k/codex/bin/pypy3", "\x7fELF\x02\x01\x01\x00"):           false,
		file("l/codex/node_modules/.bin/tsx", "#!/usr/bin/env node\n"): false,
	} {
		withResolvedNode(t, path, node)
		if got := ProgramIsKind("codex", "codex") || ProgramIsKind("claude", "claude"); got != want {
			t.Errorf("%s: %v, want %v", strings.TrimPrefix(path, root), got, want)
		}
	}
	// A FIFO is not read: opening it must not wait for a writer.
	fifo := filepath.Join(root, "fifo", "codex", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	withResolvedNode(t, fifo, node)
	done := make(chan bool, 1)
	go func() { done <- ProgramIsKind("codex", "codex") }()
	select {
	case got := <-done:
		if got {
			t.Error("a FIFO counts as the codex CLI")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ProgramIsKind blocked on a FIFO")
	}
}

// withResolvedNode resolves every kind to resolved, and node to node.
func withResolvedNode(t *testing.T, resolved, node string) {
	t.Helper()
	old := ResolveProgram
	ResolveProgram = func(name string) (string, error) {
		if name == "node" {
			return node, nil
		}
		return resolved, nil
	}
	t.Cleanup(func() { ResolveProgram = old })
}

func withResolved(t *testing.T, resolved string) {
	t.Helper()
	old := ResolveProgram
	ResolveProgram = func(string) (string, error) { return resolved, nil }
	t.Cleanup(func() { ResolveProgram = old })
}

// The program on PATH must resolve to the kind's CLI, not to a shell or
// an interpreter behind a symlink (the review of 3643fe5).
func TestProgramIsKind(t *testing.T) {
	for resolved, want := range map[string]bool{
		"/home/u/.local/share/claude/versions/2.1.296":     true,
		"/home/u/.grok/downloads/grok-linux-x86_64":        true,
		"/usr/lib/node_modules/@openai/codex/bin/codex.js": true,
		"/home/u/.local/bin/codex":                         true,
		"/usr/bin/bash":                                    false,
		"/home/u/claude/bin/python3.12":                    false,
		"/opt/claude/node":                                 false,
		"/usr/local/bin/other":                             false,
		"/home/u/claudette/bin/x":                          false,
	} {
		withResolved(t, resolved)
		if got := ProgramIsKind("claude", "claude") || ProgramIsKind("grok", "grok") || ProgramIsKind("codex", "codex"); got != want {
			t.Errorf("%s: %v, want %v", resolved, got, want)
		}
	}
	c, _ := NewRegistry().Get("claude")
	withResolved(t, "/usr/bin/dash")
	if got := c.Resume("s-1", "", "", ""); got != nil {
		t.Fatalf("claude as dash resumes: %v", got)
	}
	old := ResolveProgram
	ResolveProgram = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { ResolveProgram = old }()
	if got := c.Resume("s-1", "", "", ""); got != nil {
		t.Fatalf("claude not on PATH resumes: %v", got)
	}
}

func TestBuiltins(t *testing.T) {
	r := NewRegistry()
	if got := strings.Join(r.Names(), ","); got != "claude,codex,agy,pi,omp,opencode,grok" {
		t.Fatalf("names = %s", got)
	}
	c, _ := r.Get("claude")
	if !c.Builtin || c.ModelFlag != "--model" || !slices.Equal(c.InterruptKeys, []string{"esc"}) || !c.CodeReview || c.Continue == nil {
		t.Fatalf("claude = %+v", c)
	}
	// A resumed Claude runs as guarded as the spawned one (the review of
	// b0902bb): the model, the effort and the hooks' --settings stay.
	if got := c.Resume("s-1", "opus", "", `{"hooks":{}}`); !slices.Equal(got, []string{"claude", "--dangerously-skip-permissions",
		"--resume", "s-1", "--model", "opus", "--effort", "xhigh", "--settings", `{"hooks":{}}`}) {
		t.Fatalf("resume = %v", got)
	}
	if got := c.Resume("s-1", "", "high", ""); !slices.Equal(got, []string{"claude", "--dangerously-skip-permissions", "--resume", "s-1", "--effort", "high"}) {
		t.Fatalf("resume without model and settings = %v", got)
	}
	// A session id that is not a plain id never reaches an argv.
	for _, bad := range []string{"-rf", "--help", "", "a b", "x;y", "../x"} {
		if got := c.Resume(bad, "", "", ""); got != nil {
			t.Errorf("resume %q = %v", bad, got)
		}
	}
	// ntm's launch flags and effort.
	if !slices.Equal(c.LaunchArgs, []string{"--dangerously-skip-permissions"}) || !slices.Equal(c.Effort(""), []string{"--effort", "xhigh"}) ||
		!slices.Equal(c.Effort("high"), []string{"--effort", "high"}) || c.Effort("x y") != nil || c.SettingsFlag != "--settings" {
		t.Fatalf("claude launch = %+v", c)
	}
	x, _ := r.Get("codex")
	if got := x.Resume("thr-1", "", "", ""); !slices.Equal(got, []string{"codex", "--no-daemon", "--dangerously-bypass-approvals-and-sandbox",
		"resume", "thr-1", "-c", "model_reasoning_effort=xhigh"}) {
		t.Fatalf("codex resume = %v", got)
	}
	if x.CodeReview || !slices.Equal(x.LaunchArgs, []string{"--no-daemon", "--dangerously-bypass-approvals-and-sandbox"}) ||
		!slices.Equal(x.Effort(""), []string{"-c", "model_reasoning_effort=xhigh"}) || !x.Hooks.Trust || x.NewSession != "/new" {
		t.Fatalf("codex = %+v", x)
	}
	if a, _ := r.Get("agy"); a.NTMType != "antigravity" || a.ModelFlag != "" || a.InterruptKeys != nil || a.Resume("s", "", "", "") != nil || len(a.Cannot) == 0 {
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
		if err := checkResumeArgv(a.Name, a.ResumeArgv, a.LaunchArgs, a.ResumeFlags); err != nil {
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
		!m.Hooks.SessionStart || m.Continue != nil || !m.LimitPattern().MatchString("OUT OF CREDITS") || m.Resume("x", "", "", "") != nil {
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
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--resume\"]\n",
		"[kinds.x]\nguard_settings = \"../etc/passwd\"\n",
		// resume_argv runs the kind itself, by its bare name, and only an
		// agent CLI that herdr starts: an allowlist (the review of
		// b0902bb), so no shell, launcher, interpreter or symlinked name.
		"[kinds.x]\nresume_argv = [\"x\", \"--resume\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"sh\", \"-c\", \"gemini {session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"find\", \"-exec\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"python3.12\", \"-c\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"/usr/local/bin/gemini\", \"--resume\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"claude\", \"--resume\", \"{session}\"]\n",
		"[kinds.bash]\nresume_argv = [\"bash\", \"-c\", \"{session}\"]\n",
		// {session} only as a named flag's value or after resume; "--" is
		// not a flag, and a bare positional is refused.
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"{session}\", \"run\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--\", \"{session}\", \"y\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"chat\", \"{session}\"]\n",
		// {session} only as a resume flag's value: after another flag it
		// could name a file the CLI loads (the review of 3643fe5).
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--settings\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--mcp-config\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"-e\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--extensions\", \"--resume\", \"{session}\"]\n",
		"[kinds.gemini]\nlaunch_args = [\"--yolo\"]\nresume_argv = [\"gemini\", \"--yolo\", \"-e\", \"--resume\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--resume\", \"{session}\", \"--resume\", \"{session}\"]\n",
		// No "--" anywhere: nhm's flags would become prompt text.
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--resume\", \"{session}\", \"--\"]\n",
		// The resume flag follows the kind or a launch_arg: an argument
		// before it could take it as its value (the review of ce21b11).
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--mcp-config\", \"--resume\", \"{session}\"]\n",
		"[kinds.gemini]\nlaunch_args = [\"--yolo\"]\nresume_argv = [\"gemini\", \"--yolo\", \"-x\", \"--resume\", \"{session}\"]\n",
		// resume_flags come from each kind's own allowlist (acfs-9hkh.79):
		// a short flag or --session-file is no resume flag, and a kind nhm
		// has checked no flags of has no resume.
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"-c\", \"{session}\"]\nresume_flags = [\"-c\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"-r\", \"{session}\"]\nresume_flags = [\"-r\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--session-file\", \"{session}\"]\nresume_flags = [\"--session-file\"]\n",
		"[kinds.copilot]\nresume_argv = [\"copilot\", \"--session-file\", \"{session}\"]\nresume_flags = [\"--session-file\"]\n",
		"[kinds.kimi]\nresume_argv = [\"kimi\", \"--resume\", \"{session}\"]\nresume_flags = [\"--resume\"]\n",
		// resume_flags are per kind, declared, and name a resume or a
		// session: without them, or with a file-loading flag, no resume.
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--resume\", \"{session}\"]\nresume_flags = []\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--settings\", \"{session}\"]\nresume_flags = [\"--settings\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--mcp-config\", \"{session}\"]\nresume_flags = [\"--mcp-config\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--resume\", \"{session}\"]\nresume_flags = [\"-r\"]\n",
		"[kinds.x]\neffort_args = [\"--effort\"]\n",
		"[kinds.x]\ndefault_effort = \"high\"\n",
		"[kinds.x]\neffort_args = [\"--effort\", \"{effort}\"]\ndefault_effort = \"x y\"\n",
		"[kinds.gemini]\nresume_argv = [\"/usr/bin/env\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--resume={session}\"]\n",
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
		// The placement cases declare every usual resume flag, so each
		// fails on its own rule, not on missing resume_flags.
		if strings.Contains(bad, "resume_argv") && !strings.Contains(bad, "resume_flags") {
			bad += "resume_flags = [\"--resume\"]\n"
		}
		write(bad)
		if err := NewRegistry().LoadConfig(path); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
	// An agent CLI that herdr starts, with the session as the value of one
	// of the kind's documented resume flags.
	for _, good := range []struct{ kind, config string }{
		{"gemini", "[kinds.gemini]\nmodel_flag = \"-m\"\nresume_argv = [\"gemini\", \"--resume\", \"{session}\"]\nresume_flags = [\"--resume\"]\n"},
		{"cursor", "[kinds.cursor]\nmodel_flag = \"-m\"\nresume_argv = [\"cursor\", \"--resume\", \"{session}\", \"--quiet\"]\nresume_flags = [\"--resume\"]\n"},
		{"copilot", "[kinds.copilot]\nmodel_flag = \"-m\"\nresume_argv = [\"copilot\", \"--resume\", \"{session}\"]\nresume_flags = [\"--resume\"]\n"},
	} {
		write(good.config)
		r := NewRegistry()
		if err := r.LoadConfig(path); err != nil {
			t.Fatalf("refused:\n%s%v", good.config, err)
		}
		g, _ := r.Get(good.kind)
		// nhm's flags follow the session id, before the kind's own tail.
		if got := g.Resume("s-9", "flash", "", ""); len(got) < 5 || got[2] != "s-9" || got[3] != "-m" || got[4] != "flash" {
			t.Fatalf("%s resume = %v", good.kind, got)
		}
	}
}
