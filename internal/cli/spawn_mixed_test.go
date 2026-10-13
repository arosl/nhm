package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/store"
)

// nhm add from one project's checkout into a session whose workspace
// holds another project's agents only warns before it creates anything;
// the same add, once the workspace holds an agent of the project, does
// not (nhm-8ze).
func TestAddWarnsOfAnotherProjectsWorkspace(t *testing.T) {
	s, inst, h := newSendInstance(t)
	spawnHerdr(t, s, h)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	project := t.TempDir()
	add := func() (map[string]any, string) {
		t.Helper()
		code, out, errOut := run("add", "swarm", "--cc=1", "--project", project, "--prompt", "Read AGENTS.md.", "--allow-unguarded",
			"--dry-run", "--socket", s.SocketPath, "--json")
		if code != 0 {
			t.Fatalf("add = %d %s %s", code, out, errOut)
		}
		env := decodeEnvelope(t, out)
		warnings, _ := env["warnings"].([]any)
		for _, w := range warnings {
			if text, _ := w.(string); strings.Contains(text, "holds agents of other projects only") {
				return env["result"].(map[string]any), text
			}
		}
		return env["result"].(map[string]any), ""
	}

	// The session's workspace w1 holds IcyKnoll and BlueLake of /p only.
	res, mixed := add()
	root, _ := res["working_dir"].(string)
	if root == "" || !strings.Contains(mixed, "workspace w1 holds agents of other projects only (/p), not of "+root+":") {
		t.Fatalf("add into /p's workspace: result %v, warning %q", res, mixed)
	}

	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: "GreenPike", MailProjectKey: root,
		ProjectID: "proj-2", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t9",
			PaneID: "w1:p9", HerdrAlias: "greenpike", Kind: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if _, mixed = add(); mixed != "" {
		t.Fatalf("add beside its own project's agent warned: %q", mixed)
	}
}

// answers is a prompter for the spawn wizard: one claude, the defaults,
// no kickoff, and no at the yes.
type answers struct{}

func (answers) Choose(_ string, options []string, multi bool) ([]string, error) {
	if multi {
		return []string{"claude"}, nil
	}
	return options[len(options)-1:], nil
}
func (answers) Input(_, _, value string) (string, error) { return value, nil }
func (answers) Write(string, string) (string, error)     { return "", nil }
func (answers) Confirm(string) (bool, error)             { return false, nil }

// The wizard prints the plan's warnings with the plan, before its yes, so
// the operator reads that the session holds other projects' agents only
// before anything starts (nhm-8ze). The test's HOME has no guards, so
// admission refuses right after the plan; the warning still comes first.
func TestSpawnWizardWarnsWithThePlan(t *testing.T) {
	s, _, h := newSendInstance(t)
	spawnHerdr(t, s, h)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr strings.Builder
	code := runSpawnWizard(context.Background(), answers{}, "swarm", true, t.TempDir(), s.SocketPath, &stdout, &stderr)
	errOut := stderr.String()
	warn := strings.Index(errOut, "nhm add: warning: workspace w1 holds agents of other projects only (/p)")
	if code == exitOK || warn < 0 || !strings.Contains(stdout.String(), "would start 1 agents in swarm (w1)") ||
		strings.Contains(stdout.String(), "started 1") {
		t.Fatalf("wizard = %d\nstdout %s\nstderr %s", code, stdout.String(), errOut)
	}
	if refused := strings.Index(errOut, "admission refused"); refused >= 0 && refused < warn {
		t.Fatalf("the warning came after the refusal:\n%s", errOut)
	}
}
