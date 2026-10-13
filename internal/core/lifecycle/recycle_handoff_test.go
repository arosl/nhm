package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr"
)

// The handoff is about the bead with the newest handoff comment, not the
// first bead in progress (nhm-d2f).
func TestHandoffBead(t *testing.T) {
	at := time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		beads []Bead
		want  string
	}{
		"no bead":          {nil, ""},
		"no handoff":       {[]Bead{{ID: "a"}, {ID: "b"}}, "a"},
		"only the second":  {[]Bead{{ID: "a"}, {ID: "b", HasHandoff: true, HandoffAt: at}}, "b"},
		"the second newer": {[]Bead{{ID: "a", HasHandoff: true, HandoffAt: at}, {ID: "b", HasHandoff: true, HandoffAt: at.Add(time.Minute)}}, "b"},
		"the first newer":  {[]Bead{{ID: "a", HasHandoff: true, HandoffAt: at}, {ID: "b", HasHandoff: true, HandoffAt: at.Add(-time.Minute)}}, "a"},
		"the same time":    {[]Bead{{ID: "a", HasHandoff: true, HandoffAt: at}, {ID: "b", HasHandoff: true, HandoffAt: at}}, "a"},
		"the same second, a higher comment id": {[]Bead{{ID: "a", HasHandoff: true, HandoffAt: at, HandoffID: 7},
			{ID: "b", HasHandoff: true, HandoffAt: at, HandoffID: 9}}, "b"},
		"the same second, a lower comment id": {[]Bead{{ID: "a", HasHandoff: true, HandoffAt: at, HandoffID: 9},
			{ID: "b", HasHandoff: true, HandoffAt: at, HandoffID: 7}}, "a"},
		"a handoff of no time": {[]Bead{{ID: "a"}, {ID: "b", HasHandoff: true}}, "b"},
	} {
		id := ""
		if got := handoffBead(c.beads); got != nil {
			id = got.ID
		}
		if id != c.want {
			t.Errorf("%s: %q, want %q", name, id, c.want)
		}
	}
}

// Recycle's handoff and its kickoff's bead_* placeholders both take the
// bead with the newest handoff comment, and the result names it. Seen:
// the handoff was on the second bead in progress, and recycle read the
// first, which had none.
func TestRecycleHandoffTakesTheNewestHandoff(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		assigned []Bead
		want     string
	}{
		"the handoff on the second": {[]Bead{{ID: "acfs-4"}, {ID: "acfs-11", HasHandoff: true, Handoff: "Handoff: B", HandoffAt: at}}, "acfs-11"},
		"the newer of two": {[]Bead{{ID: "acfs-4", HasHandoff: true, Handoff: "Handoff: A", HandoffAt: at.Add(-time.Hour)},
			{ID: "acfs-11", HasHandoff: true, Handoff: "Handoff: B", HandoffAt: at}}, "acfs-11"},
		"no handoff: the first": {[]Bead{{ID: "acfs-4"}, {ID: "acfs-11"}}, "acfs-4"},
	} {
		w := newWorld(t)
		r := &beadReader{beads: map[string]send.Bead{"acfs-4": {ID: "acfs-4", Title: "Four", Priority: 2},
			"acfs-11": {ID: "acfs-11", Title: "Eleven", Priority: 1}}}
		w.env.Bead = r.bead
		bindIn(t, w, "IcyKnoll", "w1:p1")
		w.facts.assigned["IcyKnoll"] = c.assigned
		var got []string
		w.env.Handoff = func(_ context.Context, _ bindings.Binding, bead *Bead, _ string) Handoff {
			got = append(got, bead.ID)
			return Handoff{Text: bead.Handoff, Sources: []string{"bead"}, Degraded: []string{}}
		}
		res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Template: "polish"})
		if err != nil || res.Stage != StageDone {
			t.Fatalf("%s: recycle = %+v %v", name, res, err)
		}
		if len(got) != 1 || got[0] != c.want || res.HandoffBead != c.want {
			t.Errorf("%s: the handoff read %v, the result names %q; want %s", name, got, res.HandoffBead, c.want)
		}
		if len(w.sent) != 1 || !strings.Contains(w.sent[0].Message, "Polish "+c.want+":") {
			t.Errorf("%s: kickoff %+v, want the placeholders of %s", name, w.sent, c.want)
		}
	}
}

// handoffInWait is the fake herdr where the agent writes its handoff
// while recycle waits for its turn (agent.wait): its beads become after.
type handoffInWait struct {
	*fakeHerdr
	facts *fakeFacts
	after []Bead
}

func (f *handoffInWait) AgentWait(ctx context.Context, p herdr.AgentWaitParams) (herdr.AgentInfo, error) {
	f.facts.assigned["IcyKnoll"] = f.after
	return f.fakeHerdr.AgentWait(ctx, p)
}

// countedFacts is fakeFacts that counts the reads of Assigned.
type countedFacts struct {
	*fakeFacts
	reads int
}

func (f *countedFacts) Assigned(ctx context.Context, root, agent string) ([]Bead, error) {
	f.reads++
	return f.fakeFacts.Assigned(ctx, root, agent)
}

// The agent writes its handoff while recycle --self waits for its turn.
// The kickoff and the handoff both take the bead as it is after the
// wait, from one read under the lock; the read before the wait only
// checks the kickoff.
func TestRecycleKickoffAndHandoffTakeOneRead(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	r := &beadReader{beads: map[string]send.Bead{"acfs-4": {ID: "acfs-4", Title: "Four"}, "acfs-11": {ID: "acfs-11", Title: "Eleven"}}}
	w.env.Bead = r.bead
	bindIn(t, w, "IcyKnoll", "w1:p1")
	w.facts.assigned["IcyKnoll"] = []Bead{{ID: "acfs-4"}, {ID: "acfs-11"}}
	f := &countedFacts{fakeFacts: w.facts}
	w.env.Facts = f
	w.env.Herdr = &handoffInWait{fakeHerdr: w.h, facts: w.facts, after: []Bead{{ID: "acfs-4"},
		{ID: "acfs-11", HasHandoff: true, Handoff: "Handoff: B", HandoffAt: time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)}}}
	var got []string
	w.env.Handoff = func(_ context.Context, _ bindings.Binding, bead *Bead, _ string) Handoff {
		got = append(got, bead.ID)
		return Handoff{Text: bead.Handoff, Sources: []string{"bead"}, Degraded: []string{}}
	}
	res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Template: "polish", WaitTurn: time.Minute})
	if err != nil || res.Stage != StageDone {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if len(got) != 1 || got[0] != "acfs-11" || res.HandoffBead != "acfs-11" ||
		len(w.sent) != 1 || !strings.Contains(w.sent[0].Message, "Polish acfs-11:") {
		t.Fatalf("handoff %v (%q), kickoff %+v: want both of acfs-11", got, res.HandoffBead, w.sent)
	}
	if f.reads != 2 {
		t.Errorf("Assigned read %d times, want 2: the check, then one read after the wait", f.reads)
	}
}

// The agent closes its last bead while recycle --self waits for its turn.
// The render under the lock fails, and the kickoff checked before the
// wait stands, with a warning: an unattended recycle does not stop
// without a word.
func TestRecycleKeepsTheCheckedKickoff(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	r := &beadReader{beads: map[string]send.Bead{"acfs-4": {ID: "acfs-4", Title: "Four"}}}
	w.env.Bead = r.bead
	bindIn(t, w, "IcyKnoll", "w1:p1")
	w.facts.assigned["IcyKnoll"] = []Bead{{ID: "acfs-4"}}
	w.env.Herdr = &handoffInWait{fakeHerdr: w.h, facts: w.facts, after: []Bead{}}
	var got []*Bead
	w.env.Handoff = func(_ context.Context, _ bindings.Binding, bead *Bead, _ string) Handoff {
		got = append(got, bead)
		return Handoff{Sources: []string{}, Degraded: []string{}}
	}
	res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Template: "polish", WaitTurn: time.Minute})
	if err != nil || res.Stage != StageDone || len(w.sent) != 1 || !strings.Contains(w.sent[0].Message, "Polish acfs-4:") {
		t.Fatalf("recycle = %+v %v, kickoff %+v", res, err, w.sent)
	}
	if !slices.ContainsFunc(res.Warnings, func(s string) bool {
		return strings.Contains(s, "the kickoff keeps the bead read before the wait") && strings.Contains(s, "no bead in progress")
	}) {
		t.Errorf("warnings = %q", res.Warnings)
	}
	if len(got) != 1 || got[0] != nil || res.HandoffBead != "" {
		t.Errorf("the handoff read %v (%q), want no bead", got, res.HandoffBead)
	}
}

// A handoff comment whose time does not read is still a handoff, and
// recycle says the pick may be wrong.
func TestRecycleWarnsOfAnUndatedHandoff(t *testing.T) {
	w := newWorld(t)
	bindIn(t, w, "IcyKnoll", "w1:p1")
	w.facts.assigned["IcyKnoll"] = []Bead{{ID: "acfs-4", HasHandoff: true, Handoff: "Handoff: A", HandoffUndated: true},
		{ID: "acfs-11", HasHandoff: true, Handoff: "Handoff: B", HandoffAt: time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)}}
	w.env.Handoff = func(_ context.Context, _ bindings.Binding, bead *Bead, _ string) Handoff {
		return Handoff{Text: bead.Handoff, Sources: []string{"bead"}, Degraded: []string{}}
	}
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "Go on."})
	if err != nil || res.HandoffBead != "acfs-11" {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if !slices.ContainsFunc(res.Warnings, func(s string) bool {
		return strings.Contains(s, "a handoff comment on acfs-4 has a time that does not read")
	}) {
		t.Errorf("warnings = %q", res.Warnings)
	}
}

// fakeBr puts on PATH a br that lists two beads in progress for IcyKnoll,
// with comments1 and comments2 as their comments.
func fakeBr(t *testing.T, comments1, comments2 string) string {
	t.Helper()
	bin, root := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, ".beads", "beads.db"), "")
	writeFile(t, filepath.Join(bin, "br"), "#!/bin/sh\ncase \"$*\" in\n"+
		`*" list --status in_progress "*) echo '{"issues":[{"id":"acfs-1","title":"One","status":"in_progress","assignee":"IcyKnoll"},{"id":"acfs-2","title":"Two","status":"in_progress","assignee":"IcyKnoll"}],"has_more":false}' ;;`+"\n"+
		`*"comments list acfs-1 "*) echo '`+comments1+`' ;;`+"\n"+
		`*"comments list acfs-2 "*) echo '`+comments2+`' ;;`+"\n"+
		"*) echo \"unexpected: $*\" >&2; exit 1 ;;\nesac\n")
	if err := os.Chmod(filepath.Join(bin, "br"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return root
}

// Assigned keeps each bead's newest handoff comment, its time and its id:
// by the time br gives, then, in the same second, by the comment id, not
// by the comment's place in the list. Only a handoff comment's time is
// read. One whose time does not read is still a handoff (retire, reap
// and on-close need no more), counts as the oldest, and marks its bead
// undated.
func TestAssignedReadsTheNewestHandoff(t *testing.T) {
	ctx := context.Background()
	root := fakeBr(t,
		`[{"id":5,"text":"Handoff (D): the newest, in the second of A","created_at":"2026-10-13T05:00:00Z"},`+
			`{"id":1,"text":"Handoff (A): newer than B","created_at":"2026-10-13T05:00:00Z"},{"id":2,"text":"a note","created_at":""},`+
			`{"id":3,"text":"Handoff (B): older, listed later","created_at":"2026-10-13T04:00:00Z"}]`,
		`[{"id":4,"text":"Handoff (C)","created_at":"2026-10-13T05:30:00+02:00"}]`)
	beads, err := CLI{}.Assigned(ctx, root, "IcyKnoll")
	if err != nil || len(beads) != 2 {
		t.Fatalf("Assigned = %+v %v", beads, err)
	}
	if b := beads[0]; !b.HasHandoff || b.Handoff != "Handoff (D): the newest, in the second of A" || b.HandoffID != 5 ||
		!b.HandoffAt.Equal(time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)) {
		t.Errorf("acfs-1 = %+v", b)
	}
	if b := beads[1]; !b.HasHandoff || !b.HandoffAt.Equal(time.Date(2026, 10, 13, 3, 30, 0, 0, time.UTC)) {
		t.Errorf("acfs-2 = %+v, want its handoff at 03:30 UTC", b)
	}
	if b := handoffBead(beads); b.ID != "acfs-1" || b.HandoffUndated || beads[1].HandoffUndated {
		t.Errorf("handoffBead = %+v, want acfs-1 (05:00 UTC is after 03:30 UTC), both dated", b)
	}

	root = fakeBr(t, `[{"id":6,"text":"Handoff (E): dated","created_at":"2026-10-13T05:00:00Z"},`+
		`{"id":7,"text":"Handoff (F): undated, a higher id","created_at":"yesterday"}]`,
		`[{"id":4,"text":"Handoff (C): undated","created_at":"yesterday"}]`)
	beads, err = CLI{}.Assigned(ctx, root, "IcyKnoll")
	if err != nil || len(beads) != 2 {
		t.Fatalf("Assigned with undatable times = %+v %v", beads, err)
	}
	if b := beads[0]; b.Handoff != "Handoff (E): dated" || !b.HandoffUndated {
		t.Errorf("acfs-1 = %+v, want E, marked undated", b)
	}
	if b := beads[1]; !b.HasHandoff || b.Handoff != "Handoff (C): undated" || !b.HandoffAt.IsZero() || !b.HandoffUndated {
		t.Errorf("acfs-2 = %+v, want C at no time, marked undated", b)
	}
	if b := handoffBead(beads); b.ID != "acfs-1" {
		t.Errorf("handoffBead = %s, want acfs-1 (a time beats none)", b.ID)
	}
}
