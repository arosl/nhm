package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// fakeAcfs puts an old ACFS's acfs command first on PATH: its `agents
// export` says the Bash keeps nothing, and every call leaves a mark. nhm
// must never run it (nhm-te0), and never take its word over the Bash's
// files.
func fakeAcfs(t *testing.T, dir string) (called string) {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	called = filepath.Join(dir, "acfs-called")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >>'" + called + "'\n" +
		"echo '{\"format\":\"acfs-agents-state\",\"version\":1,\"exported_at\":\"x\",\"tokens\":[],\"wake\":[],\"keep\":[]}'\n"
	if err := os.WriteFile(filepath.Join(bin, "acfs"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ACFS_BIN", filepath.Join(bin, "acfs"))
	return called
}

// TestTakeoverVerb drives the verb on a server with no agents and no Bash
// state (a fresh install): no Agent Mail call is made, and no acfs
// command runs. handback is gone (nhm-te0).
func TestTakeoverVerb(t *testing.T) {
	s, inst := cliInstance(t)
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": []any{}, "agents": []any{},
			"workspaces": []any{}}}, nil
	})
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("ACFS_HOME", "")
	called := fakeAcfs(t, dir)
	sock := s.SocketPath

	if code, _, _ := run("admin", "takeover", "--socket", sock); code != 2 {
		t.Fatalf("takeover without --dry-run or --yes = %d", code)
	}
	code, out, errOut := run("admin", "takeover", "--dry-run", "--socket", sock)
	if code != 0 || !strings.Contains(out, "would pass") {
		t.Fatalf("dry run = %d %s %s", code, out, errOut)
	}
	if o, _ := core.ReadOwner(inst.Dir); o.Owner != core.OwnerBash {
		t.Fatalf("the dry run switched: %+v", o)
	}
	code, out, errOut = run("admin", "takeover", "--yes", "--socket", sock)
	if code != 0 || !strings.Contains(out, "owner: nhm (generation 1)") {
		t.Fatalf("takeover = %d %s %s", code, out, errOut)
	}
	if code, _, errOut = run("admin", "takeover", "--yes", "--socket", sock); code != 2 || !strings.Contains(errOut, "already nhm") {
		t.Fatalf("a second takeover = %d %s", code, errOut)
	}
	if code, _, _ = run("admin", "handback", "--yes", "--socket", sock); code != 2 {
		t.Fatalf("handback = %d, want the unknown admin verb's 2", code)
	}
	if o, _ := core.ReadOwner(inst.Dir); o.Owner != core.OwnerNhm || o.Generation != 1 {
		t.Fatalf("owner = %+v", o)
	}
	if data, err := os.ReadFile(called); !os.IsNotExist(err) {
		t.Fatalf("nhm ran acfs: %q %v", data, err)
	}
}

// TestTakeoverExcludeVerb (acfs-9hkh.80): a token no binding takes refuses
// the takeover with exit 2, in the dry run and with --yes, and the owner
// stays the Bash; --exclude names it, and the takeover passes and lists
// it. nhm-te0: the token is a file in ~/.acfs/state, which the takeover
// reads itself, over an acfs whose export says the Bash keeps nothing.
func TestTakeoverExcludeVerb(t *testing.T) {
	s, inst := cliInstance(t)
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": []any{}, "agents": []any{},
			"workspaces": []any{}}}, nil
	})
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("ACFS_HOME", "")
	tokens := filepath.Join(dir, ".acfs", "state", "tokens", "0123456789abcdef")
	if err := os.MkdirAll(tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{".project": dir + "/work\n", "RainyBasin": "tok-rainy\n"} {
		if err := os.WriteFile(filepath.Join(tokens, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	called := fakeAcfs(t, dir)
	sock := s.SocketPath

	for _, mode := range []string{"--dry-run", "--yes"} {
		code, _, errOut := run("admin", "takeover", mode, "--socket", sock)
		if code != 2 || !strings.Contains(errOut, "the token of RainyBasin is not imported") || !strings.Contains(errOut, "--exclude") {
			t.Fatalf("takeover %s = %d %s", mode, code, errOut)
		}
		if o, _ := core.ReadOwner(inst.Dir); o.Owner != core.OwnerBash || o.Generation != 0 {
			t.Fatalf("takeover %s switched: %+v", mode, o)
		}
	}
	code, out, errOut := run("admin", "takeover", "--yes", "--exclude", "rainybasin", "--socket", sock, "--json")
	if code != 0 {
		t.Fatalf("takeover --exclude = %d %s %s", code, out, errOut)
	}
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	if ex, _ := res["excluded"].([]any); len(ex) != 1 || ex[0] != "RainyBasin" {
		t.Fatalf("excluded = %v", res["excluded"])
	}
	if o, _ := core.ReadOwner(inst.Dir); o.Owner != core.OwnerNhm {
		t.Fatalf("owner = %+v", o)
	}
	if _, err := os.Stat(filepath.Join(tokens, "RainyBasin")); err != nil {
		t.Fatalf("the takeover touched the Bash's token: %v", err)
	}
	if data, err := os.ReadFile(called); !os.IsNotExist(err) {
		t.Fatalf("nhm ran acfs: %q %v", data, err)
	}
}

// TestProjectOfOutsideGit (acfs-9hkh.80): a pane outside any git work
// tree has its own folder as its project, as the Bash keys an agent
// spawned there; inside a work tree, the tree's root.
func TestProjectOfOutsideGit(t *testing.T) {
	ctx := context.Background()
	// Under /tmp, not t.TempDir(): a TMPDIR inside a git work tree would
	// put "outside git" inside one.
	tmpDir := func() string {
		dir, err := os.MkdirTemp("/tmp", "nhm-projectof-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	plain := tmpDir()
	key, id, err := projectOf(ctx, plain)
	if real, _ := filepath.EvalSymlinks(plain); err != nil || key != real || id == "" {
		t.Fatalf("outside git = %q %q %v", key, id, err)
	}
	// projectAt takes the folder itself, also inside a work tree.
	repo := tmpDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	key, _, err = projectOf(ctx, sub)
	if real, _ := filepath.EvalSymlinks(repo); err != nil || key != real {
		t.Fatalf("inside git = %q %v", key, err)
	}
	key, _, err = projectAt(ctx, sub)
	if real, _ := filepath.EvalSymlinks(sub); err != nil || key != real {
		t.Fatalf("projectAt in a work tree = %q %v", key, err)
	}
}
