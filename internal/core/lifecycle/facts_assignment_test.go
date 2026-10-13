package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A bead a spawn assignment claimed for an agent (assignee
// "<Mail name>/ntm-<key>") is the agent's in every fact that reads its
// beads, so retire, recycle and reap see it as they see the agent's own
// claims; another agent's claim, a longer name and an unassigned bead are
// not (nhm-i85).
func TestFactsCountSpawnAssignmentClaims(t *testing.T) {
	bin, root := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, ".beads", "beads.db"), "")
	row := func(id, status, assignee string) string {
		return `{"id":"` + id + `","title":"T","status":"` + status + `","assignee":"` + assignee + `"}`
	}
	list := func(rows ...string) string {
		return `echo '{"issues":[` + strings.Join(rows, ",") + `],"has_more":false}'`
	}
	inProgress := []string{row("acfs-1", "in_progress", "IcyKnoll"), row("acfs-2", "in_progress", "IcyKnoll/ntm-0123456789ab"),
		row("acfs-3", "in_progress", "IcyKnollX/ntm-0123456789ab"), row("acfs-4", "in_progress", "BlueLake/ntm-0123456789ab"),
		row("acfs-5", "in_progress", "")}
	closed := []string{row("acfs-6", "closed", "IcyKnoll/ntm-fedcba987654"), row("acfs-7", "closed", "BlueLake")}
	writeFile(t, filepath.Join(bin, "br"), "#!/bin/sh\ncase \"$*\" in\n"+
		`*"--assignee"*) echo "br filters by name: $*" >&2; exit 1 ;;`+"\n"+
		`*" list --status in_progress "*) `+list(inProgress...)+" ;;\n"+
		`*" list --status closed "*) `+list(closed...)+" ;;\n"+
		`*" list --status all "*) `+list(append(inProgress, closed...)...)+" ;;\n"+
		`*"comments list "*) echo '[]' ;;`+"\n"+
		"*) echo \"unexpected: $*\" >&2; exit 1 ;;\nesac\n")
	if err := os.Chmod(filepath.Join(bin, "br"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx, c := context.Background(), CLI{}

	assigned, err := c.Assigned(ctx, root, "IcyKnoll")
	var ids []string
	for _, b := range assigned {
		ids = append(ids, b.ID)
	}
	if err != nil || !slices.Equal(ids, []string{"acfs-1", "acfs-2"}) {
		t.Fatalf("Assigned = %v %v", ids, err)
	}
	if closedIDs, err := c.Closed(ctx, root, "IcyKnoll"); err != nil || !slices.Equal(closedIDs, []string{"acfs-6"}) {
		t.Fatalf("Closed = %v %v", closedIDs, err)
	}
	for agent, want := range map[string]bool{"IcyKnoll": true, "BlueLake": true, "IcyKnollX": true, "Icy": false} {
		if known, err := c.Known(ctx, root, agent); err != nil || known != want {
			t.Fatalf("Known(%s) = %v %v", agent, known, err)
		}
	}
	got, err := c.InProgress(ctx, root)
	if err != nil || strings.Join(got["IcyKnoll"], ",") != "acfs-1,acfs-2" || strings.Join(got["IcyKnollX"], ",") != "acfs-3" ||
		strings.Join(got["BlueLake"], ",") != "acfs-4" || len(got) != 3 {
		t.Fatalf("InProgress = %v %v", got, err)
	}
}
