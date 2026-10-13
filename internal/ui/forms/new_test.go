package forms

import (
	"testing"

	"github.com/arosl/nhm/internal/ui/runner"
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
		jobs := &fakeJobs{}
		f := NewNewForm(jobs, c.project)
		f.SetLabel(c.label)
		if f.Confirm() != nil || f.Err() == nil || len(jobs.started) != 0 || f.Phase() != Editing {
			t.Errorf("%q in %q was accepted", c.label, c.project)
		}
	}
}

func TestNewCreatesOnce(t *testing.T) {
	jobs := &fakeJobs{}
	f := NewNewForm(jobs, "/data/p/")
	f.SetLabel(" proj-a ")
	cmd := f.Confirm()
	if cmd == nil || f.Confirm() != nil {
		t.Fatal("new did not submit once")
	}
	f.Update(do(t, cmd))
	if req := jobs.started[0].req.(runner.NewRequest); req.Label != "proj-a" || req.Project != "/data/p" || len(jobs.started) != 1 {
		t.Fatalf("job = %+v", jobs.started)
	}
	jobs.end(t, "op-1", 0, "", map[string]string{"session": "proj-a", "workspace_id": "w9"})
	poll(t, f.Update, f.job.request)
	if f.Phase() != Done || f.WorkspaceID() != "w9" {
		t.Fatalf("new: %v %q %v", f.Phase(), f.WorkspaceID(), f.Err())
	}
	refused := NewNewForm(&fakeJobs{}, "/p")
	refused.SetLabel("proj-a")
	refused.Update(do(t, refused.Confirm()))
	refused.job.jobs.(*fakeJobs).end(t, "op-1", runner.CodeRefused, "workspace proj-a exists (w1)", nil)
	poll(t, refused.Update, refused.job.request)
	if refused.Phase() != Failed || refused.Err() == nil || refused.Err().Error() != "workspace proj-a exists (w1)" {
		t.Fatalf("refused new: %v %v", refused.Phase(), refused.Err())
	}
}
