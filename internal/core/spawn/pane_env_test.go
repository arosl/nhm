package spawn

import (
	"context"
	"testing"
)

// ntm's --pane-env: each agent's pane gets the variables with {project},
// {pane} (its number among the agents of its type) and {role} (its short
// type) filled; nhm's own names win; a bad name or a NUL refuses
// (nhm-2p6.4.7).
func TestSpawnPaneEnv(t *testing.T) {
	w := newWorld(t)
	r := req(Want{Kind: "claude", Count: 2}, Want{Kind: "codex", Count: 1})
	r.PaneEnv = map[string]string{"PROJ": "{project}", "SLOT": "{role}-{pane}", "NHM_AGENT": "mine"}
	res, err := Spawn(context.Background(), w.env, r)
	if err != nil || len(res.Targets) != 3 {
		t.Fatalf("spawn = %v %+v", err, res.Targets)
	}
	for i, want := range []string{"cc-1", "cc-2", "cod-1"} {
		e := w.h.envs[res.Targets[i].PaneID]
		if e["SLOT"] != want || e["PROJ"] != "/p" || e["NHM_AGENT"] != res.Targets[i].MailName {
			t.Errorf("target %d env = %v, want SLOT %s", i, e, want)
		}
	}
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"1BAD": "x"}, `invalid --pane-env name "1BAD"`},
		{map[string]string{"A-B": "x"}, `invalid --pane-env name "A-B"`},
		{map[string]string{"OK": "a\x00b"}, `invalid --pane-env value for "OK": contains NUL`},
	} {
		r := req(Want{Kind: "claude", Count: 1})
		r.PaneEnv = c.env
		if err := r.Validate(); err == nil || err.Error() != c.want {
			t.Errorf("%v = %v, want %q", c.env, err, c.want)
		}
	}
}
