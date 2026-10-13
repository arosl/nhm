package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr"
)

// bindIn is bind for an agent of a project with a kickoff template
// "polish" that has bead placeholders, and one ("plain") that has none.
func bindIn(t *testing.T, w *world, name, pane string) bindings.Binding {
	t.Helper()
	project := t.TempDir()
	dir := filepath.Join(project, ".nhm", "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, body := range map[string]string{"polish.md": "Polish {{bead_id}}: {{bead_title}} ({{bead_priority}}).",
		"plain.md": "Carry on, {{agent}}."} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := w.env.Bindings.Create(context.Background(), bindings.New{MailName: name, MailProjectKey: project, ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: pane,
			HerdrAlias: strings.ToLower(name), Kind: "claude", Model: "opus", NativeSession: &bindings.NativeSession{Value: "sess-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	w.h.add(pane, "claude", strings.ToLower(name), "sess-1", herdr.StatusIdle)
	return b
}

// beadReader is Env.Bead over a fixed set of beads; it records the ids
// it read.
type beadReader struct {
	beads map[string]send.Bead
	read  []string
}

func (r *beadReader) bead(_ context.Context, _, id string) (send.Bead, error) {
	r.read = append(r.read, id)
	b, ok := r.beads[id]
	if !ok {
		return send.Bead{}, errors.New("br show " + id + ": not found")
	}
	return b, nil
}

// A kickoff template's bead_* placeholders come from the agent's bead in
// progress, the one the handoff reads, or from --bead (nhm-e8c).
func TestRecycleKickoffFillsBeadPlaceholders(t *testing.T) {
	ctx := context.Background()
	beads := map[string]send.Bead{
		"acfs-7": {ID: "acfs-7", Title: "Fix the login", Priority: 1, Status: "in_progress", IssueType: "bug"},
		"acfs-8": {ID: "acfs-8", Title: "Improve the beads", Priority: 2, Status: "open", IssueType: "task"},
	}

	// The agent's bead in progress.
	w := newWorld(t)
	r := &beadReader{beads: beads}
	w.env.Bead = r.bead
	bindIn(t, w, "IcyKnoll", "w1:p1")
	w.facts.assigned["IcyKnoll"] = []Bead{{ID: "acfs-7", Title: "Fix the login"}}
	res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Template: "polish"})
	if err != nil || res.Stage != StageDone || len(w.sent) != 1 || !strings.HasSuffix(w.sent[0].Message, "Polish acfs-7: Fix the login (P1).") {
		t.Fatalf("recycle = %+v %v, kickoff %+v", res, err, w.sent)
	}

	// --bead names another one.
	w = newWorld(t)
	r = &beadReader{beads: beads}
	w.env.Bead = r.bead
	bindIn(t, w, "IcyKnoll", "w1:p1")
	w.facts.assigned["IcyKnoll"] = []Bead{{ID: "acfs-7"}}
	res, err = Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Template: "polish", BeadID: "acfs-8"})
	if err != nil || len(w.sent) != 1 || !strings.HasSuffix(w.sent[0].Message, "Polish acfs-8: Improve the beads (P2).") ||
		len(r.read) != 1 || r.read[0] != "acfs-8" {
		t.Fatalf("recycle --bead = %+v %v, kickoff %+v, read %v", res, err, w.sent, r.read)
	}

	// A template with no bead placeholders reads no bead.
	w = newWorld(t)
	r = &beadReader{beads: beads}
	w.env.Bead = r.bead
	bindIn(t, w, "IcyKnoll", "w1:p1")
	if res, err = Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Template: "plain"}); err != nil ||
		!strings.HasSuffix(w.sent[0].Message, "Carry on, IcyKnoll.") || len(r.read) != 0 {
		t.Fatalf("plain = %+v %v, read %v", res, err, r.read)
	}
}

// With no bead to fill the placeholders, an unknown --bead, or --bead
// with --prompt, recycle refuses at its check: nothing stops.
func TestRecycleKickoffWithoutABeadIsRefused(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		req  RecycleRequest
		want string
	}{
		"no bead in progress": {RecycleRequest{Target: "IcyKnoll", Template: "polish"}, "no bead in progress: give --bead ID"},
		"unknown --bead":      {RecycleRequest{Target: "IcyKnoll", Template: "polish", BeadID: "acfs-404"}, "bead acfs-404: br show acfs-404: not found"},
		"--bead with --prompt": {RecycleRequest{Target: "IcyKnoll", Prompt: "Go on.", BeadID: "acfs-7"},
			"--bead fills a template's bead_* placeholders"},
	} {
		w := newWorld(t)
		r := &beadReader{beads: map[string]send.Bead{"acfs-7": {ID: "acfs-7"}}}
		w.env.Bead = r.bead
		bindIn(t, w, "IcyKnoll", "w1:p1")
		res, err := Recycle(ctx, w.env, c.req)
		if err == nil || res.Stage != StageCheck || !strings.Contains(err.Error(), c.want) || len(w.h.prompts) != 0 || len(w.h.starts) != 0 {
			t.Errorf("%s: recycle = %+v %v (prompts %v)", name, res, err, w.h.prompts)
		}
	}
}
