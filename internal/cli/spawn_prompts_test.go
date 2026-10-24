package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/arosl/nhm/internal/core/spawn"
)

// ntm's [prompts]: the inline default first, else the file, both trimmed;
// a file that cannot be read is ntm's error; other types have none
// (nhm-2p6.4.6).
func TestPromptsResolveForType(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cod.md")
	if err := os.WriteFile(file, []byte("\nRead AGENTS.md first.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := promptsConfig{CCDefault: "  Hi.  ", CCDefaultFile: "/nowhere", CodDefaultFile: file, AgyDefaultFile: "/nowhere/agy.md"}
	for _, c := range []struct{ typ, want, err string }{
		{"cc", "Hi.", ""},
		{"cod", "Read AGENTS.md first.", ""},
		{"agy", "", "reading prompts.agy_default_file: "},
		{"omp", "", ""},
		{"oc", "", ""},
	} {
		got, err := p.resolveForType(c.typ)
		if got != c.want || (c.err == "") != (err == nil) || (err != nil && !strings.HasPrefix(err.Error(), c.err)) {
			t.Errorf("%s = %q %v, want %q %q", c.typ, got, err, c.want, c.err)
		}
	}
	// A spawn's kinds by their short types: claude is cc, codex cod;
	// grok has no [prompts] key.
	d, err := promptsConfig{CCDefault: "A", CodDefault: "B", OmpDefault: "C"}.defaultPrompts(
		[]spawn.Want{{Kind: "claude", Count: 1}, {Kind: "codex", Count: 1}, {Kind: "grok", Count: 1}})
	if err != nil || len(d) != 2 || d["claude"] != "A" || d["codex"] != "B" {
		t.Fatalf("defaults = %v %v", d, err)
	}
}

// spawn takes the [prompts] defaults and add does not, as in ntm; with no
// prompt to send, neither waits for the agents, except with --assign or
// --trust-folder (nhm-2p6.4.6).
func TestSpawnRequestPrompts(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, []byte("[prompts]\ncc_default = \"Read AGENTS.md.\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NHM_CONFIG", config)
	parse := func(add bool, args ...string) spawn.Request {
		t.Helper()
		fs := pflag.NewFlagSet("spawn", pflag.ContinueOnError)
		fs.SetOutput(io.Discard)
		f := addSpawnFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		req, err := f.request("swarm", add)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	for _, c := range []struct {
		add      bool
		args     []string
		defaults int
		noWait   bool
	}{
		{false, []string{"--cc=1"}, 1, false},
		{false, []string{"--cod=1"}, 0, true},
		{false, []string{"--cod=1", "--prompt", "hi"}, 0, false},
		{false, []string{"--cc=1", "--no-kickoff"}, 0, true},
		{false, []string{"--cc=1", "--assign"}, 0, false},
		{false, []string{"--cod=1", "--trust-folder"}, 0, false},
		{true, []string{"--cc=1"}, 0, true},
		{true, []string{"--cc=1", "--prompt", "hi"}, 0, false},
	} {
		req := parse(c.add, c.args...)
		if len(req.DefaultPrompts) != c.defaults || req.NoWait != c.noWait {
			t.Errorf("add %v %v: defaults %v, no wait %v", c.add, c.args, req.DefaultPrompts, req.NoWait)
		}
	}
}
