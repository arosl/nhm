package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

func TestLoadMaintainConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if c, err := loadMaintainConfig(); err != nil || c.RecycleOnClose || c.RecycleTemplate != "" {
		t.Fatalf("no file = %+v %v", c, err)
	}
	write := func(body string) {
		t.Helper()
		dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "nhm")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("[ui]\nmenu = \"go\"\n[maintain]\nrecycle_on_close = true\nrecycle_template = \" next_bead \"\n")
	if c, err := loadMaintainConfig(); err != nil || !c.RecycleOnClose || c.RecycleTemplate != "next_bead" {
		t.Fatalf("set = %+v %v", c, err)
	}
	for _, bad := range []string{"[maintain]\nrecycle_on_closed = true\n", "[maintain]\nrecycle_on_close = \"yes\"\n",
		"[maintain]\nrecycle_template = 3\n"} {
		write(bad)
		if _, err := loadMaintainConfig(); err == nil {
			t.Errorf("%q loaded", bad)
		}
	}
}

// onCloseFacts are an agent's beads in progress and closed.
type onCloseFacts struct {
	cliFacts
	inProgress []lifecycle.Bead
	closed     []string
}

func (f *onCloseFacts) Assigned(context.Context, string, string) ([]lifecycle.Bead, error) {
	return f.inProgress, nil
}
func (f *onCloseFacts) Closed(context.Context, string, string) ([]string, error) {
	return f.closed, nil
}

// nhm maintain's recycle on close (nhm-abn): a bead closes, the agent is
// idle with nothing in progress on two checks, and the recycle starts as
// a job, once; the state survives between passes in the instance.
func TestRecycleOnClose(t *testing.T) {
	s, inst := cliInstance(t)
	status := "idle"
	s.Handle("agent.list", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "agent_list", "agents": []any{map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1",
			"workspace_id": "w1", "terminal_id": "t-w1:p1", "agent": "claude", "agent_status": status, "focused": false, "revision": 1}}}, nil
	})
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".nhm", "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".nhm", "templates", "next.md"), []byte("Take the next bead with bv --robot-next."), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bs := bindings.NewStore(db)
	b, err := bs.Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: project, ProjectID: "proj-x",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
			HerdrAlias: "icyknoll", Kind: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	ie := &instanceEnv{inst: inst, db: db, herdr: herdr.New(s.SocketPath)}
	facts := &onCloseFacts{closed: []string{"acfs-1"}}
	lenv := lifecycle.Env{DB: db, Bindings: bs, Facts: facts}
	starts := &selfStarts{}
	starts.use(t)
	cfg := maintainConfig{RecycleOnClose: true, RecycleTemplate: "next"}
	now := time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)
	pass := func() ([]string, []string) {
		t.Helper()
		started, warnings := recycleOnClose(context.Background(), ie, cfg, lenv, now)
		now = now.Add(time.Minute)
		return started, warnings
	}

	if started, warnings := pass(); len(started) != 0 || len(warnings) != 0 {
		t.Fatalf("first pass = %v %v", started, warnings)
	}
	facts.closed = []string{"acfs-1", "acfs-2"}
	if started, _ := pass(); len(started) != 0 {
		t.Fatalf("the first idle check started %v", started)
	}
	started, warnings := pass()
	if len(started) != 1 || !strings.Contains(started[0], "IcyKnoll: bead acfs-2 closed; recycle started as op-self-1") || len(warnings) != 0 {
		t.Fatalf("second idle check = %v %v", started, warnings)
	}
	req, c := starts.reqs[0], starts.clients[0]
	if req.Target != b.ID || req.ExpectedGeneration != b.Generation || req.Template != "next" || req.WaitTurn != onCloseWait ||
		c.Context.Cwd != project {
		t.Fatalf("request = %+v, client %+v", req, c)
	}
	if started, _ := pass(); len(started) != 0 || len(starts.reqs) != 1 {
		t.Fatalf("a generation was recycled twice: %v", started)
	}
	if data, err := os.ReadFile(filepath.Join(inst.Dir, onCloseFile)); err != nil || !strings.Contains(string(data), `"recycled": "op-self-1"`) {
		t.Fatalf("state = %s %v", data, err)
	}

	// A recycle the checks refuse is said once and not tried again.
	if err := os.Remove(filepath.Join(inst.Dir, onCloseFile)); err != nil {
		t.Fatal(err)
	}
	cfg.RecycleTemplate = "no_such_template"
	facts.closed = []string{"acfs-1"}
	pass()
	facts.closed = []string{"acfs-1", "acfs-3"}
	pass()
	if started, warnings := pass(); len(started) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "recycle is refused") {
		t.Fatalf("refused = %v %v", started, warnings)
	}
	if started, warnings := pass(); len(started) != 0 || len(warnings) != 0 || len(starts.reqs) != 1 {
		t.Fatalf("after the refusal = %v %v", started, warnings)
	}
}
