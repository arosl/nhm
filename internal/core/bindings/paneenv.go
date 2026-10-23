package bindings

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// The variables that name an agent to ACFS's Agent Mail hook and to am,
// in the order they read them, before the tab label. Spawn sets
// AGENT_MAIL_AGENT in each pane it makes (nhm-4c4).
var mailAgentVars = []string{"AGENT_MAIL_AGENT", "AGENT_NAME"}

// ProcEnviron reads a process's environment as it started, from
// /proc/<pid>/environ.
var ProcEnviron = func(pid int64) (map[string]string, error) {
	data, err := os.ReadFile("/proc/" + strconv.FormatInt(pid, 10) + "/environ")
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, kv := range bytes.Split(data, []byte{0}) {
		if k, v, ok := strings.Cut(string(kv), "="); ok {
			env[k] = v
		}
	}
	return env, nil
}

// EvidencePaneEnv is the evidence kind of a MailAgentWarning on a new
// binding: nhm-hook prints nothing an operator sees, and nhm resolve
// shows a binding's evidence.
const EvidencePaneEnv = "pane_env"

// MailAgentWarning warns when the agent process of an occupant, about to
// be bound as mailName, names another agent to ACFS's Agent Mail hook
// and am: a pane that spawn made for one agent, reused by hand for
// another, keeps the first agent's AGENT_MAIL_AGENT in its shell
// (nhm-a73). It reads the first of the variables that is set, as they
// do. "" when none names another agent, or when the process cannot be
// read or its start time shows another process; an unknown start time
// trusts the pid.
func MailAgentWarning(mailName string, occ Occupant) string {
	key, v, ok := MailAgentVar(occ)
	if !ok || key == "" || strings.EqualFold(v, mailName) {
		return ""
	}
	return fmt.Sprintf("this pane's shell sets %s=%s: ACFS's mail hook and am will act as %s, not %s; "+
		"start %s in a fresh pane (nhm add), or unset %s before you start it", key, v, v, mailName, mailName, key)
}

// MailAgentVar is the first of the variables that name an agent to
// ACFS's Agent Mail hook and am that the occupant's agent process set
// when it started, and its value; key is "" when none is set, and the
// hook then reads the tab label. ok is false when the process cannot be
// read, or its start time shows another process; an unknown start time
// trusts the pid.
func MailAgentVar(occ Occupant) (key, value string, ok bool) {
	if occ.ForegroundPID <= 0 {
		return "", "", false
	}
	env, err := ProcEnviron(occ.ForegroundPID)
	if err != nil {
		return "", "", false
	}
	if occ.ForegroundStart != 0 {
		if start, err := ProcStart(occ.ForegroundPID); err != nil || start != occ.ForegroundStart {
			return "", "", false
		}
	}
	for _, key := range mailAgentVars {
		if v := env[key]; v != "" {
			return key, v, true
		}
	}
	return "", "", true
}

// WithMailAgentWarning adds the occupant's MailAgentWarning to its
// evidence, and returns the warning ("" when there is none).
func WithMailAgentWarning(mailName string, occ *Occupant) string {
	w := MailAgentWarning(mailName, *occ)
	if w != "" {
		occ.Evidence = append(occ.Evidence, Evidence{Kind: EvidencePaneEnv, Detail: w})
	}
	return w
}
