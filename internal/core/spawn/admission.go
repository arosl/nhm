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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// capacityWayOut ends a capacity refusal: the two ways to start anyway.
const capacityWayOut = "to start anyway: --ignore-capacity for this spawn, " +
	"or capacity = [] under [providers] in ~/.config/nhm/config.toml for every spawn"

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
	// Settings are the guard settings that each kind with a settings flag
	// gets at launch, by kind (LaunchSettings); a kind with none gets no
	// settings flag.
	Settings map[string]string
	Now      func() time.Time
}

// LaunchArgv is the argv herdr runs for an agent, as ntm launches it:
// the kind's command and launch args (the permission flags), the model,
// the reasoning effort (the kind's default when empty), and the guard
// through the kind's settings flag and its args when settings is not
// empty.
func LaunchArgv(a kinds.Adapter, model, effort, settings string) []string {
	argv := append([]string{a.Name}, a.LaunchArgs...)
	if model != "" && a.ModelFlag != "" {
		argv = append(argv, a.ModelFlag, model)
	}
	argv = append(argv, a.Effort(effort)...)
	if settings != "" && a.SettingsFlag != "" {
		argv = append(append(argv, a.SettingsFlag, settings), a.SettingsArgs...)
	}
	return argv
}

// LaunchSettings are the guard settings of a launch, by kind: Claude's
// hook settings JSON (HookSettings), and the path of nhm's pi guard
// extension, which it writes below instanceDir when dcg is installed. A
// pi without it is not guarded, and admission refuses it unless
// --allow-unguarded (guards.VerifyLaunch). lookPath is exec.LookPath.
func LaunchSettings(lookPath func(string) (string, error), instanceDir string) map[string]string {
	out := map[string]string{"claude": HookSettings(lookPath)}
	if dcg, err := lookPath("dcg"); err == nil {
		if abs, err := filepath.Abs(dcg); err == nil {
			if path, err := guards.WritePiExtension(filepath.Join(instanceDir, guards.PiExtensionDir), abs); err == nil {
				out["pi"] = path
			}
		}
	}
	return out
}

// hookEntry is one PreToolUse matcher group of Claude's settings.
type hookEntry struct {
	Matcher string        `json:"matcher"`
	Hooks   []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// ClaudeSettingsPath is the user's own Claude Code settings file
// ($CLAUDE_CONFIG_DIR/settings.json, or ~/.claude/settings.json). A
// variable so tests can point it elsewhere.
var ClaudeSettingsPath = func() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// HookSettings is the --settings JSON a Claude agent gets, as ntm builds
// it (policy.ClaudeAgentLaunchSettings): a Bash PreToolUse hook for dcg
// and one for rch, each only when its binary is installed (a hook naming
// a missing binary errors on every Bash call), and only when the user's
// own settings do not already run it (Claude merges both, and the guard
// would then run twice on every Bash call). lookPath is exec.LookPath.
func HookSettings(lookPath func(string) (string, error)) string {
	userRuns := userPreToolUsePrograms(ClaudeSettingsPath())
	var entries []hookEntry
	for _, bin := range []string{"dcg", "rch"} {
		path, err := lookPath(bin)
		if err != nil || userRuns[bin] {
			continue
		}
		entries = append(entries, hookEntry{Matcher: "Bash",
			Hooks: []hookHandler{{Type: "command", Command: shellQuote(path), Timeout: 5}}})
	}
	if len(entries) == 0 {
		return ""
	}
	data, _ := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": entries}})
	return string(data)
}

// portableMatcher is the regex syntax that Go's RE2 and Claude Code's
// JavaScript RegExp read the same way: names, alternation, groups
// without flags, and . * + ? ^ $. Anything else ((?i), \A, classes,
// {n}) may mean something else to Claude, or be rejected by it.
var portableMatcher = regexp.MustCompile(`^[A-Za-z0-9_|.*+?^$()]*$`)

// matcherCoversBash reports whether a Claude hook matcher selects the
// Bash tool: empty or "*" selects every tool, anything else is a tool
// name or a regular expression over the whole name. A matcher outside
// the portable syntax counts as not covering Bash, so nhm keeps its own
// guard (a guard that runs twice is the safe side).
func matcherCoversBash(m string) bool {
	if m == "" || m == "*" {
		return true
	}
	if !portableMatcher.MatchString(m) || strings.Contains(m, "(?") {
		return false
	}
	re, err := regexp.Compile(`^(?:` + m + `)$`)
	return err == nil && re.MatchString("Bash")
}

// userPreToolUsePrograms returns the programs (by base name) that the
// PreToolUse hooks of a Claude settings file run on Bash calls: the first
// word of each command hook, unquoted. A missing or unreadable file, or
// disableAllHooks, runs nothing.
func userPreToolUsePrograms(path string) map[string]bool {
	out := map[string]bool{}
	if path == "" {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var s struct {
		DisableAllHooks bool `json:"disableAllHooks"`
		Hooks           struct {
			PreToolUse []hookEntry `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &s) != nil || s.DisableAllHooks {
		return out
	}
	for _, e := range s.Hooks.PreToolUse {
		if !matcherCoversBash(e.Matcher) {
			continue
		}
		for _, h := range e.Hooks {
			fields := strings.Fields(h.Command)
			if h.Type != "command" || len(fields) == 0 {
				continue
			}
			out[filepath.Base(strings.Trim(fields[0], `'"`))] = true
		}
	}
	return out
}

// shellQuote quotes a path for a hook command, which Claude Code runs
// through a shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

	// Capacity: red refuses, and so does unknown in the robot profile;
	// --ignore-capacity admits both with a warning (nhm-1pq).
	if env.Capacity != nil {
		c, err := env.Capacity(ctx)
		blocked := ""
		switch {
		case err != nil && req.RequireObservations:
			blocked = fmt.Sprintf("capacity unknown: %v", err)
		case err != nil:
			warn("capacity unknown: %v", err)
		case c.Status == "red":
			blocked = "capacity red: " + strings.Join(c.Reasons, "; ")
		case c.Status == "yellow":
			warn("capacity yellow: %s", strings.Join(c.Reasons, "; "))
		}
		switch {
		case blocked != "" && req.IgnoreCapacity:
			warn("%s (overridden by --ignore-capacity)", blocked)
		case blocked != "":
			refuse("%s (%s)", blocked, capacityWayOut)
		}
	}

	var obs []quota.Observation
	if env.Quota != nil {
		obs = env.Quota()
	}
	seenKind, seenArgv := map[string]bool{}, map[string]bool{}
	for _, w := range req.Agents {
		a := kinds.Lookup(w.Kind)
		if !seenKind[w.Kind] {
			seenKind[w.Kind] = true
			admitQuota(&ad, req, env, obs, a, now, refuse, warn)
		}
		// Every distinct launch argv is checked and screened, not only
		// the first of a kind.
		argv := LaunchArgv(a, w.Model, effortOf(req, w), env.Settings[a.Name])
		key := strings.Join(argv, "\x00")
		if seenArgv[key] {
			continue
		}
		seenArgv[key] = true
		admitGuards(ctx, &ad, req, env, a, argv, refuse, warn)
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

func admitGuards(ctx context.Context, ad *Admission, req Request, env AdmissionEnv, a kinds.Adapter, argv []string,
	refuse, warn func(string, ...any)) {
	if !a.GuardSupported {
		warn("%s has no guard that nhm sets up: DCG and the other guards do not protect it", a.Name)
		return
	}
	if env.Guards == nil {
		return
	}
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

// effortOf is an agent's effort: its own, else the request's.
func effortOf(req Request, w Want) string {
	if w.Effort != "" {
		return w.Effort
	}
	return req.Effort
}

// CommandCapacity runs a capacity provider command (ACFS configures
// `acfs capacity --guard --json`) and reads its status and reasons.
func CommandCapacity(argv []string) func(ctx context.Context) (Capacity, error) {
	return func(ctx context.Context) (Capacity, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		// A provider is often a script: a child of it that holds stdout
		// open must not keep the spawn past the timeout (nhm-fty).
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
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
