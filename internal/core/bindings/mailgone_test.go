package bindings

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

// fakeMail answers list_agents and whois from its maps.
type fakeMail struct {
	listed  map[string][]string          // project -> active names
	whois   map[string]map[string]string // name -> whois fields
	listErr map[string]error
	whoErr  map[string]error
	calls   []string
}

func (f *fakeMail) Call(_ context.Context, tool string, args, out any) error {
	a := args.(map[string]any)
	project, _ := a["project_key"].(string)
	switch tool {
	case "list_agents":
		f.calls = append(f.calls, "list_agents "+project)
		if err := f.listErr[project]; err != nil {
			return err
		}
		var rows []map[string]string
		for _, n := range f.listed[project] {
			rows = append(rows, map[string]string{"name": n})
		}
		return remarshal(rows, out)
	case "whois":
		name, _ := a["agent_name"].(string)
		f.calls = append(f.calls, "whois "+name)
		if a["include_recent_commits"] != false {
			return errors.New("whois without include_recent_commits false")
		}
		if err := f.whoErr[name]; err != nil {
			return err
		}
		fields := map[string]any{"name": name}
		for k, v := range f.whois[name] {
			fields[k] = v
		}
		return remarshal(fields, out)
	}
	return errors.New("unexpected tool " + tool)
}

func remarshal(v, out any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func TestReleaseGoneIdentities(t *testing.T) {
	project := t.TempDir()
	real, _ := filepath.EvalSymlinks(project)
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr/herdr.sock", "default", t0)
	if err != nil {
		t.Fatal(err)
	}
	env, home := testEnv(t, session())
	env.InstanceDir = inst.Dir
	now := t0.Add(time.Hour)
	env.Now = func() time.Time { return now }
	ctx := context.Background()
	mk := func(name, pane string) Binding {
		b, err := env.Bindings.Create(ctx, newBinding(name, real, "w1", "w1:t1", pane))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := agentmail.WriteIdentity(home, real, pane, name, t0); err != nil {
			t.Fatal(err)
		}
		if err := store.WriteSecret(inst.Dir, b.ID, "tok-"+name); err != nil {
			t.Fatal(err)
		}
		return b
	}
	live := mk("IcyKnoll", "w1:p1")
	deregistered := mk("WindyPanther", "w1:p2")
	retired := mk("BlueLake", "w1:p3")
	fresh := mk("RedStone", "w1:p4")
	unlisted := mk("GreenCastle", "w1:p5")
	unknown := mk("PinkFox", "w1:p6")
	odd := mk("TanCrane", "w1:p7")
	// A displaced binding of a deregistered identity goes too.
	displaced := mk("RubyDove", "w1:p8")
	if displaced, err = env.Bindings.Displace(ctx, displaced.ID, displaced.Generation, "test"); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-2 * IdentityGoneGrace).Format(time.RFC3339Nano)
	mail := &fakeMail{
		listed: map[string][]string{real: {"icyknoll"}},
		whois: map[string]map[string]string{
			"WindyPanther": {"deregistered_at": old, "retired_at": ""},
			"BlueLake":     {"retired_at": old},
			// Retired a moment ago: nhm retire may still be at work.
			"RedStone": {"retired_at": now.Add(-IdentityGoneGrace / 2).Format(time.RFC3339Nano)},
			// Active, past list_agents' cap.
			"GreenCastle": {},
			// A time nhm cannot read counts at once.
			"TanCrane": {"deregistered_at": "yesterday"},
			"RubyDove": {"deregistered_at": old},
		},
		whoErr: map[string]error{"PinkFox": errors.New("agent not found")},
	}
	gone, warnings := ReleaseGoneIdentities(ctx, env, mail, []Binding{live, deregistered, retired, fresh, unlisted, unknown, odd, displaced})
	got := map[string]string{}
	for _, g := range gone {
		got[g.MailName] = g.State
	}
	want := map[string]string{"WindyPanther": IdentityDeregistered, "BlueLake": IdentityRetired, "TanCrane": IdentityDeregistered,
		"RubyDove": IdentityDeregistered}
	if len(got) != len(want) {
		t.Fatalf("gone = %+v", gone)
	}
	for n, st := range want {
		if got[n] != st {
			t.Errorf("%s = %q, want %q", n, got[n], st)
		}
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "PinkFox") {
		t.Fatalf("warnings = %v", warnings)
	}
	for _, b := range []Binding{deregistered, retired, odd, displaced} {
		r, err := env.Bindings.Get(ctx, b.ID)
		if err != nil || r.Status != Retired || !strings.HasPrefix(r.StatusReason, "the Agent Mail identity was ") {
			t.Errorf("%s = %+v %v", b.MailName, r, err)
		}
		if _, ok, _ := agentmail.ReadIdentity(home, real, b.PaneID); ok {
			t.Errorf("%s: the identity file stays", b.MailName)
		}
		if _, err := store.ReadSecret(inst.Dir, b.ID); !errors.Is(err, store.ErrNoSecret) {
			t.Errorf("%s: the token stays: %v", b.MailName, err)
		}
	}
	for _, b := range []Binding{live, fresh, unlisted, unknown} {
		r, err := env.Bindings.Get(ctx, b.ID)
		if err != nil || r.Status != Active {
			t.Errorf("%s = %+v %v", b.MailName, r, err)
		}
		if _, ok, _ := agentmail.ReadIdentity(home, real, b.PaneID); !ok {
			t.Errorf("%s: the identity file went", b.MailName)
		}
	}
	// One list per project; whois only for the names it leaves out.
	if mail.calls[0] != "list_agents "+real || len(mail.calls) != 8 {
		t.Fatalf("calls = %v", mail.calls)
	}

	// A project Agent Mail cannot list keeps its bindings.
	mail = &fakeMail{listErr: map[string]error{real: errors.New("project not found")}}
	gone, warnings = ReleaseGoneIdentities(ctx, env, mail, []Binding{live})
	if len(gone) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "project not found") {
		t.Fatalf("list failure: %+v %v", gone, warnings)
	}
}
