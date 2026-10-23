package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/kinds"
)

const depsUsage = `Usage: nhm deps [-v|--verbose] [--json]   (also nhm check)

ntm's deps on herdr: check that the tools nhm needs are installed.
Required: herdr (HERDR_BIN_PATH, else herdr on PATH), in tmux's place.
AI agents: the CLI of each kind nhm spawns. Recommended: fzf and git.
Then the services (Agent Mail) and the flywheel tools. -v adds each
tool's version and, for a missing one, how to install it. Exits 1 when a
required tool is missing. --json gives nhm's envelope with ntm's fields
(generated_at, all_installed, dependencies[]{name, required, installed,
version, path}). nhm doctor checks nhm's own setup.
`

// depCheck is one tool deps looks for: ntm's depCheck. Commands are tried
// in order; the first on PATH counts.
type depCheck struct {
	Name        string
	Commands    []string
	VersionArgs []string
	Required    bool
	Category    string
	InstallHint string
}

// DependencyCheck is one tool's result, ntm's DependencyCheck.
type DependencyCheck struct {
	Name      string `json:"name"`
	Required  bool   `json:"required"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
}

// DepsResult is deps' result: ntm's DepsResponse fields.
type DepsResult struct {
	GeneratedAt  time.Time         `json:"generated_at"`
	AllInstalled bool              `json:"all_installed"`
	Dependencies []DependencyCheck `json:"dependencies"`
}

const (
	depRequired    = "Required"
	depAgents      = "AI Agents"
	depRecommended = "Recommended"
	depFlywheel    = "Flywheel Tools"
)

// depVersionTimeout bounds one version command, as ntm's.
var depVersionTimeout = 2 * time.Second

// agentDeps are ntm's names and install hints for the agent CLIs of
// nhm's kinds; pi is nhm's.
var agentDeps = map[string]depCheck{
	"claude":   {Name: "Claude Code", Commands: []string{"claude"}, InstallHint: "npm install -g @anthropic-ai/claude-code"},
	"codex":    {Name: "OpenAI Codex", Commands: []string{"codex"}, InstallHint: "npm install -g @openai/codex"},
	"agy":      {Name: "Antigravity CLI (agy)", Commands: []string{"agy"}, InstallHint: "curl -fsSL https://antigravity.google/cli/install.sh | bash (then `agy` once to authenticate via Google OAuth)"},
	"pi":       {Name: "Pi (pi)", Commands: []string{"pi"}, InstallHint: "ACFS's install sets it up"},
	"omp":      {Name: "Oh My Pi (omp)", Commands: []string{"omp"}, InstallHint: "bun install -g @oh-my-pi/pi-coding-agent (see https://omp.sh), then run `omp setup` once interactively before the first spawn"},
	"opencode": {Name: "OpenCode", Commands: []string{"opencode"}, InstallHint: "curl -fsSL https://opencode.ai/install | bash (or `npm install -g opencode-ai`)"},
	"grok":     {Name: "Grok Build (xAI)", Commands: []string{"grok"}, InstallHint: "curl -fsSL https://x.ai/cli/install.sh | bash (then `grok login`; use `grok login --device-auth` on a remote host)"},
}

// depChecks is ntm's list with herdr for tmux and nhm's kinds for ntm's
// agents. A kind nhm's config adds is checked by its name, as ntm checks
// a plugin agent.
func depChecks() []depCheck {
	checks := []depCheck{{Name: "herdr", Commands: []string{herdrBin()}, Required: true, Category: depRequired,
		InstallHint: "ACFS's install sets it up (github.com/herdrdev/herdr)"}}
	for _, k := range kinds.Names() {
		d, ok := agentDeps[k]
		if !ok {
			d = depCheck{Name: k, Commands: []string{k}}
		}
		d.Category = depAgents
		checks = append(checks, d)
	}
	checks = append(checks,
		depCheck{Name: "fzf", Commands: []string{"fzf"}, Category: depRecommended, InstallHint: "brew install fzf (macOS) / apt install fzf (Linux)"},
		depCheck{Name: "git", Commands: []string{"git"}, Category: depRecommended, InstallHint: "brew install git (macOS) / apt install git (Linux)"})
	for i := range checks {
		checks[i].VersionArgs = []string{"--version"}
	}
	return checks
}

// flywheelTools are ntm's key flywheel tools, in its order; bv is marked
// as triage needs it, and bd is br first, as ntm's adapter looks.
var flywheelTools = []depCheck{
	{Name: "bv *", Commands: []string{"bv"}}, {Name: "bd", Commands: []string{"br", "bd"}},
	{Name: "caam", Commands: []string{"caam"}}, {Name: "caut", Commands: []string{"caut"}},
	{Name: "dcg", Commands: []string{"dcg"}}, {Name: "ubs", Commands: []string{"ubs"}},
	{Name: "cass", Commands: []string{"cass"}}, {Name: "cm", Commands: []string{"cm"}},
}

func runDeps(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deps", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, depsUsage) }
	var verbose bool
	fs.BoolVar(&verbose, "verbose", false, "versions and install hints")
	fs.BoolVar(&verbose, "v", false, "versions and install hints")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) > 0 {
		fmt.Fprint(stderr, depsUsage)
		return exitRefused
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	checks := depChecks()
	results := probeDeps(ctx, checks, true)
	mailURL, mailUp := agentMailUp(ctx)
	res := DepsResult{GeneratedAt: time.Now().UTC(), AllInstalled: true, Dependencies: results}
	for _, r := range results {
		if r.Required && !r.Installed {
			res.AllInstalled = false
		}
	}
	res.Dependencies = append(res.Dependencies, DependencyCheck{Name: "Agent Mail", Installed: mailUp, Path: mailURL})
	env := envelope{Command: "deps", Result: res}
	code, err := exitOK, error(nil)
	if !res.AllInstalled {
		code, err = exitError, fmt.Errorf("required dependencies are missing")
	}
	if *asJSON {
		return output(stdout, stderr, true, env, err, code, nil)
	}
	tools := probeDeps(ctx, flywheelTools, verbose)
	printDeps(stdout, checks, results, mailURL, mailUp, tools, verbose)
	return code
}

// probeDeps checks each tool, at once: its path, and its version when
// asked.
func probeDeps(ctx context.Context, checks []depCheck, withVersion bool) []DependencyCheck {
	out := make([]DependencyCheck, len(checks))
	var wg sync.WaitGroup
	for i, d := range checks {
		out[i] = DependencyCheck{Name: d.Name, Required: d.Required}
		wg.Add(1)
		go func(r *DependencyCheck, d depCheck) {
			defer wg.Done()
			for _, c := range d.Commands {
				if p, err := exec.LookPath(c); err == nil {
					r.Installed, r.Path = true, p
					break
				}
			}
			if r.Installed && withVersion {
				r.Version = depVersion(ctx, r.Path, d.VersionArgs)
			}
		}(&out[i], d)
	}
	wg.Wait()
	return out
}

// depVersion is a tool's version line, as ntm reads it: the command's
// output, even when it exits non-zero, made safe for a terminal (no
// control or format characters, spaces collapsed, at most 240 runes).
func depVersion(ctx context.Context, path string, args []string) string {
	if len(args) == 0 {
		args = []string{"--version"}
	}
	ctx, cancel := context.WithTimeout(ctx, depVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = 2 * time.Second
	raw, _ := cmd.CombinedOutput()
	var runes []rune
	space := false
	for _, r := range stripControl(string(raw)) {
		if unicode.IsSpace(r) {
			space = len(runes) > 0
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			continue
		}
		if space {
			runes = append(runes, ' ')
			space = false
		}
		runes = append(runes, r)
	}
	if len(runes) > 240 {
		return string(runes[:240]) + "..."
	}
	return string(runes)
}

// agentMailUp asks Agent Mail's health_check, as nhm doctor does.
func agentMailUp(ctx context.Context) (string, bool) {
	c, err := agentmail.NewClient("")
	if err != nil {
		return agentmail.DefaultURL(), false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.URL, c.Call(ctx, "health_check", map[string]any{}, nil) == nil
}

// printDeps prints ntm's report, without its colours.
func printDeps(w io.Writer, checks []depCheck, results []DependencyCheck, mailURL string, mailUp bool, tools []DependencyCheck, verbose bool) {
	line := func(icon, name, detail string) {
		fmt.Fprintln(w, strings.TrimRight(fmt.Sprintf("  %s %-15s %s", icon, name, detail), " "))
	}
	fmt.Fprint(w, "\n nhm Dependency Check\n═══════════════════════════════════════════════════\n\n")
	agents := 0
	for _, cat := range []string{depRequired, depAgents, depRecommended} {
		fmt.Fprintf(w, "%s:\n\n", cat)
		for i, d := range checks {
			if d.Category != cat {
				continue
			}
			r := results[i]
			icon, detail := "✗", ""
			if r.Installed {
				icon = "✓"
				if cat == depAgents {
					agents++
				}
				if verbose && r.Version != "" {
					detail = clip(r.Version, 40)
				}
			}
			line(icon, d.Name, detail)
			if !r.Installed && verbose && d.InstallHint != "" {
				fmt.Fprintf(w, "      Install: %s\n", d.InstallHint)
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprint(w, "Services:\n\n")
	switch {
	case mailUp && verbose:
		line("✓", "Agent Mail", "running ("+mailURL+")")
	case mailUp:
		line("✓", "Agent Mail", "")
	case verbose:
		line("○", "Agent Mail", "not detected (optional)")
	default:
		line("○", "Agent Mail", "")
	}
	fmt.Fprintf(w, "\n%s:\n\n", depFlywheel)
	installed := 0
	for _, t := range tools {
		icon, detail := "○", ""
		if t.Installed {
			installed++
			icon = "✓"
			if verbose {
				detail = clip(firstOf(t.Version, "installed"), 30)
			}
		}
		line(icon, t.Name, detail)
	}
	fmt.Fprint(w, "\n───────────────────────────────────────────────────\n")
	missing := false
	for _, r := range results {
		missing = missing || (r.Required && !r.Installed)
	}
	switch {
	case missing:
		fmt.Fprintln(w, "✗ Missing required dependencies!")
	case agents == 0:
		fmt.Fprintln(w, "⚠ No AI agents installed. Install at least one to use nhm spawn.")
	default:
		fmt.Fprintf(w, "✓ All required dependencies installed. %d agent(s), %d flywheel tool(s) available.\n", agents, installed)
	}
	fmt.Fprintln(w)
}

// clip cuts s to n runes, with "..." after a cut, as ntm cuts a version
// (ntm cuts bytes, which can split a character).
func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return string(r)
}
