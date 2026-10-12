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

// TestTakeoverHandbackVerbs drives both verbs against a fake acfs
// command, on a server with no agents: no Agent Mail call is made.
func TestTakeoverHandbackVerbs(t *testing.T) {
	s, inst := cliInstance(t)
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": []any{}, "agents": []any{},
			"workspaces": []any{}}}, nil
	})
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	imported := filepath.Join(dir, "imported.json")
	acfs := filepath.Join(dir, "acfs")
	script := "#!/bin/sh\n" +
		"case \"$2\" in\n" +
		"export) echo '{\"format\":\"acfs-agents-state\",\"version\":1,\"exported_at\":\"x\",\"tokens\":[],\"wake\":[],\"keep\":[]}' ;;\n" +
		"import) cp \"$3\" " + imported + " ;;\n" +
		"*) exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(acfs, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACFS_BIN", acfs)
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
	code, out, errOut = run("admin", "handback", "--yes", "--socket", sock, "--json")
	if code != 0 || decodeEnvelope(t, out)["result"].(map[string]any)["owner"].(map[string]any)["owner"] != core.OwnerBash {
		t.Fatalf("handback = %d %s %s", code, out, errOut)
	}
	if data, err := os.ReadFile(imported); err != nil || !strings.Contains(string(data), `"acfs-agents-state"`) {
		t.Fatalf("the Bash got %q %v", data, err)
	}
}

// TestTakeoverExcludeVerb (acfs-9hkh.80): a token no binding takes refuses
// the takeover with exit 2, in the dry run and with --yes, and the owner
// stays the Bash; --exclude names it, and the takeover passes and lists
// it. handback takes no --exclude.
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
	token := filepath.Join(dir, "RainyBasin")
	if err := os.WriteFile(token, []byte("tok-rainy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	acfs := filepath.Join(dir, "acfs")
	script := "#!/bin/sh\n" +
		"case \"$2\" in\n" +
		"export) echo '{\"format\":\"acfs-agents-state\",\"version\":1,\"exported_at\":\"x\",\"tokens\":[" +
		"{\"project_key\":\"" + dir + "/work\",\"project_hash\":\"0123456789abcdef\",\"name\":\"RainyBasin\",\"file\":\"" + token + "\"}]," +
		"\"wake\":[],\"keep\":[]}' ;;\n" +
		"*) exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(acfs, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACFS_BIN", acfs)
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
	if code, _, errOut = run("admin", "handback", "--dry-run", "--exclude", "x", "--socket", sock); code != 2 || !strings.Contains(errOut, "takeover flag") {
		t.Fatalf("handback --exclude = %d %s", code, errOut)
	}
}

// TestProjectOfOutsideGit (acfs-9hkh.80): a pane outside any git work
// tree has its own folder as its project, as the Bash keys an agent
// spawned there; inside a work tree, the tree's root.
func TestProjectOfOutsideGit(t *testing.T) {
	ctx := context.Background()
	plain := t.TempDir()
	key, id, err := projectOf(ctx, plain)
	if real, _ := filepath.EvalSymlinks(plain); err != nil || key != real || id == "" {
		t.Fatalf("outside git = %q %q %v", key, id, err)
	}
	repo := t.TempDir()
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
}
