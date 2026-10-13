package bindings

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeEnviron makes ProcEnviron answer from envs, by pid.
func fakeEnviron(t *testing.T, envs map[int64]map[string]string) {
	t.Helper()
	old := ProcEnviron
	ProcEnviron = func(pid int64) (map[string]string, error) {
		if e, ok := envs[pid]; ok {
			return e, nil
		}
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { ProcEnviron = old })
}

// The first of AGENT_MAIL_AGENT and AGENT_NAME that is set decides, as
// in ACFS's mail hook: another agent's name warns; the agent's own name
// (in any case), no variable, an unreadable process or a reused pid do
// not (nhm-a73).
func TestMailAgentWarning(t *testing.T) {
	fakeProcStart(t, map[int64]int64{100: 7, 101: 7, 102: 7, 103: 7, 104: 7, 105: 7})
	fakeEnviron(t, map[int64]map[string]string{
		100: {"AGENT_MAIL_AGENT": "OldFox", "PATH": "/bin"},
		101: {"AGENT_MAIL_AGENT": "newowl"},
		102: {"PATH": "/bin"},
		103: {"AGENT_NAME": "OldFox"},
		104: {"AGENT_MAIL_AGENT": "NewOwl", "AGENT_NAME": "OldFox"},
		105: {"AGENT_MAIL_AGENT": "", "AGENT_NAME": "OldFox"},
	})
	for _, c := range []struct {
		pid, start int64
		want       string
	}{
		{100, 7, "AGENT_MAIL_AGENT=OldFox"},
		{101, 7, ""},
		{102, 7, ""},
		{103, 7, "AGENT_NAME=OldFox"},
		{104, 7, ""},
		{105, 7, "AGENT_NAME=OldFox"},
		{100, 9, ""}, // the pid now names another process
		{100, 0, "AGENT_MAIL_AGENT=OldFox"},
		{999, 0, ""}, // gone
		{0, 0, ""},
	} {
		got := MailAgentWarning(Binding{MailName: "NewOwl", ForegroundPID: c.pid, ForegroundStart: c.start})
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("pid %d start %d: %q, want %q", c.pid, c.start, got, c.want)
		}
	}
	got := MailAgentWarning(Binding{MailName: "NewOwl", ForegroundPID: 100, ForegroundStart: 7})
	if !strings.Contains(got, "act as OldFox, not NewOwl") || !strings.Contains(got, "nhm add") {
		t.Fatalf("warning = %q", got)
	}
}

// ProcEnviron reads this process's own environment as it started.
func TestProcEnvironReadsSelf(t *testing.T) {
	env, err := ProcEnviron(int64(os.Getpid()))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("no /proc here")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["PATH"]; !ok {
		t.Fatalf("no PATH in %d variables", len(env))
	}
}
