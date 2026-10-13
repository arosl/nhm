package cli

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui/runner"
)

// selfStarts records the jobs nhm recycle --self would start.
type selfStarts struct {
	clients []runner.Client
	reqs    []lifecycle.RecycleRequest
}

func (s *selfStarts) use(t *testing.T) {
	t.Helper()
	old := startRecycleJob
	t.Cleanup(func() { startRecycleJob = old })
	startRecycleJob = func(c runner.Client, req lifecycle.RecycleRequest) (string, error) {
		s.clients, s.reqs = append(s.clients, c), append(s.reqs, req)
		return "op-self-1", nil
	}
}

// nhm recycle --self (nhm-abn): the calling pane's agent asks for its own
// recycle. The checks run now; the recycle starts as a job out of the
// pane, frozen at the binding's generation, and waits for the turn.
func TestRecycleSelfStartsAJob(t *testing.T) {
	s, inst := cliInstance(t)
	ownNhm(t, inst)
	project := t.TempDir()
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: project,
		ProjectID: "proj-x", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
			PaneID: "w1:p1", HerdrAlias: "icyknoll", Kind: "claude"}})
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	starts := &selfStarts{}
	starts.use(t)
	sock := "--socket=" + s.SocketPath

	// Outside an agent's pane there is no self.
	if code, _, errOut := run("recycle", "--self", sock); code != exitRefused || !strings.Contains(errOut, "agent's herdr pane") {
		t.Fatalf("no pane = %d %q", code, errOut)
	}
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("HERDR_TAB_ID", "w1:t1")
	// A request the recycle would refuse is refused now.
	if code, _, errOut := run("recycle", "--self", "-t", "no_such_template", sock); code != exitRefused || !strings.Contains(errOut, "kickoff") {
		t.Fatalf("a bad -t = %d %q", code, errOut)
	}
	for _, args := range [][]string{{"recycle", "--self", "IcyKnoll"}, {"recycle", "--self", "--op-id", "op-x"}, {"recycle", "--self", "--wait", "0s"}} {
		if code, _, _ := run(append(args, sock)...); code != exitRefused {
			t.Errorf("%v = %d", args, code)
		}
	}
	if len(starts.reqs) != 0 {
		t.Fatalf("a refused request started a job: %+v", starts.reqs)
	}

	code, out, errOut := run("recycle", "--self", "--prompt", "Take the next bead.", "--wait", "3m", sock, "--json")
	if code != exitOK {
		t.Fatalf("recycle --self = %d %s %s", code, out, errOut)
	}
	env := decodeEnvelope(t, out)
	res := env["result"].(map[string]any)
	if env["operation_id"] != "op-self-1" || res["mail_name"] != "IcyKnoll" || res["binding_id"] != b.ID || res["wait_turn"] != "3m0s" {
		t.Fatalf("envelope = %v", env)
	}
	if len(starts.reqs) != 1 {
		t.Fatalf("jobs = %d", len(starts.reqs))
	}
	req, c := starts.reqs[0], starts.clients[0]
	if req.Target != b.ID || req.ExpectedGeneration != b.Generation || req.WaitTurn != 3*time.Minute || req.Prompt != "Take the next bead." {
		t.Fatalf("request = %+v", req)
	}
	if c.Context.Cwd != project || c.Context.SocketPath != s.SocketPath || c.JobDir != filepath.Join(inst.Dir, "ui", "jobs") ||
		!slices.Contains(c.Env, "HERDR_SOCKET_PATH="+s.SocketPath) ||
		slices.ContainsFunc(c.Env, func(kv string) bool {
			return strings.HasPrefix(kv, "HERDR_PANE_ID=") || strings.HasPrefix(kv, "HERDR_TAB_ID=")
		}) {
		t.Fatalf("job client = %+v", c)
	}
	// A prompt, not the default kickoff: ACFS's default_new_agent is not
	// on every test host.
	if code, out, _ := run("recycle", "--self", "--prompt", "Go on.", sock); code != exitOK || !strings.Contains(out, "end your turn now") {
		t.Fatalf("text = %d %q", code, out)
	}
}

// Under ACFS's Bash, nhm does not recycle: --self is refused before any
// job starts.
func TestRecycleSelfNeedsTheOwner(t *testing.T) {
	s, _ := cliInstance(t)
	starts := &selfStarts{}
	starts.use(t)
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	code, _, _ := run("recycle", "--self", "--socket="+s.SocketPath)
	if code != exitRefused || len(starts.reqs) != 0 {
		t.Fatalf("under the Bash = %d, %d jobs", code, len(starts.reqs))
	}
}
