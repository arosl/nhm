package cli

import (
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
