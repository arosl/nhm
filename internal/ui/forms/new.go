package forms

import (
	"errors"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
)

// NewForm is the menu's new (workspace.new): a label and a project
// directory, then a herdr workspace with that label and the project as
// its cwd, with no agent.
type NewForm struct {
	run     Runner
	ctx     ui.Context
	label   string
	project string

	phase   Phase
	request uint64
	result  actions.Result
	err     error
}

// NewNewForm is a new workspace; the project starts as c's directory.
func NewNewForm(run Runner, c ui.Context) *NewForm {
	return &NewForm{run: run, ctx: c, project: c.Cwd}
}

// Phase is where the form is.
func (f *NewForm) Phase() Phase { return f.phase }

// Label and Project are the form's fields.
func (f *NewForm) Label() string   { return f.label }
func (f *NewForm) Project() string { return f.project }

// Result is new's answer.
func (f *NewForm) Result() actions.Result { return f.result }

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
	if err := actions.ValidLabel(f.label); err != nil {
		return err
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
	req := actions.Request{Context: f.ctx, Params: map[string]string{actions.ParamLabel: strings.TrimSpace(f.label),
		actions.ParamProject: filepath.Clean(strings.TrimSpace(f.project))}}
	key, _, err := f.run.review(req)
	if err == nil {
		req.OperationID, err = f.run.Guard.Submit(key)
	}
	if err != nil {
		f.err = err
		return nil
	}
	f.phase, f.request, f.err = Running, nextRequest(), nil
	return f.run.do(f.request, req)
}

// Update takes the action's answer.
func (f *NewForm) Update(msg tea.Msg) tea.Cmd {
	if m, ok := msg.(done); ok && m.request == f.request && f.phase == Running {
		f.result, f.err, f.phase = m.res, m.err, Done
		if m.err != nil {
			f.phase = Failed
		}
	}
	return nil
}
