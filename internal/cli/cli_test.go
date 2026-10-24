package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func run(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionJSON(t *testing.T) {
	code, out, errOut := run("version", "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	var info VersionInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, out)
	}
	if info.Version == "" || info.NTMCommit != NTMCommit || info.HerdrProtocol != 22 {
		t.Fatalf("info = %+v", info)
	}
}

func TestVersionText(t *testing.T) {
	code, out, _ := run("version")
	if code != 0 || !strings.HasPrefix(out, "nhm version ") || !strings.Contains(out, "herdr protocol 22") {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
}

// ntm's --version and -V print the one line "ntm version X" (cobra's
// version flag with ntm's template).
func TestVersionFlagPrintsOneLine(t *testing.T) {
	for _, flag := range []string{"--version", "-V"} {
		code, out, errOut := run(flag)
		if code != 0 || out != "nhm version "+Version+"\n" || errOut != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", flag, code, out, errOut)
		}
	}
}

// ntm exits 1 for a usage error, with "Error: <msg>" on stderr and
// nothing on stdout.
func TestUsageErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"frobnicate"}, `Error: unknown command "frobnicate" for "nhm"` + "\n"},
		{[]string{"version", "--yaml"}, "Error: unknown flag: --yaml\n"},
	} {
		code, out, errOut := run(c.args...)
		if code != 1 || out != "" || errOut != c.want {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", c.args, code, out, errOut)
		}
	}
	if code, out, _ := run("help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("help: exit %d, stdout %q", code, out)
	}
}

// Bare nhm prints the usage on stdout and exits 0, as bare ntm prints its
// help.
func TestBareRootPrintsUsage(t *testing.T) {
	code, out, errOut := run()
	if code != 0 || out != usage || errOut != "" {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}
