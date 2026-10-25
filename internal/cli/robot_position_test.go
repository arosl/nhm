package cli

import (
	"testing"
)

// ntm takes a robot flag's own flags in front of it as well as after it:
// `--lines=5 --robot-tail=S` is a robot call, and a flag nhm does not
// know in front of a robot flag is a JSON INVALID_FLAG, not "unknown
// command" on stderr (nhm-2p6.2.5).
func TestRobotFlagAfterModifier(t *testing.T) {
	s := swarmInstance(t)
	sock := "--socket=" + s.SocketPath
	env, code := robotRun(t, "--lines=5", sock, "--robot-tail=swarm")
	if code != 0 || env.Raw["panes"] == nil {
		t.Fatalf("--lines=5 --socket --robot-tail = %d %+v", code, env.Raw)
	}
	env, code = robotRun(t, "--no-mail", "--json", sock, "--robot-status", "--robot-limit=1")
	if code != 0 || len(env.Raw["sessions"].([]any)) != 1 {
		t.Fatalf("--no-mail --json --socket --robot-status = %d %+v", code, env.Raw)
	}
	if env, code := robotRun(t, "--bogus", "--robot-status", sock); code != robotError || env.ErrorCode != "INVALID_FLAG" {
		t.Fatalf("--bogus --robot-status = %d %+v", code, env)
	}
	// A verb first is a verb call, whatever follows.
	if code, _, errOut := run("--lines=5", "status"); code == 0 || errOut == "" {
		t.Fatalf("--lines=5 status = %d %q", code, errOut)
	}
}

// An error envelope names the robot command in _meta.command, as ntm's
// EncodeErrorJSON does: the first --robot-* flag that is not a global
// modifier, else the first one (nhm-2p6.2.5).
func TestRobotErrorsNameTheCommand(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("NHM_ROBOT_FORMAT", "")
	for _, c := range []struct {
		args    []string
		command string
	}{
		{[]string{"--robot-format=xml", "--robot-status"}, "robot-status"},
		{[]string{"--bogus", "--robot-tail=s"}, "robot-tail"},
		{[]string{"--robot-status", "--robot-limit=-1", "--socket=/nonexistent"}, "robot-status"},
		{[]string{"--robot-format=xml"}, "robot-format"},
	} {
		env, code := robotRun(t, c.args...)
		if code == 0 || env.Raw["_meta"] == nil || env.Raw["_meta"].(map[string]any)["command"] != c.command {
			t.Errorf("%v = %d %v, want _meta.command %q", c.args, code, env.Raw["_meta"], c.command)
		}
	}
	if robotCommand != "" {
		t.Fatalf("robotCommand outlived the call: %q", robotCommand)
	}
}
