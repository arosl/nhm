// Package guards says which guards run in each agent kind's sessions
// (plan-v8 2.14): DCG (blocks destructive commands before they run),
// RCH (offloads builds), and Agent Mail's Stop hook. nhm reads each
// kind's settings files itself; herdr is not involved.
//
// States: unsupported (the kind has no hook for it), missing (no source
// holds it, or nhm cannot check this kind yet), untrusted (Codex holds the
// hook but has not trusted it, so it does not run), configured (a loaded
// source holds it, and Codex trusts it), verified_for_launch (configured,
// and a launch's argv keeps the source that holds it), stale (a settings
// change after verification; the spawn path sets it).
//
// The merge rules are exp.6's (ACFS evidence): Claude adds hooks from
// user, project and local settings, and from --settings; --bare drops
// them all; --setting-sources keeps only the named sources. Codex adds
// every layer, the project's only when the project is trusted, and runs
// a hook only when config.toml holds its current trusted_hash.
package guards

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/arosl/nhm/internal/integrations/kinds"
)

// State is a guard's state for a kind.
type State string

const (
	Unsupported       State = "unsupported"
	Missing           State = "missing"
	Untrusted         State = "untrusted"
	Configured        State = "configured"
	VerifiedForLaunch State = "verified_for_launch"
	Stale             State = "stale"
)

// Guard names.
const (
	DCG           = "dcg"
	RCH           = "rch"
	AgentMailStop = "agent_mail_stop"
)

// Guards lists the guards in report order.
var Guards = []string{DCG, RCH, AgentMailStop}

// Result is one guard's state for one kind.
type Result struct {
	Kind  string `json:"kind"`
	Guard string `json:"guard"`
	State State  `json:"state"`
	// Sources are the settings sources that hold the guard ("user",
	// "project", "local"; for Codex the hooks file).
	Sources []string `json:"sources,omitempty"`
	Detail  string   `json:"detail,omitempty"`
	Fix     string   `json:"fix,omitempty"`
}

// Env says where the settings are. Empty fields take the defaults.
type Env struct {
	Home        string
	CodexHome   string
	ProjectRoot string
}

func (e Env) withDefaults() Env {
	if e.Home == "" {
		e.Home, _ = os.UserHomeDir()
	}
	if e.CodexHome == "" {
		e.CodexHome = os.Getenv("CODEX_HOME")
	}
	if e.CodexHome == "" && e.Home != "" {
		e.CodexHome = filepath.Join(e.Home, ".codex")
	}
	return e
}

// Kinds lists the kinds Check reports, in the kind registry's order.
var Kinds = kinds.Names()

// checkers read a settings format: the kind registry names the file
// (Adapter.GuardSettings); each format needs its own reader.
var checkers = map[string]func(Env) []Result{
	"claude": checkClaude,
	"codex":  checkCodex,
	"agy":    checkAgy,
}

// Check reports every guard for a kind.
func Check(kind string, env Env) []Result {
	env = env.withDefaults()
	a := kinds.Lookup(kind)
	if !a.GuardSupported {
		return each(kind, func(g string) Result {
			return Result{State: Unsupported, Detail: kind + " has no guard that nhm sets up; every spawn warns"}
		})
	}
	if check, ok := checkers[kind]; ok {
		return check(env)
	}
	detail := "nhm has no reader for this kind's guard settings"
	if a.GuardSettings != "" {
		detail += " (" + a.GuardSettings + ")"
	}
	return each(kind, func(g string) Result {
		return Result{State: Missing, Detail: detail, Fix: "spawn it with --allow-unguarded"}
	})
}

func each(kind string, fn func(g string) Result) []Result {
	out := make([]Result, 0, len(Guards))
	for _, g := range Guards {
		r := fn(g)
		r.Kind, r.Guard = kind, g
		out = append(out, r)
	}
	return out
}

// hookGroup is one matcher group of Claude's and Codex's hooks format.
type hookGroup struct {
	Matcher *string       `json:"matcher"`
	Hooks   []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type hooksFile struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

// readHooks reads a settings or hooks.json file; ok is false when it is
// missing.
func readHooks(path string) (hooksFile, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return hooksFile{}, false, nil
	}
	if err != nil {
		return hooksFile{}, false, err
	}
	var f hooksFile
	if err := json.Unmarshal(data, &f); err != nil {
		return hooksFile{}, false, err
	}
	return f, true, nil
}

// program is the basename of a hook command's first word, unquoted.
func program(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	first := strings.Trim(fields[0], `"'`)
	if first == "bash" || first == "sh" || first == "python3" || first == "env" {
		if len(fields) > 1 {
			first = strings.Trim(fields[1], `"'`)
		}
	}
	return filepath.Base(first)
}

// matches reports whether a matcher selects a tool; no matcher (or "*" or
// "") selects every tool.
func matches(matcher *string, tool string) bool {
	if matcher == nil || *matcher == "" || *matcher == "*" {
		return true
	}
	re, err := regexp.Compile("^(?:" + *matcher + ")$")
	if err != nil {
		return false
	}
	return re.MatchString(tool)
}

// holds says whether a hooks file holds a guard, and where: the event,
// the group and handler index (for Codex's trust key).
func holds(f hooksFile, guard string) (event string, group, handler int, ok bool) {
	switch guard {
	case DCG, RCH:
		for gi, g := range f.Hooks["PreToolUse"] {
			if !matches(g.Matcher, "Bash") {
				continue
			}
			for hi, h := range g.Hooks {
				if program(h.Command) == guard {
					return "PreToolUse", gi, hi, true
				}
			}
		}
	case AgentMailStop:
		for gi, g := range f.Hooks["Stop"] {
			for hi, h := range g.Hooks {
				if strings.Contains(h.Command, "agent_mail_hook") || (program(h.Command) == "am" && strings.Contains(h.Command, "stop")) {
					return "Stop", gi, hi, true
				}
			}
		}
	}
	return "", 0, 0, false
}

// fixFor is the command that installs a guard.
func fixFor(kind, guard string) string {
	switch {
	case guard == DCG && kind == "codex":
		// dcg install configures Claude Code only (acfs-9hkh.81).
		return "acfs update adds dcg's Bash guard to Codex's hooks.json and records its trust (nhm_hooks.sh codex-dcg register, then trust codex); dcg install covers Claude Code only"
	case guard == DCG:
		return "dcg install (ACFS's install runs it)"
	case guard == RCH:
		return "rch install (ACFS's install runs it)"
	default:
		return "ACFS's install adds agent_mail_hook.sh stop to " + kind + "'s hooks"
	}
}

func claudeSources(env Env) []struct{ name, path string } {
	out := []struct{ name, path string }{{"user", filepath.Join(env.Home, ".claude", "settings.json")}}
	if env.ProjectRoot != "" {
		out = append(out,
			struct{ name, path string }{"project", filepath.Join(env.ProjectRoot, ".claude", "settings.json")},
			struct{ name, path string }{"local", filepath.Join(env.ProjectRoot, ".claude", "settings.local.json")})
	}
	return out
}

func checkClaude(env Env) []Result {
	files := map[string]hooksFile{}
	var errs []string
	for _, s := range claudeSources(env) {
		f, ok, err := readHooks(s.path)
		if err != nil {
			errs = append(errs, s.path+": "+err.Error())
			continue
		}
		if ok {
			files[s.name] = f
		}
	}
	return each("claude", func(g string) Result {
		var srcs []string
		for _, s := range claudeSources(env) {
			if _, _, _, ok := holds(files[s.name], g); ok {
				srcs = append(srcs, s.name)
			}
		}
		if len(srcs) == 0 {
			r := Result{State: Missing, Detail: "no Claude settings source holds it", Fix: fixFor("claude", g)}
			if len(errs) > 0 {
				r.Detail += "; unreadable: " + strings.Join(errs, "; ")
			}
			return r
		}
		return Result{State: Configured, Sources: srcs}
	})
}

func checkAgy(env Env) []Result {
	path := filepath.Join(env.Home, ".gemini", "config", "hooks.json")
	data, err := os.ReadFile(path)
	var named map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(data, &named)
	}
	return each("agy", func(g string) Result {
		if g != DCG {
			return Result{State: Unsupported, Detail: "agy has no hook for it"}
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return Result{State: Missing, Detail: path + " does not exist", Fix: fixFor("agy", g)}
			}
			return Result{State: Missing, Detail: path + ": " + err.Error(), Fix: fixFor("agy", g)}
		}
		for name, raw := range named {
			var block struct {
				Enabled    *bool       `json:"enabled"`
				PreToolUse []hookGroup `json:"PreToolUse"`
			}
			if json.Unmarshal(raw, &block) != nil || (block.Enabled != nil && !*block.Enabled) {
				continue
			}
			for _, grp := range block.PreToolUse {
				for _, h := range grp.Hooks {
					if strings.Contains(program(h.Command), "dcg") {
						// agy cannot be run in a sandbox, so nhm never
						// verifies its launch (exp.6).
						return Result{State: Configured, Sources: []string{path + "#" + name}}
					}
				}
			}
		}
		return Result{State: Missing, Detail: "no enabled PreToolUse hook in " + path + " runs dcg", Fix: fixFor("agy", g)}
	})
}

// VerifyLaunch checks a kind's launch argv against the merge rules: a
// configured guard is verified_for_launch when the argv keeps a source
// that holds it. It returns the results with their new states.
func VerifyLaunch(results []Result, argv []string) []Result {
	out := make([]Result, len(results))
	copy(out, results)
	flagHooks, hasFlag := settingsFlagHooks(argv)
	for i, r := range out {
		// Claude's --settings on the launch argv carries hooks itself, as
		// ntm passes them (plan-v8 2.14: the launch's own precedence).
		// A guard it holds is verified for this launch whatever the
		// user's settings hold.
		if r.Kind == "claude" && hasFlag && r.State != Unsupported {
			if _, _, _, ok := holds(flagHooks, r.Guard); ok {
				out[i].State, out[i].Detail = VerifiedForLaunch, "the launch's --settings holds it"
				out[i].Sources = append(append([]string{}, r.Sources...), "flag")
				continue
			}
		}
		if r.State != Configured {
			continue
		}
		switch r.Kind {
		case "claude":
			keep, why := claudeKeeps(argv, r.Sources)
			if keep {
				out[i].State = VerifiedForLaunch
			} else {
				out[i].State, out[i].Detail = Missing, why
			}
		case "codex":
			// Codex adds every layer; trust was checked with the state.
			out[i].State = VerifiedForLaunch
		}
	}
	return out
}

// settingsFlagHooks reads the hooks of a --settings value on the argv:
// inline JSON, as spawn passes it, or a settings file.
func settingsFlagHooks(argv []string) (hooksFile, bool) {
	if slices.Contains(argv, "--bare") {
		// --bare drops every hook, the flag's too.
		return hooksFile{}, false
	}
	for i, a := range argv {
		val := ""
		switch {
		case strings.HasPrefix(a, "--settings="):
			val = strings.TrimPrefix(a, "--settings=")
		case a == "--settings" && i+1 < len(argv):
			val = argv[i+1]
		default:
			continue
		}
		var f hooksFile
		if strings.HasPrefix(strings.TrimSpace(val), "{") {
			if json.Unmarshal([]byte(val), &f) == nil {
				return f, true
			}
			return hooksFile{}, false
		}
		if f, ok, err := readHooks(val); err == nil && ok {
			return f, true
		}
	}
	return hooksFile{}, false
}

// claudeKeeps applies Claude's --bare and --setting-sources to the
// sources that hold a guard.
func claudeKeeps(argv, sources []string) (bool, string) {
	allowed := map[string]bool{"user": true, "project": true, "local": true}
	for i, a := range argv {
		if a == "--bare" {
			return false, "the launch argv has --bare, which drops every hook"
		}
		val := ""
		switch {
		case strings.HasPrefix(a, "--setting-sources="):
			val = strings.TrimPrefix(a, "--setting-sources=")
		case a == "--setting-sources" && i+1 < len(argv):
			val = argv[i+1]
		default:
			continue
		}
		allowed = map[string]bool{}
		for _, s := range strings.Split(val, ",") {
			allowed[strings.TrimSpace(s)] = true
		}
	}
	for _, s := range sources {
		if allowed[s] {
			return true, ""
		}
	}
	return false, "the launch argv's --setting-sources leaves out " + strings.Join(sources, ", ")
}
