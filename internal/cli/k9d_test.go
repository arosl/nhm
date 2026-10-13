package cli

import (
	"fmt"
	"strings"
	"testing"
)

// nhm version prints ntm's headline on line 1, "nhm version <v>": ACFS's
// export-config.sh reads field 3. Line 2 holds the commit, the herdr
// protocol and the ntm commit (the operator's F.7 ruling, nhm-k9d).
func TestVersionHasNTMsHeadline(t *testing.T) {
	code, out, errOut := run("version")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if code != 0 || errOut != "" || len(lines) != 2 {
		t.Fatalf("version = %d %q %q", code, out, errOut)
	}
	if lines[0] != "nhm version "+Version || strings.Fields(lines[0])[2] != Version {
		t.Errorf("line 1 = %q, want field 3 to be %q", lines[0], Version)
	}
	if want := fmt.Sprintf("commit %s, herdr protocol 22, ntm %.8s", Commit, NTMCommit); lines[1] != want {
		t.Errorf("line 2 = %q, want %q", lines[1], want)
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

// nhm dashboard and nhm logs are ntm's names for nhm board and nhm read:
// the same help, and the same answer to the same arguments (the
// operator's F.7 ruling, nhm-k9d). Only arguments that are refused
// before any herdr call are run: the board without them would open.
func TestDashboardAndLogsAreAliases(t *testing.T) {
	for _, c := range []struct {
		alias, verb string
		calls       [][]string
	}{
		{"dashboard", "board", [][]string{{"--help"}, {"session"}}},
		{"dash", "board", [][]string{{"--help"}, {"session"}}},
		{"d", "board", [][]string{{"--help"}, {"session"}}},
		{"logs", "read", [][]string{{"--help"}, {}, {"one", "two", "three"}}},
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
