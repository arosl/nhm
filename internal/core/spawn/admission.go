// Package spawn is nhm's spawn, add and new (plan-v8 2.2): admission
// before anything paid starts, then per agent the identity first, a fresh
// pane with the launch id in its environment, the binding, agent.start,
// labels, the stagger and the kickoff through send. Partial failure keeps
// what started and cleans up only what this operation created and nothing
// uses.
package spawn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/integrations/quota"
)

// Admission decisions, ntm's names.
const (
	Admit  = "admit"
	Refuse = "refuse"
)

// quotaLimitPct is the share of a window at which a start is refused.
const quotaLimitPct = 90

// quotaStaleAfter is the age at which a quota observation is stale.
const quotaStaleAfter = time.Hour

// Admission is the decision before anything starts.
type Admission struct {
	Decision string   `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
	Reasons  []string `json:"reasons"`
	Warnings []string `json:"warnings"`
	// Exceptions are the guards --allow-unguarded let through, for this
	// launch only.
	Exceptions []string `json:"exceptions,omitempty"`
	// Profile is "operator" or "require_observations".
	Profile string `json:"profile"`
}

// Capacity is a capacity provider's answer: green, yellow or red.
type Capacity struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
}

// AdmissionEnv is where admission reads its facts. nil functions skip
// that source.
type AdmissionEnv struct {
	// Quota returns the pools' observations.
	Quota func() []quota.Observation
	// Capacity runs the capacity provider; nil means "none".
	Capacity func(ctx context.Context) (Capacity, error)
	// Guards checks a kind's guards.
	Guards func(kind string) []guards.Result
	// Screen screens a launch argv against DCG's policy.
	Screen func(ctx context.Context, argv []string) (guards.Verdict, error)
	// AdmitUnknown lists the pools whose unknown quota user config admits
	// for robot calls.
	AdmitUnknown []string
	Now          func() time.Time
}

// LaunchArgv is the argv herdr runs for a kind and model: the kind's
// command, its launch args and its model flag.
func LaunchArgv(a kinds.Adapter, model string) []string {
	argv := append([]string{a.Name}, a.LaunchArgs...)
	if model != "" && a.ModelFlag != "" {
		argv = append(argv, a.ModelFlag, model)
	}
	return argv
}

// Decide decides whether the agents of a request may start (plan-v8 2.2,
// the admission table). It runs before any identity or pane exists.
func Decide(ctx context.Context, env AdmissionEnv, req Request) Admission {
	ad := Admission{Decision: Admit, Reasons: []string{}, Warnings: []string{}, Profile: "operator"}
	if req.RequireObservations {
		ad.Profile = "require_observations"
	}
	refuse := func(format string, args ...any) { ad.Reasons = append(ad.Reasons, fmt.Sprintf(format, args...)) }
	warn := func(format string, args ...any) { ad.Warnings = append(ad.Warnings, fmt.Sprintf(format, args...)) }
	now := time.Now()
	if env.Now != nil {
		now = env.Now()
	}

	// Capacity: red refuses, and nothing overrides it.
	if env.Capacity != nil {
		c, err := env.Capacity(ctx)
		switch {
		case err != nil && req.RequireObservations:
			refuse("capacity unknown: %v", err)
		case err != nil:
			warn("capacity unknown: %v", err)
		case c.Status == "red":
			refuse("capacity red: %s", strings.Join(c.Reasons, "; "))
		case c.Status == "yellow":
			warn("capacity yellow: %s", strings.Join(c.Reasons, "; "))
		}
	}

	var obs []quota.Observation
	if env.Quota != nil {
		obs = env.Quota()
	}
	seen := map[string]bool{}
	for _, w := range req.Agents {
		if seen[w.Kind] {
			continue
		}
		seen[w.Kind] = true
		a := kinds.Lookup(w.Kind)
		admitQuota(&ad, req, env, obs, a, now, refuse, warn)
		admitGuards(ctx, &ad, req, env, a, w.Model, refuse, warn)
	}
	if len(ad.Reasons) > 0 {
		ad.Decision, ad.Reason = Refuse, strings.Join(ad.Reasons, "; ")
	}
	return ad
}

func admitQuota(ad *Admission, req Request, env AdmissionEnv, obs []quota.Observation, a kinds.Adapter, now time.Time,
	refuse, warn func(string, ...any)) {
	i := slices.IndexFunc(obs, func(o quota.Observation) bool { return o.Pool == a.QuotaPool || o.Kind == a.Name })
	unknown := ""
	switch {
	case i < 0:
		unknown = "no quota source"
	case !obs[i].Known():
		unknown = obs[i].Note
		if unknown == "" {
			unknown = "no observation"
		}
	case now.Sub(*obs[i].ObservedAt) > quotaStaleAfter:
		unknown = "the last observation is from " + obs[i].ObservedAt.UTC().Format(time.RFC3339)
	}
	if unknown != "" {
		switch {
		case !req.RequireObservations:
			warn("%s quota unknown (%s): admitted", a.QuotaPool, unknown)
		case slices.Contains(env.AdmitUnknown, a.QuotaPool):
			warn("%s quota unknown (%s): admitted by the config's policy", a.QuotaPool, unknown)
		default:
			refuse("%s quota unknown (%s); a robot call needs an observation", a.QuotaPool, unknown)
		}
		if i < 0 || !obs[i].Known() {
			return
		}
	}
	o := obs[i]
	ok, why := quota.Check(obs, o.Kind, quotaLimitPct)
	weekly := o.Weekly
	if ok && weekly != nil && !weekly.Reset && weekly.UsedPct != nil && *weekly.UsedPct >= quotaLimitPct {
		ok, why = false, fmt.Sprintf("%s: weekly window %d%% used (limit %d%%)", o.Kind, int(*weekly.UsedPct), quotaLimitPct)
	}
	if ok {
		return
	}
	limit := o.LimitReached != nil && !o.LimitReached.Stale
	if req.Force && !req.RequireObservations && !limit {
		warn("%s (overridden by --force)", why)
		return
	}
	refuse("%s", why)
}

func admitGuards(ctx context.Context, ad *Admission, req Request, env AdmissionEnv, a kinds.Adapter, model string,
	refuse, warn func(string, ...any)) {
	if !a.GuardSupported {
		warn("%s runs no hooks: DCG and the other guards do not protect it (F.9)", a.Name)
		return
	}
	if env.Guards == nil {
		return
	}
	argv := LaunchArgv(a, model)
	results := guards.VerifyLaunch(env.Guards(a.Name), argv)
	for _, r := range results {
		if r.State == guards.Configured || r.State == guards.VerifiedForLaunch || r.State == guards.Unsupported {
			continue
		}
		msg := fmt.Sprintf("%s guard %s is %s: %s", a.Name, r.Guard, r.State, r.Detail)
		if req.AllowUnguarded {
			ad.Exceptions = append(ad.Exceptions, msg)
			continue
		}
		if r.Fix != "" {
			msg += " (fix: " + r.Fix + ")"
		}
		refuse("%s", msg)
	}
	if env.Screen == nil {
		return
	}
	v, err := env.Screen(ctx, argv)
	switch {
	case errors.Is(err, guards.ErrNoDCG) && req.AllowUnguarded:
		ad.Exceptions = append(ad.Exceptions, a.Name+": dcg is not installed; the launch argv was not screened")
	case err != nil:
		refuse("%s: the launch argv could not be screened: %v", a.Name, err)
	case v.Decision != guards.Allow:
		// A denied argv is never an exception.
		refuse("%s: DCG denies the launch argv %s: %s", a.Name, v.Command, v.Reason)
	}
}

// CommandCapacity runs a capacity provider command (ACFS configures
// `acfs capacity --guard --json`) and reads its status and reasons.
func CommandCapacity(argv []string) func(ctx context.Context) (Capacity, error) {
	return func(ctx context.Context) (Capacity, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
		var c Capacity
		if jerr := json.Unmarshal(out, &c); jerr != nil || c.Status == "" {
			if err == nil {
				err = fmt.Errorf("%s answered no status", argv[0])
			}
			return Capacity{}, fmt.Errorf("capacity provider: %w", err)
		}
		// `--check` style providers exit 1 at red; the JSON says so too.
		return c, nil
	}
}
