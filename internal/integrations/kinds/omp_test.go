package kinds

import (
	"slices"
	"testing"
)

// ompSession is a native session of omp 18.14.1 as herdr reports it: the
// path of the session's .jsonl, <time>_<id>.jsonl.
const ompSession = "/home/u/.omp/agent/sessions/--data-projects-x--/2026-11-02T00-43-21-660Z_01a199b5-f6bc-7731-bc27-11b8e4bd7887.jsonl"

// omp ends by ntm's exit keys, not a prompt, and resumes by the session
// id in the name of the .jsonl that herdr reports, with the model and the
// effort of its launch (ntm's agentsession.ResumeCommand; nhm-dv41).
func TestOmpExitAndResume(t *testing.T) {
	o, _ := NewRegistry().Get("omp")
	if !o.ExitByKeys || o.ExitCommand != "" || o.NewSession != "/new" {
		t.Fatalf("omp exit = %+v", o)
	}
	if got := o.Resume(ompSession, "", "", ""); !slices.Equal(got, []string{"omp", "--resume", "01a199b5-f6bc-7731-bc27-11b8e4bd7887"}) {
		t.Fatalf("resume = %v", got)
	}
	if got := o.Resume(ompSession, "opencode-go/glm-5.3", "high", ""); !slices.Equal(got, []string{"omp", "--resume",
		"01a199b5-f6bc-7731-bc27-11b8e4bd7887", "--model", "opencode-go/glm-5.3", "--thinking", "high"}) {
		t.Fatalf("resume with model and effort = %v", got)
	}
	// A plain id is the id.
	if got := o.Resume("01a199b5-f6bc", "", "", ""); !slices.Equal(got, []string{"omp", "--resume", "01a199b5-f6bc"}) {
		t.Fatalf("resume of an id = %v", got)
	}
	// A value with no id in omp's form never reaches an argv.
	for _, bad := range []string{
		"",
		"-rf",
		"relative/2026_abc.jsonl",
		"/x/2026-11-02T00-43-21-660Z.jsonl",
		"/x/2026_abc.json",
		"/x/2026_-rf.jsonl",
		"/x/2026_a_b.jsonl",
		"/x/2026_a.b.jsonl",
		"/x/2026_a b.jsonl",
		"/x/2026_.jsonl",
		"a.b",
		"a_b",
	} {
		if got := o.Resume(bad, "", "", ""); got != nil {
			t.Errorf("resume %q = %v", bad, got)
		}
	}
	// Other kinds take the value as it is.
	if c, _ := NewRegistry().Get("claude"); c.SessionID != nil || c.ExitByKeys {
		t.Fatalf("claude = %+v", c)
	}
}
