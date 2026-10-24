package kinds

import (
	"errors"
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
// ACFS's bun wrapper holds two absolute paths, so a deep $HOME makes it
// long. It counts while the whole file fits what nhm reads (the review of
// 64a5abc: 512 bytes cut it off), and a longer one is refused as cut off.
func TestProgramContentLongWrapper(t *testing.T) {
	old := ProgramHead
	ProgramHead = realProgramHead
	t.Cleanup(func() { ProgramHead = old })
	root, err := os.MkdirTemp("/tmp", "nhm-kinds-long")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	deep := func(n int) string { // n path components of 200 bytes each
		parts := []string{root}
		for i := 0; i < n; i++ {
			parts = append(parts, strings.Repeat(string(rune('a'+i)), 200))
		}
		return filepath.Join(parts...)
	}
	write := func(path, content string) string {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, c := range []struct {
		name  string
		depth int
		want  bool
	}{
		{"about 300-byte paths, over 512 bytes", 1, true},
		{"about 2100-byte paths, over 4096 bytes", 10, false},
	} {
		home := deep(c.depth)
		bunDir := filepath.Dir(write(filepath.Join(home, ".bun", "bin", "bun"), "\x7fELF\x02\x01\x01\x00"))
		js := write(filepath.Join(home, ".bun", "install", "codex", "bin", "codex.js"), "#!/usr/bin/env node\n")
		wrapper := "#!/bin/bash\nexec \"" + bunDir + "/bun\" \"" + js + "\" \"$@\"\n"
		bin := write(filepath.Join(home, ".local", "codex", "bin", "codex"), wrapper)
		if c.want && len(wrapper) <= 512 || !c.want && len(wrapper) <= programHeadLimit {
			t.Fatalf("%s: the wrapper is only %d bytes", c.name, len(wrapper))
		}
		withResolved(t, bin)
		if got := ProgramIsKind("codex", "codex"); got != c.want {
			t.Errorf("%s (%d bytes): %v, want %v", c.name, len(wrapper), got, c.want)
		}
	}
}

// An absolute-node shebang does not depend on PATH (acfs-9hkh.86): a
// system node (an ELF named node, root-owned, in a root-owned directory,
// neither group- nor world-writable) is accepted under a systemd-like PATH
// without node; anything else needs to be the node on PATH.
func TestSystemNodeShebang(t *testing.T) {
	oldHead, oldResolve, oldRoot := ProgramHead, ResolveProgram, RootOwned
	ProgramHead = realProgramHead
	t.Cleanup(func() { ProgramHead, ResolveProgram, RootOwned = oldHead, oldResolve, oldRoot })
	root, err := os.MkdirTemp("/tmp", "nhm-node")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	node := func(dir, content string, dirMode os.FileMode) string {
		d := filepath.Join(root, dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, dirMode); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(d, "node")
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	elf := "\x7fELF\x02\x01\x01\x00"
	sys := node("usr/bin", elf, 0o755)
	groupDir := node("shared/bin", elf, 0o775)
	notELF := node("script/bin", "#!/bin/sh\n", 0o755)
	shebang := func(p string) []byte { return []byte("#!" + p + "\n") }

	// A systemd-like PATH: no node on it.
	ResolveProgram = func(string) (string, error) { return "", errors.New("node not on PATH") }
	RootOwned = func(os.FileInfo) bool { return true }
	for p, want := range map[string]bool{sys: true, groupDir: false, notELF: false} {
		if got := nodeShebang(shebang(p)); got != want {
			t.Errorf("systemd PATH, root-owned %s: %v, want %v", strings.TrimPrefix(p, root), got, want)
		}
	}
	RootOwned = func(os.FileInfo) bool { return false }
	if nodeShebang(shebang(sys)) {
		t.Error("systemd PATH: a node that root does not own was accepted")
	}
	// A shell PATH whose node is that file: accepted whoever owns it.
	ResolveProgram = func(string) (string, error) { return filepath.EvalSymlinks(sys) }
	if !nodeShebang(shebang(sys)) {
		t.Error("shell PATH: the node on PATH was refused")
	}
}

func TestProgramContent(t *testing.T) {
	old := ProgramHead
	ProgramHead = realProgramHead
	t.Cleanup(func() { ProgramHead = old })
	// An absolute root whatever TMPDIR is: with a relative TMPDIR, t.TempDir
	// is relative, and the bun wrapper (rightly) takes only absolute paths.
	root, err := os.MkdirTemp("/tmp", "nhm-kinds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
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
	if p, _ := r.Get("pi"); !p.GuardSupported {
		t.Fatal("pi has no guard (nhm-dyr)")
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

// claudeTrustWrapped is the body of Claude Code's folder-trust dialog in a
// pane of each width, as Claude Code wraps it (E18, nhm-za8): the question
// wraps in a pane under about 70 columns, the answers do not.
var claudeTrustWrapped = map[int]string{
	40: `
 Accessing workspace:

 /srv/project

 Quick safety check: Is this a project
 you created or one you trust? (Like
 your own code, a well-known open
 source project, or work from your
 team). If not, take a moment to review
 what's in this folder first.

 Claude Code'll be able to read, edit,
 and execute files here.

 Security guide

`,
	60: `
 Accessing workspace:

 /srv/project

 Quick safety check: Is this a project you created or one
 you trust? (Like your own code, a well-known open source
 project, or work from your team). If not, take a moment to
 review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files
 here.

 Security guide

`,
}

// codexHooksReview is codex-cli 0.162.1's dialog when a hook in its
// hooks.json is new or changed, as herdr 0.9.3 reads the screen (ACFS
// acfs-akb2, nhm-5as).
const codexHooksReview = `
  Hooks need review
  1 hook is new or changed.
  Hooks can run outside the sandbox after you trust them.


› 1. Review hooks
  2. Trust all and continue
  3. Continue without trusting (hooks won't run)

  enter confirm · esc skip
`

func TestCodexDialog(t *testing.T) {
	x, _ := Get("codex")
	if got := x.Dialog(codexHooksReview); !strings.HasPrefix(got, "Codex's 'Hooks need review' dialog") {
		t.Fatalf("hooks dialog = %q", got)
	}
	// Wrapped in a narrow pane, it reads the same.
	narrow := strings.Replace(codexHooksReview, "Continue without trusting", "Continue without\n     trusting", 1)
	if got := x.Dialog(narrow); got == "" {
		t.Fatal("the wrapped hooks dialog is not named")
	}
	for _, screen := range []string{"Trust this folder?\n› 1. Trust and continue\n  2. Quit\n", "› Ask Codex to do anything\n"} {
		if got := x.Dialog(screen); got != "" {
			t.Fatalf("%q names %q", screen, got)
		}
	}
}

// claudeBackgroundWork is the end of a 120x40 pane at Claude Code
// 2.1.309's dialog when /exit finds background work, as herdr 0.9.5
// read it in the sandbox (nhm-m7x); blank lines above the dialog cut.
const claudeBackgroundWork = `
❯ mock-tool: {"name": "Bash", "input": {"command": "sleep 600", "run_in_background": true,
  "description": "Wait for mail"}}

● mock reply 1

  Ran 1 shell command

● mock: read 1 tool result(s)

✻ Sautéed for 3s · done 11:03 PM · 1 shell still running


▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔
   Background work is running
   The following will stop when you exit:

   shell · sleep 600

   ❯ 1. Exit and stop tasks
     2. Move to background and exit
     3. Stay

   Enter to confirm · Esc to cancel
`

// claudeBackgroundWorkShort is the same dialog in an 80x16 pane, whole:
// Claude Code shows the first answer only.
const claudeBackgroundWorkShort = `

● mock: read 1 tool result(s)

✻ Cogitated for 3s · done 11:03 PM · 1 shell still
  running
▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔
   Background work is running
   The following will stop when you exit:

   shell · sleep 600

   ❯ 1. Exit and stop tasks

   Enter to confirm · Esc to cancel
`

// claudeQuotesTheDialog is an idle Claude Code whose output quotes the
// dialog, whole, above its prompt box, as the sandbox showed the box:
// no dialog is open.
const claudeQuotesTheDialog = `
● The bead says that /exit opened this dialog:

  Background work is running
  The following will stop when you exit:
     shell · until out=$(am check-inbox --agent IcyKnoll ...
  ❯ 1. Exit and stop tasks
    2. Move to background and exit
    3. Stay
  Enter to confirm · Esc to cancel

──────────────────────────────────────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on · 1 shell · ↓ to manage
`

func TestExitDialog(t *testing.T) {
	c, _ := Get("claude")
	name, answer, keys := c.ExitDialog(claudeBackgroundWork)
	if !strings.HasPrefix(name, "Claude's 'Background work is running' dialog") || answer != "Exit and stop tasks" || !slices.Equal(keys, []string{"enter"}) {
		t.Fatalf("background work = %q %q %v", name, answer, keys)
	}
	// A short pane, a narrow one that wraps the title, and trailing blank
	// lines read the same.
	wrapped := strings.Replace(claudeBackgroundWorkShort, "Background work is running", "Background work is\n   running", 1)
	for _, s := range []string{claudeBackgroundWorkShort, wrapped, claudeBackgroundWork + "\n   \n\n"} {
		if n, a, k := c.ExitDialog(s); n != name || a != answer || !slices.Equal(k, keys) {
			t.Fatalf("%q = %q %q %v", s, n, a, k)
		}
	}
	// With the cursor on another answer, nhm names the dialog and does
	// not answer it.
	stay := strings.Replace(strings.Replace(claudeBackgroundWork, "❯ 1.", "  1.", 1), "  3. Stay", "❯ 3. Stay", 1)
	if n, a, k := c.ExitDialog(stay); n != name || a != "" || k != nil {
		t.Fatalf("cursor on Stay = %q %q %v", n, a, k)
	}
	// Only a dialog that ends the screen counts: the words quoted above
	// the prompt box, with the cursor line or without it, give none, and
	// so does another dialog that ends the screen below the quote.
	other := claudeQuotesTheDialog[:strings.Index(claudeQuotesTheDialog, "\n───")] + "\n" + strings.Repeat("▔", 40) +
		"\n Bash command\n\n   make build\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Enter to confirm · Esc to cancel\n"
	for _, s := range []string{claudeQuotesTheDialog, strings.Replace(claudeQuotesTheDialog, "  ❯ 1. Exit and stop tasks\n", "", 1), other,
		"❯ \n  ? for shortcuts\n", ""} {
		if n, a, k := c.ExitDialog(s); n != "" || a != "" || k != nil {
			t.Fatalf("%q = %q %q %v", s, n, a, k)
		}
	}
	// Codex's /quit opened no dialog in the sandbox, in a turn or with a
	// background terminal: Codex names none.
	if x, _ := Get("codex"); x.ExitDialog != nil {
		t.Fatal("codex names an exit dialog")
	}
}

func TestTrustKeysWrapped(t *testing.T) {
	c, _ := Get("claude")
	x, _ := Get("codex")
	no := " ❯ No, exit\n   Yes, I trust this folder"
	yes := "   No, exit\n ❯ Yes, I trust this folder"
	for cols, body := range claudeTrustWrapped {
		screen := func(answers string) string {
			return "$ claude --dangerou\nsly-skip-permissions --effort xhigh\n\n" + strings.Repeat("─", cols) + body +
				answers + "\n\n Enter to confirm · Esc to cancel\n"
		}
		if got := c.TrustKeys(screen(no)); !slices.Equal(got, []string{"down", "enter"}) {
			t.Errorf("claude at %d columns, cursor on no = %v", cols, got)
		}
		if got := c.TrustKeys(screen(yes)); !slices.Equal(got, []string{"enter"}) {
			t.Errorf("claude at %d columns, cursor on yes = %v", cols, got)
		}
		if got := c.TrustKeys(screen(" ❯ Something else")); got != nil {
			t.Errorf("claude at %d columns, no trust answers = %v", cols, got)
		}
		if got := x.TrustKeys(screen(no)); got != nil {
			t.Errorf("codex answered claude's dialog at %d columns: %v", cols, got)
		}
	}
	codex40 := "  /srv/project\n  Trust this folder? Codex can read,\n  edit, and run files here.\n› 1. Trust and continue\n  2. Quit\n"
	if got := x.TrustKeys(codex40); !slices.Equal(got, []string{"enter"}) {
		t.Errorf("codex at 40 columns = %v", got)
	}
	// Borders drop out: a boxed question reads as one line.
	boxed := "╭────────────╮\n│ Is this a  │\n│ project?   │\n╰────────────╯\n"
	if got := flatScreen(boxed); got != "Is this a project?" {
		t.Errorf("flatScreen(boxed) = %q", got)
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
