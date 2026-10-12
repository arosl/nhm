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
	code, out, _ := run("--version")
	if code != 0 || !strings.HasPrefix(out, "nhm ") || !strings.Contains(out, "herdr protocol 22") {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"frobnicate"}, {"version", "--yaml"}} {
		code, out, errOut := run(args...)
		if code != 2 || out != "" || errOut == "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
	if code, out, _ := run("help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("help: exit %d, stdout %q", code, out)
	}
}
