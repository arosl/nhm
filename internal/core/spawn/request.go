package spawn

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/integrations/kinds"
)

// Want is one --cc=N[:MODEL] or --agent KIND=N[:MODEL].
type Want struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Model string `json:"model,omitempty"`
}

// ParseCount reads "N" or "N:MODEL".
func ParseCount(kind, value string) (Want, error) {
	n, model, _ := strings.Cut(value, ":")
	count, err := strconv.Atoi(strings.TrimSpace(n))
	if err != nil || count < 0 || count > 32 {
		return Want{}, fmt.Errorf("--%s=%s: give a count from 0 to 32, optionally :MODEL", kind, value)
	}
	return Want{Kind: kind, Count: count, Model: strings.TrimSpace(model)}, nil
}

// ParseAgent reads --agent KIND=N[:MODEL].
func ParseAgent(value string) (Want, error) {
	kind, rest, ok := strings.Cut(value, "=")
	if !ok {
		return Want{}, fmt.Errorf("--agent %s: use KIND=N[:MODEL]", value)
	}
	return ParseCount(strings.TrimSpace(kind), rest)
}

// Stagger modes.
const (
	StaggerNone  = "none"
	StaggerFixed = "fixed"
	StaggerSmart = "smart"
)

// smartCap bounds a smart stagger's wait.
const smartCap = 120 * time.Second

// Request is one spawn, add or new.
type Request struct {
	// Session is the workspace label; WorkspaceID is set when it exists.
	Session     string
	WorkspaceID string
	// Add refuses a missing workspace instead of creating it.
	Add bool
	// ProjectRoot is the agents' cwd and the Agent Mail project key.
	ProjectRoot string
	ProjectID   string
	Agents      []Want
	// Label names the agents' tabs; Group is a token on the panes.
	Label, Group string
	// PerTab agents share one tab (pane.split); 1 by default.
	PerTab int
	// Prompt is the kickoff; empty means ACFS's default_new_agent
	// palette prompt, unless NoKickoff.
	Prompt    string
	NoKickoff bool
	// Enrich is the kickoff's CM and CASS enrichment (plan-v8 2.13:
	// kickoffs default to best-effort CM).
	Enrich send.Enrichment
	// Assign appends the next_bead prompt. The caller asked for the
	// explicit yes before it set this.
	Assign       bool
	StaggerMode  string
	StaggerDelay time.Duration
	ReadyTimeout time.Duration
	TrustFolder  bool
	Force        bool
	// AllowUnguarded records an exception for a missing guard, for this
	// launch only.
	AllowUnguarded bool
	// RequireObservations is the robot profile of admission.
	RequireObservations bool
	DryRun              bool
	OperationID         string
	Scope               any
}

// Validate checks the request against the kind registry.
func (r *Request) Validate() error {
	if r.Session == "" {
		return fmt.Errorf("name the session (a workspace label)")
	}
	total := 0
	for i, w := range r.Agents {
		a, ok := kinds.Get(w.Kind)
		if !ok {
			return fmt.Errorf("unknown kind %q: nhm knows %s", w.Kind, strings.Join(kinds.Names(), ", "))
		}
		if w.Model != "" && a.ModelFlag == "" {
			return fmt.Errorf("%s takes no model: its model is fixed outside nhm", w.Kind)
		}
		if w.Model != "" && !modelPattern.MatchString(w.Model) {
			return fmt.Errorf("invalid model %q", w.Model)
		}
		r.Agents[i].Kind = a.Name
		total += w.Count
	}
	if total == 0 {
		return fmt.Errorf("nothing to spawn: give --cc=N, --cod=N, --agy=N … or --agent KIND=N")
	}
	if r.PerTab <= 0 {
		r.PerTab = 1
	}
	switch r.StaggerMode {
	case "":
		r.StaggerMode = StaggerNone
		if r.StaggerDelay > 0 {
			r.StaggerMode = StaggerFixed
		}
	case StaggerNone, StaggerFixed, StaggerSmart:
	default:
		return fmt.Errorf("--stagger-mode is fixed or smart, not %q", r.StaggerMode)
	}
	if r.ReadyTimeout <= 0 {
		r.ReadyTimeout = 60 * time.Second
	}
	return nil
}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// Count is the number of agents the request starts.
func (r *Request) Count() int {
	n := 0
	for _, w := range r.Agents {
		n += w.Count
	}
	return n
}
