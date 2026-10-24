package spawn

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/integrations/kinds"
)

// Want is one agent spec: --cc=N[:MODEL[:EFFORT]] or --agent KIND=SPEC.
type Want struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Model string `json:"model,omitempty"`
	// Effort overrides the request's effort for these agents. A kind
	// without an effort flag drops it at launch, as ntm does.
	Effort string `json:"effort,omitempty"`
}

// ParseAgent reads --agent KIND=SPEC, where SPEC is ParseCount's.
func ParseAgent(value string) (Want, error) {
	kind, rest, ok := strings.Cut(value, "=")
	if !ok {
		return Want{}, fmt.Errorf("--agent %s: use KIND=N[:MODEL[:EFFORT]]", value)
	}
	return ParseCount(strings.TrimSpace(kind), rest)
}

// Stagger modes.
const (
	StaggerNone  = "none"
	StaggerFixed = "fixed"
	StaggerSmart = "smart"
)

// Request is one spawn, add or new.
type Request struct {
	// Session is the workspace label; WorkspaceID is set when it exists.
	Session     string
	WorkspaceID string
	// Add refuses a missing workspace instead of creating it.
	Add bool
	// Safety refuses a workspace that exists already: --robot-spawn's
	// --spawn-safety, as ntm refuses to reuse a session (ErrSessionExists).
	Safety bool
	// ProjectRoot is the agents' cwd and the Agent Mail project key.
	ProjectRoot string
	ProjectID   string
	Agents      []Want
	// Effort is the reasoning effort of the kinds that take one; empty is
	// each kind's default (ntm's xhigh for Claude and Codex).
	Effort string
	// Label names the agents' tabs; Group is a token on the panes.
	Label, Group string
	// Role is what the agents are spawned for: "" (they work beads) or
	// "reviewer" (reap may retire one that worked none).
	Role string
	// PerTab is the most agents of the project that share one tab, a
	// pane each (pane.split); DefaultPerTab when 0. A later spawn fills a
	// tab of the project that has room before it opens one; 1 gives each
	// agent its own tab. The panes fill a grid of PerTab cells in reading
	// order (grid, splitTarget): 6 is 2 rows of 3.
	PerTab int
	// Prompt is the kickoff; empty means ACFS's default_new_agent
	// palette prompt, unless NoKickoff.
	Prompt    string
	NoKickoff bool
	// Enrich is the kickoff's CM and CASS enrichment (plan-v8 2.13:
	// kickoffs default to best-effort CM).
	Enrich send.Enrichment
	// AssignOptions are spawn --assign's choices as text, "" without it.
	// They enter the fingerprint only: a rerun of the operation with other
	// choices is refused (nhm-cvw).
	AssignOptions string
	StaggerMode   string
	StaggerDelay  time.Duration
	ReadyTimeout  time.Duration
	// NoWait starts the agents and does not wait for them to be ready:
	// --robot-spawn without --spawn-wait or a prompt, as ntm's. It takes
	// no kickoff, since a kickoff needs a ready agent.
	NoWait bool
	// KickoffAllReady sends the kickoffs only when every agent is ready,
	// as ntm's robot spawn sends no prompt after a failed ready wait.
	KickoffAllReady bool
	TrustFolder     bool
	Force           bool
	// AllowUnguarded records an exception for a missing guard, for this
	// launch only.
	AllowUnguarded bool
	// IgnoreCapacity admits a red or unknown capacity with a warning,
	// for this launch only (nhm-1pq).
	IgnoreCapacity bool
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
	if r.Safety && r.Add {
		return fmt.Errorf("--spawn-safety refuses an existing session, and nhm add needs one")
	}
	if r.Effort != "" && !kinds.EffortPattern.MatchString(r.Effort) {
		return fmt.Errorf("--effort %q is not a plain word (low, medium, high, xhigh, max; at most 32 of letters, numbers, . _ + -, "+
			"a letter or a number first)", r.Effort)
	}
	if r.Role != "" && r.Role != RoleReviewer {
		return fmt.Errorf("--role takes %q (or nothing), not %q", RoleReviewer, r.Role)
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
		// nhm puts the model and the effort into an argv as values of their
		// own, so each starts with a letter or a digit, never "-" (ntm's
		// charset allows that first).
		if w.Model != "" && !modelPattern.MatchString(w.Model) {
			return fmt.Errorf("invalid model %q: nhm takes a model of at most 128 of ntm's characters (letters, numbers, . _ / @ : + -) "+
				"that starts with a letter or a number", w.Model)
		}
		if w.Effort != "" && !kinds.EffortPattern.MatchString(w.Effort) {
			return fmt.Errorf("invalid reasoning effort %q: nhm takes an effort of at most 32 of ntm's characters (letters, numbers, . _ + -) "+
				"that starts with a letter or a number", w.Effort)
		}
		r.Agents[i].Kind = a.Name
		total += w.Count
	}
	if total == 0 {
		return fmt.Errorf("nothing to spawn: give --cc=N, --cod=N, --agy=N … or --agent KIND=N")
	}
	if r.PerTab <= 0 {
		r.PerTab = DefaultPerTab
	}
	switch r.StaggerMode {
	case "":
		r.StaggerMode = StaggerNone
		if r.StaggerDelay > 0 {
			r.StaggerMode = StaggerFixed
		}
	case StaggerNone, StaggerFixed, StaggerSmart:
	default:
		return fmt.Errorf("--stagger-mode %w", ValidateStaggerMode(r.StaggerMode))
	}
	if err := ValidateStaggerDelay(r.StaggerDelay); err != nil {
		return fmt.Errorf("--stagger-delay %w", err)
	}
	if r.NoWait && !r.NoKickoff {
		return fmt.Errorf("a kickoff needs the agents ready: a spawn that does not wait sends none")
	}
	if r.ReadyTimeout <= 0 {
		r.ReadyTimeout = 60 * time.Second
	}
	return nil
}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,127}$`)

// RoleReviewer marks agents spawned to review: they work no beads.
const RoleReviewer = "reviewer"

// ValidModel reports a model name nhm puts into an argv: it starts with a
// letter or digit, so it never reads as a flag.
func ValidModel(model string) bool { return modelPattern.MatchString(model) }

// Count is the number of agents the request starts.
func (r *Request) Count() int {
	n := 0
	for _, w := range r.Agents {
		n += w.Count
	}
	return n
}
