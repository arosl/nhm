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

// MailAgentWarning warns when the bound agent's process names another
// agent to ACFS's Agent Mail hook and am: a pane that spawn made for one
// agent, reused by hand for another, keeps the first agent's
// AGENT_MAIL_AGENT in its shell (nhm-a73). It reads the first of the
// variables that is set, as they do. "" when none names another agent,
// or when the process cannot be read or is no longer the bound one.
func MailAgentWarning(b Binding) string {
	if b.ForegroundPID <= 0 {
		return ""
	}
	env, err := ProcEnviron(b.ForegroundPID)
	if err != nil {
		return ""
	}
	if b.ForegroundStart != 0 {
		if start, err := ProcStart(b.ForegroundPID); err != nil || start != b.ForegroundStart {
			return ""
		}
	}
	for _, key := range mailAgentVars {
		v := env[key]
		if v == "" {
			continue
		}
		if strings.EqualFold(v, b.MailName) {
			return ""
		}
		return fmt.Sprintf("this pane's shell sets %s=%s: ACFS's mail hook and am will act as %s, not %s; "+
			"start %s in a fresh pane (nhm add), or unset %s before you start it", key, v, v, b.MailName, b.MailName, key)
	}
	return ""
}
