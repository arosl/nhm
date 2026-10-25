package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// pureGoTOON makes the TOON encoder the pure Go one, whatever is on PATH:
// a TOON_TRU_BIN that is not toon_rust's tru makes ntm's encoder fall
// back to it.
func pureGoTOON(t *testing.T) {
	t.Helper()
	notTru := filepath.Join(t.TempDir(), "not-tru")
	if err := os.WriteFile(notTru, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOON_BIN", "")
	t.Setenv("TOON_TRU_BIN", notTru)
}

// --robot-format=toon writes a successful answer in TOON, as ntm does,
// from the flag, its alias or the environment; an error envelope stays
// JSON (nhm-2p6.2.11).
func TestRobotFormatTOON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tru is a POSIX shell script")
	}
	t.Setenv("HERDR_SOCKET_PATH", "")
	clearRobotFormatEnv(t)
	pureGoTOON(t)
	for _, c := range []struct {
		args []string
		env  string
	}{
		{[]string{"--robot-version", "--robot-format=toon"}, ""},
		{[]string{"--robot-version", "--robot-output-format=toon"}, ""},
		{[]string{"--robot-version"}, "toon"},
	} {
		t.Setenv("NHM_ROBOT_FORMAT", c.env)
		code, out, errOut := run(c.args...)
		if code != 0 || errOut != "" || json.Valid([]byte(out)) || !strings.Contains(out, "output_format: toon\n") ||
			!strings.Contains(out, "tmux_available: false\n") {
			t.Fatalf("%v with NHM_ROBOT_FORMAT=%q = %d %q %q", c.args, c.env, code, out, errOut)
		}
	}
	t.Setenv("NHM_ROBOT_FORMAT", "")
	env, code := robotRun(t, "--robot-version", "--robot-snapshot", "--robot-format=toon")
	if code != robotError || env.ErrorCode != "INVALID_FLAG" || env.Raw["output_format"] != "toon" {
		t.Fatalf("an error under toon = %d %v", code, env.Raw)
	}

	// With toon_rust's tru, tru encodes the answer's JSON.
	tru := filepath.Join(t.TempDir(), "tru")
	script := "#!/bin/sh\ncase \"$1\" in\n--help) echo 'toon_rust: the reference implementation in Rust' ;;\n" +
		"*) printf 'TRU:'; cat ;;\nesac\n"
	if err := os.WriteFile(tru, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOON_TRU_BIN", tru)
	code, out, _ := run("--robot-version", "--robot-format=toon")
	if json := strings.TrimPrefix(out, "TRU:"); code != 0 || json == out || !strings.Contains(json, `"output_format":"toon"`) {
		t.Fatalf("with tru = %d %q", code, out)
	}
}
