package ensemble

// Ensemble runs (plan-v8 2.11). A run is a group of agents, one per angle
// of a preset, each started by nhm's spawn and prompted by nhm's send; it
// needs nothing else from herdr. Its durable metadata is run.json in the
// run's directory, beside the angles' artifacts:
//
//	<instance>/runs/<run-id>/run.json
//	<instance>/runs/<run-id>/angle-<i>-<mode>.md
//	<instance>/runs/<run-id>/synthesis.md
//
// Each angle's prompt names its artifact and the first line it must hold:
// "<!-- nhm-ensemble run=<id> angle=<i> mode=<mode> -->". synthesize
// accepts an artifact only with that line, as a regular file inside the
// run's directory, so an artifact always belongs to its run and angle. It
// refuses when an angle is missing, unless partial, which records the
// omissions. stop keeps every artifact and retires no agent.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/store"
)

// Run states.
const (
	RunStarting     = "starting"
	RunRunning      = "running"
	RunSynthesizing = "synthesizing"
	RunStopped      = "stopped"
	RunFailed       = "failed"
)

// Angle is one mode of a run and the agent that takes it.
type Angle struct {
	Index        int    `json:"index"`
	Mode         string `json:"mode"`
	Code         string `json:"code"`
	Kind         string `json:"kind"`
	Model        string `json:"model,omitempty"`
	MailName     string `json:"mail_name,omitempty"`
	PaneID       string `json:"pane_id,omitempty"`
	BindingID    string `json:"binding_id,omitempty"`
	Artifact     string `json:"artifact"`
	PromptOp     string `json:"prompt_operation,omitempty"`
	PromptStatus string `json:"prompt_status,omitempty"`
	Error        string `json:"error,omitempty"`
}

// Run is a run's metadata, as run.json holds it.
type Run struct {
	ID            string            `json:"run_id"`
	Preset        string            `json:"preset"`
	Strategy      SynthesisStrategy `json:"strategy"`
	Question      string            `json:"question"`
	Session       string            `json:"session"`
	WorkspaceID   string            `json:"workspace_id,omitempty"`
	Group         string            `json:"group"`
	ProjectRoot   string            `json:"project_root"`
	Status        string            `json:"status"`
	SpawnOp       string            `json:"spawn_operation,omitempty"`
	Angles        []Angle           `json:"angles"`
	SynthesisTo   string            `json:"synthesis_agent,omitempty"`
	SynthesisOp   string            `json:"synthesis_operation,omitempty"`
	Synthesis     string            `json:"synthesis_artifact,omitempty"`
	Omitted       []int             `json:"omitted_angles,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	SynthesizedAt *time.Time        `json:"synthesized_at,omitempty"`
	StoppedAt     *time.Time        `json:"stopped_at,omitempty"`
	Dir           string            `json:"-"`
}

// RunEnv is what a run needs: the instance's runs directory, and nhm's
// spawn and send (each holds the owner lock itself).
type RunEnv struct {
	RunsDir string
	Spawn   func(ctx context.Context, req spawn.Request) (spawn.Result, error)
	Send    func(ctx context.Context, req send.Request) (send.Result, error)
	Now     func() time.Time
}

func (e RunEnv) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// StartRequest starts a run.
type StartRequest struct {
	Preset      string
	Question    string
	Session     string
	ProjectRoot string
	ProjectID   string
	// Kind and Model are the agent kind and model of every angle
	// (ntm's presets name modes, not kinds); Kind defaults to claude.
	Kind  string
	Model string
}

// Errors.
var (
	ErrNoRun          = errors.New("ensemble: no such run")
	ErrMissingAngles  = errors.New("ensemble: angles without a valid artifact")
	ErrRunStopped     = errors.New("ensemble: the run is stopped")
	ErrNoQuestion     = errors.New("ensemble: a run needs a question")
	runIDPattern      = regexp.MustCompile(`^ens-[0-9]{8}T[0-9]{6}-[0-9a-f]{6}$`)
	markerLinePattern = regexp.MustCompile(`^<!-- nhm-ensemble run=(\S+) angle=([0-9]+) mode=(\S+) -->$`)
)

// NewRunID returns a run id: the UTC start time and six hex digits.
func NewRunID(now time.Time) (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("ensemble: run id: %w", err)
	}
	return "ens-" + now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b), nil
}

// Marker is the first line an angle's artifact must hold.
func Marker(runID string, angle int, mode string) string {
	return fmt.Sprintf("<!-- nhm-ensemble run=%s angle=%d mode=%s -->", runID, angle, mode)
}

// Start resolves the preset, records the run, spawns one agent per angle
// into the run's group, and prompts each with its angle. A failed spawn
// or send of one angle is recorded on that angle; the others go on.
func Start(ctx context.Context, env RunEnv, cat *Catalog, req StartRequest) (Run, error) {
	if strings.TrimSpace(req.Question) == "" {
		return Run{}, ErrNoQuestion
	}
	p, err := cat.Preset(req.Preset)
	if err != nil {
		return Run{}, err
	}
	modes, err := cat.Resolve(p)
	if err != nil {
		return Run{}, err
	}
	kind := req.Kind
	if kind == "" {
		kind = "claude"
	}
	id, err := NewRunID(env.now())
	if err != nil {
		return Run{}, err
	}
	session := req.Session
	if session == "" {
		session = id
	}
	r := Run{ID: id, Preset: p.Name, Strategy: p.Synthesis.Strategy, Question: req.Question, Session: session,
		Group: id, ProjectRoot: req.ProjectRoot, Status: RunStarting, CreatedAt: env.now().UTC(),
		Dir: filepath.Join(env.RunsDir, id)}
	for i, m := range modes {
		r.Angles = append(r.Angles, Angle{Index: i, Mode: m.ID, Code: m.Code, Kind: kind, Model: req.Model,
			Artifact: filepath.Join(r.Dir, fmt.Sprintf("angle-%d-%s.md", i, m.ID))})
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return r, fmt.Errorf("ensemble: %w", err)
	}
	if err := save(r); err != nil {
		return r, err
	}
	sres, serr := env.Spawn(ctx, spawn.Request{Session: session, ProjectRoot: req.ProjectRoot, ProjectID: req.ProjectID,
		Agents: []spawn.Want{{Kind: kind, Count: len(modes), Model: req.Model}}, Group: r.Group, Label: r.Group,
		NoKickoff: true})
	r.SpawnOp, r.WorkspaceID = sres.OperationID, sres.WorkspaceID
	for i := range r.Angles {
		if i >= len(sres.Targets) {
			r.Angles[i].Error = "spawn started no agent for this angle"
			continue
		}
		t := sres.Targets[i]
		r.Angles[i].MailName, r.Angles[i].PaneID, r.Angles[i].BindingID = t.MailName, t.PaneID, t.BindingID
		if !t.Ready || t.MailName == "" {
			r.Angles[i].Error = "agent not ready: " + firstOf(t.Error, t.Status)
		}
	}
	if serr != nil && sres.OperationID == "" {
		r.Status = RunFailed
		_ = save(r)
		return r, fmt.Errorf("ensemble: spawn: %w", serr)
	}
	for i := range r.Angles {
		a := &r.Angles[i]
		if a.Error != "" {
			continue
		}
		res, err := env.Send(ctx, send.Request{Session: session, WorkspaceID: r.WorkspaceID, ProjectRoot: req.ProjectRoot,
			Selector: send.Selector{Names: []string{a.MailName}}, Message: anglePrompt(r, *a, modes[i])})
		a.PromptOp = res.OperationID
		a.PromptStatus = sendStatus(res)
		if err != nil {
			a.Error = "prompt: " + err.Error()
		}
	}
	r.Status = RunRunning
	if serr != nil {
		r.Status = RunFailed
	}
	if err := save(r); err != nil {
		return r, err
	}
	return r, serr
}

// sendStatus is the submission of a send to one agent: its receipt's.
func sendStatus(res send.Result) string {
	if len(res.Targets) == 0 {
		return "no_target"
	}
	return res.Targets[0].Status
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return "unknown"
}

// anglePrompt is the prompt of one angle: the question, the mode's card,
// and where to write the result.
func anglePrompt(r Run, a Angle, m ReasoningMode) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are angle %d of the nhm ensemble run %s (preset %s).\n\n", a.Index, r.ID, r.Preset)
	fmt.Fprintf(&b, "Reason only in this mode: %s (%s, %s). %s\n", m.Name, m.Code, m.Category, m.Description)
	fmt.Fprintf(&b, "It produces: %s\n", m.Outputs)
	if len(m.FailureModes) > 0 {
		fmt.Fprintf(&b, "Avoid: %s\n", strings.Join(m.FailureModes, "; "))
	}
	fmt.Fprintf(&b, "\nThe question:\n%s\n\n", r.Question)
	fmt.Fprintf(&b, "Write your result as Markdown to %s. Its first line must be exactly:\n%s\n", a.Artifact, Marker(r.ID, a.Index, a.Mode))
	b.WriteString("Then reply with the file's path only.\n")
	return b.String()
}

// Load reads a run.
func Load(runsDir, id string) (Run, error) {
	if !runIDPattern.MatchString(id) {
		return Run{}, fmt.Errorf("%w: %q is not a run id", ErrNoRun, id)
	}
	dir := filepath.Join(runsDir, id)
	data, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Run{}, fmt.Errorf("%w: %s", ErrNoRun, id)
	}
	if err != nil {
		return Run{}, fmt.Errorf("ensemble: %w", err)
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return Run{}, fmt.Errorf("ensemble: %s: %w", id, err)
	}
	r.Dir = dir
	return r, nil
}

// List returns every run, newest first.
func List(runsDir string) ([]Run, error) {
	entries, err := os.ReadDir(runsDir)
	if errors.Is(err, os.ErrNotExist) {
		return []Run{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ensemble: %w", err)
	}
	runs := []Run{}
	for _, e := range entries {
		if !e.IsDir() || !runIDPattern.MatchString(e.Name()) {
			continue
		}
		if r, err := Load(runsDir, e.Name()); err == nil {
			runs = append(runs, r)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
	return runs, nil
}

func save(r Run) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("ensemble: %w", err)
	}
	return store.WriteFileAtomic(filepath.Join(r.Dir, "run.json"), append(data, '\n'), 0o600)
}

// ArtifactState is one angle's artifact, as synthesize sees it.
type ArtifactState struct {
	Index  int    `json:"index"`
	Mode   string `json:"mode"`
	Path   string `json:"path"`
	Valid  bool   `json:"valid"`
	Reason string `json:"reason,omitempty"`
}

// Artifacts checks each angle's artifact.
func Artifacts(r Run) []ArtifactState {
	out := make([]ArtifactState, 0, len(r.Angles))
	for _, a := range r.Angles {
		s := ArtifactState{Index: a.Index, Mode: a.Mode, Path: a.Artifact}
		s.Reason = checkArtifact(r, a)
		s.Valid = s.Reason == ""
		out = append(out, s)
	}
	return out
}

// checkArtifact returns why an angle's artifact is not valid, or "".
func checkArtifact(r Run, a Angle) string {
	rel, err := filepath.Rel(r.Dir, a.Artifact)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.Dir(rel) != "." {
		return "not in the run's directory"
	}
	info, err := os.Lstat(a.Artifact)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "missing"
	case err != nil:
		return err.Error()
	case !info.Mode().IsRegular():
		return "not a regular file"
	case info.Size() == 0:
		return "empty"
	}
	f, err := os.Open(a.Artifact)
	if err != nil {
		return err.Error()
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	line, _, _ := strings.Cut(string(head[:n]), "\n")
	m := markerLinePattern.FindStringSubmatch(strings.TrimRight(line, "\r"))
	if m == nil {
		return "its first line is not the run's marker"
	}
	if m[1] != r.ID || m[2] != fmt.Sprint(a.Index) || m[3] != a.Mode {
		return fmt.Sprintf("its marker names run %s angle %s mode %s", m[1], m[2], m[3])
	}
	return ""
}

// SynthesizeRequest synthesizes a run.
type SynthesizeRequest struct {
	RunID string
	// To is the agent that writes the synthesis; "" takes the first angle
	// whose artifact is valid.
	To string
	// Partial synthesizes with angles missing, and records them.
	Partial bool
}

// Synthesize checks the artifacts and sends the synthesis prompt to one
// agent through send.
func Synthesize(ctx context.Context, env RunEnv, req SynthesizeRequest) (Run, error) {
	r, err := Load(env.RunsDir, req.RunID)
	if err != nil {
		return r, err
	}
	if r.Status == RunStopped {
		return r, fmt.Errorf("%w: %s", ErrRunStopped, r.ID)
	}
	states := Artifacts(r)
	var omitted []int
	var valid []ArtifactState
	for _, s := range states {
		if s.Valid {
			valid = append(valid, s)
		} else {
			omitted = append(omitted, s.Index)
		}
	}
	if len(valid) == 0 {
		return r, fmt.Errorf("%w: none of the %d angles has one", ErrMissingAngles, len(states))
	}
	if len(omitted) > 0 && !req.Partial {
		var parts []string
		for _, s := range states {
			if !s.Valid {
				parts = append(parts, fmt.Sprintf("angle %d (%s): %s", s.Index, s.Mode, s.Reason))
			}
		}
		return r, fmt.Errorf("%w: %s; give --partial to synthesize without them", ErrMissingAngles, strings.Join(parts, "; "))
	}
	to := req.To
	if to == "" {
		to = r.Angles[valid[0].Index].MailName
	}
	r.Synthesis = filepath.Join(r.Dir, "synthesis.md")
	res, err := env.Send(ctx, send.Request{Session: r.Session, WorkspaceID: r.WorkspaceID, ProjectRoot: r.ProjectRoot,
		Selector: send.Selector{Names: []string{to}}, Message: synthesisPrompt(r, valid, omitted)})
	if err != nil {
		return r, fmt.Errorf("ensemble: synthesis prompt: %w", err)
	}
	now := env.now().UTC()
	r.SynthesisTo, r.SynthesisOp, r.Omitted, r.SynthesizedAt, r.Status = to, res.OperationID, omitted, &now, RunSynthesizing
	return r, save(r)
}

func synthesisPrompt(r Run, valid []ArtifactState, omitted []int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Synthesize the nhm ensemble run %s (preset %s, strategy %s).\n\n", r.ID, r.Preset, r.Strategy)
	fmt.Fprintf(&b, "The question:\n%s\n\nRead these angles' results:\n", r.Question)
	for _, s := range valid {
		fmt.Fprintf(&b, "- angle %d (%s): %s\n", s.Index, s.Mode, s.Path)
	}
	if len(omitted) > 0 {
		fmt.Fprintf(&b, "\nThese angles gave no result and are left out: %v. Say so in the synthesis.\n", omitted)
	}
	fmt.Fprintf(&b, "\nCombine them with the %s strategy: keep what they agree on, name where they disagree and why, and end with the decisions or actions they support.\n", r.Strategy)
	fmt.Fprintf(&b, "Write the synthesis as Markdown to %s. Its first line must be exactly:\n<!-- nhm-ensemble run=%s synthesis -->\n", r.Synthesis, r.ID)
	b.WriteString("Then reply with the file's path only.\n")
	return b.String()
}

// Stop marks a run stopped. Every artifact stays, and no agent is retired.
func Stop(env RunEnv, id string) (Run, error) {
	r, err := Load(env.RunsDir, id)
	if err != nil {
		return r, err
	}
	if r.Status == RunStopped {
		return r, nil
	}
	now := env.now().UTC()
	r.Status, r.StoppedAt = RunStopped, &now
	return r, save(r)
}
