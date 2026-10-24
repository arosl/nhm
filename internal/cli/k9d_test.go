package cli

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/ui"
)

// nhm version prints ntm's headline on line 1, "nhm version <v>": ACFS's
// export-config.sh reads field 3. ntm's five detail lines follow, then
// nhm's two (nhm-2p6.1.6; rule 4 replaces the F.7 line 2 of nhm-k9d).
func TestVersionHasNTMsHeadline(t *testing.T) {
	code, out, errOut := run("version")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if code != 0 || errOut != "" || len(lines) != 8 {
		t.Fatalf("version = %d %q %q", code, out, errOut)
	}
	if lines[0] != "nhm version "+Version || strings.Fields(lines[0])[2] != Version {
		t.Errorf("line 1 = %q, want field 3 to be %q", lines[0], Version)
	}
	for i, want := range []string{
		"  commit:    " + Commit,
		"  built:     " + Date,
		"  builder:   " + BuiltBy,
		"  go:        " + runtime.Version(),
		fmt.Sprintf("  platform:  %s/%s", runtime.GOOS, runtime.GOARCH),
		"  herdr:     protocol 22",
		fmt.Sprintf("  ntm:       %.8s", NTMCommit),
	} {
		if lines[i+1] != want {
			t.Errorf("line %d = %q, want %q", i+2, lines[i+1], want)
		}
	}
	if code, out, _ := run("--version"); code != 0 || !strings.HasPrefix(out, "nhm version "+Version+"\n") {
		t.Errorf("--version = %d %q", code, out)
	}
	// ntm's --short, -s: the version alone; --json wins over it.
	for _, flag := range []string{"--short", "-s"} {
		if code, out, errOut := run("version", flag); code != 0 || out != Version+"\n" || errOut != "" {
			t.Errorf("version %s = %d %q %q", flag, code, out, errOut)
		}
	}
	if code, out, _ := run("version", "--short", "--json"); code != 0 || !strings.HasPrefix(out, "{") {
		t.Errorf("version --short --json = %d %q", code, out)
	}
}

// nhm dashboard, dash and d are ntm's names for nhm board: the same
// help, and the same answer to the same arguments (the operator's F.7
// ruling, nhm-k9d). Only arguments that are refused before any herdr
// call are run: the board without them would open. nhm logs is ntm's
// logs since 2026-10-23 (logs_test.go).
func TestDashboardAndLogsAreAliases(t *testing.T) {
	for _, c := range []struct {
		alias, verb string
		calls       [][]string
	}{
		{"dashboard", "board", [][]string{{"--help"}, {"one", "two"}, {"--no-tui"}}},
		{"dash", "board", [][]string{{"--help"}, {"one", "two"}}},
		{"d", "board", [][]string{{"--help"}, {"one", "two"}}},
	} {
		for _, args := range c.calls {
			code, out, errOut := run(append([]string{c.alias}, args...)...)
			wantCode, wantOut, wantErr := run(append([]string{c.verb}, args...)...)
			if code != wantCode || out != wantOut || errOut != wantErr {
				t.Errorf("nhm %s %v = %d %q %q; nhm %s gives %d %q %q", c.alias, args, code, out, errOut, c.verb, wantCode, wantOut, wantErr)
			}
		}
		if _, out, _ := run(c.alias, "--help"); !strings.Contains(out, c.alias) {
			t.Errorf("nhm %s --help does not name it:\n%s", c.alias, out)
		}
	}
}

// spawn refuses ACFS's --count form and several sessions in one call, as
// ntm 9d5ed977 does: their row is waived (the operator's ruling, nhm-k9d,
// 2026-10-23).
func TestSpawnRefusesCountAndSeveralSessions(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	for _, args := range [][]string{
		{"spawn", "acfs-sim", "--count", "3", "--label", "swarm-3", "--dry-run"},
		{"spawn", "p1", "--cc=2", "p2", "--cod=1", "p3", "--agy=1"},
	} {
		if code, _, errOut := run(args...); code != exitRefused || !strings.Contains(errOut, "Usage: nhm spawn") {
			t.Errorf("%v = %d %q", args, code, errOut)
		}
	}
}

// ntm's dashboard <session>: the board shows that session's workspace,
// and the calling pane, which is another workspace's, is left out of its
// context (the operator's ruling, nhm-k9d, 2026-10-23).
func TestDashboardTakesASession(t *testing.T) {
	s, _ := zoomServer(t)
	h := herdr.New(s.SocketPath)
	caller := ui.Context{SocketPath: s.SocketPath, WorkspaceID: "w9", WorkspaceLabel: "mine", TabID: "w9:t1", PaneID: "w9:p1", Agent: "claude", Cwd: "/tmp"}
	for _, c := range []struct{ session, label string }{{"swarm", "swarm"}, {"w1", ""}} {
		got, err := boardContextFor(context.Background(), h, caller, c.session)
		if err != nil || got.WorkspaceID != "w1" || got.WorkspaceLabel != c.label || got.PaneID != "" || got.TabID != "" ||
			got.Agent != "" || got.Cwd != "/tmp" || got.SocketPath != s.SocketPath {
			t.Errorf("boardContextFor(%s) = %+v %v", c.session, got, err)
		}
	}
	// The calling pane's own workspace keeps the pane.
	caller.WorkspaceID = "w1"
	if got, err := boardContextFor(context.Background(), h, caller, "swarm"); err != nil || got.PaneID != "w9:p1" {
		t.Errorf("own workspace = %+v %v", got, err)
	}
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "")
	t.Setenv(ui.ContextEnv, "")
	for _, verb := range []string{"dashboard", "board", "d"} {
		code, out, errOut := run(verb, "nosuch")
		if code != exitRefused || out != "" || !strings.Contains(errOut, `session "nosuch": no such workspace (nhm list shows the sessions)`) {
			t.Errorf("%s nosuch = %d %q %q", verb, code, out, errOut)
		}
	}
}
