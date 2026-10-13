package forms

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/ui/runner"
)

// NewForm is the menu's new: a label and a project directory, then a
// herdr workspace with that label and the project as its cwd, with no
// agent.
type NewForm struct {
	job     tracker
	label   string
	project string

	phase       Phase
	workspaceID string
	err         error
}

// NewNewForm is a new workspace; project starts as the captured pane's
// project.
func NewNewForm(jobs Jobs, project string) *NewForm {
	return &NewForm{job: tracker{jobs: jobs}, project: project}
}

// Phase is where the form is.
func (f *NewForm) Phase() Phase { return f.phase }

// Label and Project are the form's fields.
func (f *NewForm) Label() string   { return f.label }
func (f *NewForm) Project() string { return f.project }

// WorkspaceID is the created workspace.
func (f *NewForm) WorkspaceID() string { return f.workspaceID }

// JobID is the job, once it started.
func (f *NewForm) JobID() string { return f.job.id }

// Err is the error the form ended with, or why it refused a confirm.
func (f *NewForm) Err() error { return f.err }

// SetLabel and SetProject edit the fields.
func (f *NewForm) SetLabel(s string) {
	if f.phase < Running {
		f.label, f.err = s, nil
	}
}

func (f *NewForm) SetProject(s string) {
	if f.phase < Running {
		f.project, f.err = s, nil
	}
}

// Validate checks the fields; nhm new checks the rest (a label in use, a
// directory that is not there).
func (f *NewForm) Validate() error {
	label := strings.TrimSpace(f.label)
	switch {
	case label == "":
		return errors.New("give the workspace a label")
	case utf8.RuneCountInString(label) > 128:
		return errors.New("the label is longer than 128 characters")
	case strings.HasPrefix(label, "-"):
		return errors.New("a label must not start with '-'")
	case strings.IndexFunc(label, unicode.IsControl) >= 0:
		return errors.New("the label holds a control character")
	}
	if p := strings.TrimSpace(f.project); p == "" || !filepath.IsAbs(p) {
		return errors.New("give the project as an absolute directory")
	}
	return nil
}

// Confirm creates the workspace once.
func (f *NewForm) Confirm() tea.Cmd {
	if f.phase >= Running {
		return nil
	}
	if err := f.Validate(); err != nil {
		f.err = err
		return nil
	}
	f.phase, f.err = Running, nil
	return f.job.start(runner.VerbNew, runner.NewRequest{Label: strings.TrimSpace(f.label),
		Project: filepath.Clean(strings.TrimSpace(f.project))})
}

// Update takes the job's messages.
func (f *NewForm) Update(msg tea.Msg) tea.Cmd {
	if f.phase != Running {
		return nil
	}
	cmd, done := f.job.handle(msg)
	if !done {
		return cmd
	}
	f.phase = Failed
	if f.err = f.job.startErr; f.err != nil {
		return nil
	}
	r := *f.job.ended
	var out struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if len(r.Result) > 0 {
		if err := json.Unmarshal(r.Result, &out); err != nil {
			f.err = fmt.Errorf("new's result cannot be read: %w", err)
			return nil
		}
	}
	f.workspaceID = out.WorkspaceID
	if f.err = jobError(r); f.err == nil {
		f.phase = Done
	}
	return nil
}
