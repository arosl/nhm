package bindings

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/arosl/nhm/internal/herdr"
)

// Herdr is what this package needs from herdr; *herdr.Client has it.
type Herdr interface {
	Snapshot(ctx context.Context) (herdr.SessionSnapshot, error)
	PaneProcessInfo(ctx context.Context, paneID string) (herdr.ProcessInfo, error)
	AgentRename(ctx context.Context, target, name string) (herdr.AgentInfo, error)
}

// Labeler is what Label needs from herdr; *herdr.Client has it.
type Labeler interface {
	PaneRename(ctx context.Context, paneID, label string) (herdr.PaneInfo, error)
	TabGet(ctx context.Context, tabID string) (herdr.TabInfo, error)
	TabRename(ctx context.Context, tabID, label string) (herdr.TabInfo, error)
}

// EnvHerdr is what Env needs from herdr: Herdr, and the labels that
// adopt sets. *herdr.Client has it.
type EnvHerdr interface {
	Herdr
	Labeler
}

// ProcStart returns a process's start time in clock ticks since boot
// (field 22 of /proc/<pid>/stat). A variable so tests can replace it.
var ProcStart = func(pid int64) (int64, error) {
	data, err := os.ReadFile("/proc/" + strconv.FormatInt(pid, 10) + "/stat")
	if err != nil {
		return 0, err
	}
	// The command name, field 2, is in parentheses and may hold spaces.
	text := string(data)
	i := strings.LastIndexByte(text, ')')
	if i < 0 {
		return 0, fmt.Errorf("/proc/%d/stat: no command field", pid)
	}
	fields := strings.Fields(text[i+1:])
	// fields[0] is field 3 (state), so field 22 is fields[19].
	if len(fields) < 20 {
		return 0, fmt.Errorf("/proc/%d/stat: %d fields", pid, len(fields)+2)
	}
	return strconv.ParseInt(fields[19], 10, 64)
}

// Check results.
const (
	CheckOK      = "ok"
	CheckFail    = "fail"
	CheckUnknown = "unknown"
)

// Check is one comparison of a binding with herdr's view.
type Check struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Detail string `json:"detail"`
}

// Verdict sums up a verification.
type Verdict string

const (
	// Verified: the pane holds an agent of the binding's kind, no check
	// failed, and the alias, the native session or the process matches.
	Verified Verdict = "verified"
	// Unverified: nothing contradicts the binding, and nothing but the
	// kind confirms it.
	Unverified Verdict = "unverified"
	// Mismatch: a check failed; the pane may hold another occupant.
	Mismatch Verdict = "mismatch"
	// Absent: the pane is gone or holds no agent.
	Absent Verdict = "absent"
	// NotActive: the binding is displaced or retired; nothing was checked.
	NotActive Verdict = "not_active"
)

// Verification is how nhm checked a binding against herdr.
type Verification struct {
	Verdict     Verdict           `json:"verdict"`
	Checks      []Check           `json:"checks"`
	AgentStatus herdr.AgentStatus `json:"agent_status,omitempty"`
	AgentName   string            `json:"herdr_name,omitempty"`
}

// Verify compares an active binding with herdr's snapshot of its pane.
func Verify(ctx context.Context, h Herdr, b Binding) (Verification, error) {
	if b.Status != Active || b.PaneID == "" {
		return Verification{Verdict: NotActive, Checks: []Check{}}, nil
	}
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return Verification{}, fmt.Errorf("bindings: verify %s: %w", b.ID, err)
	}
	return verifyIn(ctx, h, b, snap), nil
}

func verifyIn(ctx context.Context, h Herdr, b Binding, snap herdr.SessionSnapshot) Verification {
	v := Verification{Checks: []Check{}}
	add := func(name, result, detail string) { v.Checks = append(v.Checks, Check{name, result, detail}) }
	if _, ok := findPane(snap, b.PaneID); !ok {
		add("pane", CheckFail, b.PaneID+" is not in herdr's session")
		v.Verdict = Absent
		return v
	}
	agent, ok := findAgent(snap, b.PaneID)
	if !ok || agent.Agent == "" {
		add("occupant", CheckFail, "no agent in "+b.PaneID)
		v.Verdict = Absent
		return v
	}
	v.AgentStatus, v.AgentName = agent.AgentStatus, agent.Name
	if agent.Agent == b.Kind {
		add("kind", CheckOK, agent.Agent)
	} else {
		add("kind", CheckFail, fmt.Sprintf("herdr sees %s, the binding has %s", agent.Agent, b.Kind))
	}
	switch {
	case b.HerdrAlias == "":
		add("alias", CheckUnknown, "the binding has no alias")
	case agent.Name == b.HerdrAlias:
		add("alias", CheckOK, agent.Name)
	case agent.Name == "":
		// herdr clears the alias at its own SessionStart (a compaction or
		// a resume); the hooks apply it again.
		add("alias", CheckUnknown, "herdr shows no alias")
	default:
		add("alias", CheckFail, fmt.Sprintf("herdr shows %s, the binding has %s", agent.Name, b.HerdrAlias))
	}
	switch {
	case b.NativeSession == nil || agent.AgentSession == nil:
		add("session", CheckUnknown, "no native session on one side")
	case agent.AgentSession.Value == b.NativeSession.Value:
		add("session", CheckOK, agent.AgentSession.Value)
	default:
		add("session", CheckFail, fmt.Sprintf("herdr has %s, the binding has %s", agent.AgentSession.Value, b.NativeSession.Value))
	}
	result, detail := checkProcess(ctx, h, b)
	add("process", result, detail)

	v.Verdict = Unverified
	for _, c := range v.Checks {
		if c.Result == CheckFail {
			v.Verdict = Mismatch
			return v
		}
	}
	for _, c := range v.Checks {
		if c.Name != "kind" && c.Result == CheckOK {
			v.Verdict = Verified
		}
	}
	return v
}

func checkProcess(ctx context.Context, h Herdr, b Binding) (string, string) {
	if b.ForegroundPID == 0 {
		return CheckUnknown, "the binding has no process"
	}
	info, err := h.PaneProcessInfo(ctx, b.PaneID)
	if err != nil {
		return CheckUnknown, "pane.process_info: " + err.Error()
	}
	for _, p := range info.ForegroundProcesses {
		if int64(p.PID) != b.ForegroundPID {
			continue
		}
		if b.ForegroundStart == 0 {
			return CheckOK, fmt.Sprintf("pid %d (no start time recorded)", p.PID)
		}
		start, err := ProcStart(b.ForegroundPID)
		if err != nil {
			return CheckUnknown, fmt.Sprintf("pid %d: %v", p.PID, err)
		}
		if start != b.ForegroundStart {
			return CheckFail, fmt.Sprintf("pid %d started at %d, the binding has %d (pid reused)", p.PID, start, b.ForegroundStart)
		}
		return CheckOK, fmt.Sprintf("pid %d started at %d", p.PID, start)
	}
	return CheckFail, fmt.Sprintf("pid %d is no longer in the pane's foreground", b.ForegroundPID)
}

// processFacts reads the pane's first foreground process and its start
// time; zeros when herdr or /proc cannot tell.
func processFacts(ctx context.Context, h Herdr, paneID string) (pid, start int64) {
	info, err := h.PaneProcessInfo(ctx, paneID)
	if err != nil || len(info.ForegroundProcesses) == 0 {
		return 0, 0
	}
	pid = int64(info.ForegroundProcesses[0].PID)
	if s, err := ProcStart(pid); err == nil {
		start = s
	}
	return pid, start
}

func findPane(snap herdr.SessionSnapshot, paneID string) (herdr.PaneInfo, bool) {
	for _, p := range snap.Panes {
		if p.PaneID == paneID {
			return p, true
		}
	}
	return herdr.PaneInfo{}, false
}

func findAgent(snap herdr.SessionSnapshot, paneID string) (herdr.AgentInfo, bool) {
	for _, a := range snap.Agents {
		if a.PaneID == paneID {
			return a, true
		}
	}
	return herdr.AgentInfo{}, false
}

func findTab(snap herdr.SessionSnapshot, tabID string) (herdr.TabInfo, bool) {
	for _, t := range snap.Tabs {
		if t.TabID == tabID {
			return t, true
		}
	}
	return herdr.TabInfo{}, false
}

// Query names the binding to resolve: a mail name, a herdr alias or a
// pane id ("w1:p2"), or the calling pane (Current, from HERDR_PANE_ID).
type Query struct {
	Target  string
	Current bool
	// ProjectKey limits a name to one project; empty searches them all.
	ProjectKey string
}

// Resolution is a binding and how nhm verified it.
type Resolution struct {
	Binding      Binding      `json:"binding"`
	Verification Verification `json:"verification"`
}

// Resolve finds the binding a query names and verifies it against herdr
// (h may be nil: then nothing is verified). A name with no active binding
// resolves to its newest displaced or retired one.
func Resolve(ctx context.Context, s *Store, h Herdr, q Query, getenv func(string) string) (Resolution, error) {
	b, err := find(ctx, s, h, q, getenv)
	if err != nil {
		return Resolution{}, err
	}
	r := Resolution{Binding: b, Verification: Verification{Verdict: NotActive, Checks: []Check{}}}
	if h != nil {
		if r.Verification, err = Verify(ctx, h, b); err != nil {
			return Resolution{}, err
		}
	}
	return r, nil
}

func find(ctx context.Context, s *Store, h Herdr, q Query, getenv func(string) string) (Binding, error) {
	target := strings.TrimSpace(q.Target)
	if q.Current {
		target = getenv("HERDR_PANE_ID")
		if target == "" {
			return Binding{}, errors.New("bindings: --current needs HERDR_PANE_ID (run it in a herdr pane)")
		}
	}
	if target == "" {
		return Binding{}, errors.New("bindings: name a mail name, a herdr name or a pane id")
	}
	// Pane ids hold a colon; mail names and aliases never do. A moved
	// pane's old id (the moved agent's own HERDR_PANE_ID) finds it too.
	if strings.Contains(target, ":") {
		return s.ActiveByPane(ctx, CallerPane(ctx, s, h, target))
	}
	b, err := s.ActiveByName(ctx, q.ProjectKey, target)
	if !errors.Is(err, ErrNotFound) {
		return b, err
	}
	if b, err = s.ActiveByAlias(ctx, strings.ToLower(target)); !errors.Is(err, ErrNotFound) {
		return b, err
	}
	list, err := s.List(ctx, Filter{ProjectKey: q.ProjectKey, MailName: target})
	if err != nil {
		return Binding{}, err
	}
	if len(list) == 0 {
		return Binding{}, fmt.Errorf("%w: %s", ErrNotFound, target)
	}
	return list[0], nil
}
