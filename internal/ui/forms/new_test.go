package forms

import (
	"errors"
	"testing"

	"github.com/arosl/nhm/internal/ui"
)

func TestNewValidates(t *testing.T) {
	cases := []struct{ label, project string }{
		{"", "/p"},
		{"  ", "/p"},
		{"-rf", "/p"},
		{"a\x1bb", "/p"},
		{"proj", ""},
		{"proj", "rel/dir"},
	}
	for _, c := range cases {
		fc := &fakeCore{}
		f := NewNewForm(newRunner(t, "workspace.new", fc.core()), ui.Context{Cwd: c.project})
		f.SetLabel(c.label)
		if f.Confirm() != nil || f.Err() == nil || len(fc.created) != 0 || f.Phase() != Editing {
			t.Errorf("%q in %q was accepted", c.label, c.project)
		}
	}
}

func TestNewCreatesOnce(t *testing.T) {
	fc := &fakeCore{}
	f := NewNewForm(newRunner(t, "workspace.new", fc.core()), ui.Context{Cwd: "/data/p/"})
	if f.Project() != "/data/p/" {
		t.Fatalf("project = %q", f.Project())
	}
	f.SetLabel(" proj-a ")
	cmd := f.Confirm()
	if cmd == nil || f.Confirm() != nil {
		t.Fatal("new did not submit once")
	}
	f.Update(do(t, cmd))
	if f.Phase() != Done || len(fc.created) != 1 || fc.created[0] != "proj-a@/data/p#op-1" || f.Result().Status != "created" {
		t.Fatalf("new: %v %v %+v %v", f.Phase(), fc.created, f.Result(), f.Err())
	}
	fc = &fakeCore{newErr: errors.New("workspace proj-a exists (w1)")}
	refused := NewNewForm(newRunner(t, "workspace.new", fc.core()), ui.Context{Cwd: "/p"})
	refused.SetLabel("proj-a")
	refused.Update(do(t, refused.Confirm()))
	if refused.Phase() != Failed || refused.Err() == nil || refused.Err().Error() != "workspace proj-a exists (w1)" {
		t.Fatalf("refused new: %v %v", refused.Phase(), refused.Err())
	}
}
