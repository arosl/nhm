package ui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/arosl/nhm/internal/integrations/kinds"
)

// Prompter asks the spawn wizard's questions (plan-v8 2.4, nhm.spawn).
// Gum is the real one; tests answer from a script.
type Prompter interface {
	// Choose returns the chosen options; multi allows more than one.
	Choose(header string, options []string, multi bool) ([]string, error)
	Input(header, placeholder, value string) (string, error)
	Write(header, placeholder string) (string, error)
	Confirm(question string) (bool, error)
}

// ErrCancelled means the operator left the wizard: nothing starts.
var ErrCancelled = errors.New("ui: cancelled; nothing started")

// SpawnAgent is one kind the wizard chose.
type SpawnAgent struct {
	Kind  string
	Count int
	Model string
}

// SpawnAnswers are the wizard's answers.
type SpawnAnswers struct {
	Agents       []SpawnAgent
	Label, Group string
	// Prompt is the kickoff; empty with DefaultKickoff is ACFS's
	// default_new_agent; NoKickoff sends none.
	Prompt         string
	DefaultKickoff bool
	NoKickoff      bool
	// Memory puts CM rules in the kickoff; CASS adds CASS context.
	Memory, CASS bool
}

// Kickoff and memory choices.
const (
	kickoffDefault = "ACFS's default_new_agent prompt"
	kickoffOwn     = "write my own"
	kickoffNone    = "no kickoff"
	memoryCM       = "CM rules (best-effort)"
	memoryBoth     = "CM rules and CASS context"
	memoryNone     = "neither"
)

// SpawnWizard asks for the kinds and counts, the models, the label and
// group, the kickoff and the memory. It starts nothing; the caller shows
// the plan and asks for the final yes.
func SpawnWizard(p Prompter, header string, adapters []kinds.Adapter) (SpawnAnswers, error) {
	var a SpawnAnswers
	names := make([]string, 0, len(adapters))
	byName := map[string]kinds.Adapter{}
	for _, k := range adapters {
		names = append(names, k.Name)
		byName[k.Name] = k
	}
	chosen, err := p.Choose(header+"\nWhich kinds? (space selects, enter confirms)", names, true)
	if err != nil {
		return a, err
	}
	if len(chosen) == 0 {
		return a, ErrCancelled
	}
	for _, name := range chosen {
		k, ok := byName[name]
		if !ok {
			return a, fmt.Errorf("ui: unknown kind %q", name)
		}
		text, err := p.Input(fmt.Sprintf("How many %s agents?", name), "1", "1")
		if err != nil {
			return a, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 1 || n > 32 {
			return a, fmt.Errorf("ui: %q is not a count from 1 to 32", text)
		}
		agent := SpawnAgent{Kind: name, Count: n}
		if k.ModelFlag != "" {
			model, err := p.Input(fmt.Sprintf("Model for %s (empty: the CLI's default)", name), "", "")
			if err != nil {
				return a, err
			}
			agent.Model = strings.TrimSpace(model)
		}
		a.Agents = append(a.Agents, agent)
	}
	if a.Label, err = p.Input("Tab label (empty: the agent's name, or the project's when agents share the tab)", "", ""); err != nil {
		return a, err
	}
	if a.Group, err = p.Input("Group (empty: none)", "", ""); err != nil {
		return a, err
	}
	kick, err := p.Choose("Kickoff prompt", []string{kickoffDefault, kickoffOwn, kickoffNone}, false)
	if err != nil {
		return a, err
	}
	switch first(kick) {
	case kickoffDefault:
		a.DefaultKickoff = true
	case kickoffOwn:
		if a.Prompt, err = p.Write("Kickoff prompt (ctrl-d saves)", "Your task is …"); err != nil {
			return a, err
		}
		if strings.TrimSpace(a.Prompt) == "" {
			return a, errors.New("ui: an empty kickoff prompt")
		}
	case kickoffNone:
		a.NoKickoff = true
	default:
		return a, ErrCancelled
	}
	if !a.NoKickoff {
		mem, err := p.Choose("Memory in the kickoff", []string{memoryCM, memoryBoth, memoryNone}, false)
		if err != nil {
			return a, err
		}
		switch first(mem) {
		case memoryCM:
			a.Memory = true
		case memoryBoth:
			a.Memory, a.CASS = true, true
		case memoryNone:
		default:
			return a, ErrCancelled
		}
	}
	return a, nil
}

func first(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// SpawnArgs is the nhm spawn command line of the answers, which the
// wizard shows before the final yes.
func SpawnArgs(session string, add bool, a SpawnAnswers) []string {
	verb := "spawn"
	if add {
		verb = "add"
	}
	args := []string{"nhm", verb, session}
	for _, ag := range a.Agents {
		v := strconv.Itoa(ag.Count)
		if ag.Model != "" {
			v += ":" + ag.Model
		}
		args = append(args, "--agent", ag.Kind+"="+v)
	}
	if a.Label != "" {
		args = append(args, "--label", a.Label)
	}
	if a.Group != "" {
		args = append(args, "--group", a.Group)
	}
	switch {
	case a.NoKickoff:
		args = append(args, "--no-kickoff")
	case a.Prompt != "":
		args = append(args, "--prompt", a.Prompt)
	}
	if !a.NoKickoff {
		if !a.Memory {
			args = append(args, "--with-memory=false")
		}
		if a.CASS {
			args = append(args, "--with-cass")
		} else {
			args = append(args, "--no-cass-context")
		}
	}
	return args
}

// Gum asks with gum, on the terminal.
type Gum struct{ Bin string }

func (g Gum) bin() string {
	if g.Bin != "" {
		return g.Bin
	}
	return "gum"
}

func (g Gum) run(args ...string) (string, error) {
	cmd := exec.Command(g.bin(), args...)
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 130 {
		return "", ErrCancelled
	}
	if err != nil {
		return "", fmt.Errorf("ui: gum %s: %w", args[0], err)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Choose runs gum choose.
func (g Gum) Choose(header string, options []string, multi bool) ([]string, error) {
	args := []string{"choose", "--header", header}
	if multi {
		args = append(args, "--no-limit")
	}
	out, err := g.run(append(args, options...)...)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// Input runs gum input.
func (g Gum) Input(header, placeholder, value string) (string, error) {
	return g.run("input", "--header", header, "--placeholder", placeholder, "--value", value)
}

// Write runs gum write.
func (g Gum) Write(header, placeholder string) (string, error) {
	return g.run("write", "--header", header, "--placeholder", placeholder)
}

// Confirm runs gum confirm: exit 0 is yes, 1 is no.
func (g Gum) Confirm(question string) (bool, error) {
	cmd := exec.Command(g.bin(), "confirm", question)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	case errors.As(err, &exit) && exit.ExitCode() == 130:
		return false, ErrCancelled
	}
	return false, fmt.Errorf("ui: gum confirm: %w", err)
}
