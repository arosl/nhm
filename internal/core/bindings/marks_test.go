package bindings

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// Adopt gives the pane both marks, as spawn does: the label and the
// sidebar tokens mail and model (nhm-hae).
func TestAdoptMarksThePane(t *testing.T) {
	h := session(paneSpec{pane: "w1:p1", kind: "claude", session: "sess-9", tabLabel: "5"})
	env, _ := testEnv(t, h)
	res, err := Adopt(context.Background(), env, AdoptRequest{MailName: "IcyKnoll", PaneID: "w1:p1", ProjectKey: t.TempDir(),
		ProjectID: "proj-x", Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if h.paneLabels["w1:p1"] != "IcyKnoll opus" || !maps.Equal(h.paneTokens["w1:p1"], map[string]string{"mail": "IcyKnoll", "model": "opus"}) ||
		len(res.Warnings) != 0 {
		t.Fatalf("marks: label %v, tokens %v, warnings %v", h.paneLabels, h.paneTokens, res.Warnings)
	}
}

// markRecorder records Mark's and KeepMarks's calls; fail makes both
// calls fail.
type markRecorder struct {
	calls []string
	fail  error
}

func (m *markRecorder) PaneRename(_ context.Context, pane, label string) (herdr.PaneInfo, error) {
	m.calls = append(m.calls, "rename "+pane+" "+label)
	return herdr.PaneInfo{}, m.fail
}

func (m *markRecorder) PaneReportMetadata(_ context.Context, p herdr.ReportMetadataParams) error {
	m.calls = append(m.calls, "report "+p.PaneID+" "+p.Source+" "+p.Agent+" mail="+p.Tokens["mail"]+" model="+p.Tokens["model"])
	return m.fail
}

func TestMarkSetsBothMarks(t *testing.T) {
	b := Binding{MailName: "IcyKnoll", Kind: "claude", Model: "opus", PaneID: "w1:p1"}
	m := &markRecorder{}
	done, warnings := Mark(context.Background(), m, b)
	if !slices.Equal(m.calls, []string{"rename w1:p1 IcyKnoll opus", "report w1:p1 nhm claude mail=IcyKnoll model=opus"}) ||
		!slices.Equal(done, []string{"pane label", "sidebar tokens"}) || len(warnings) != 0 {
		t.Fatalf("calls %v, done %v, warnings %v", m.calls, done, warnings)
	}
	// Without a model: the name alone, and no model token.
	b.Model = ""
	if PaneLabel(b) != "IcyKnoll" || !maps.Equal(MarkTokens(b), map[string]string{"mail": "IcyKnoll"}) {
		t.Fatalf("no model: label %q, tokens %v", PaneLabel(b), MarkTokens(b))
	}
	// Failures are warnings, one per mark.
	m = &markRecorder{fail: errors.New("pane_not_found")}
	if done, warnings = Mark(context.Background(), m, b); len(done) != 0 ||
		!slices.Equal(warnings, []string{"pane label: pane_not_found", "sidebar tokens: pane_not_found"}) {
		t.Fatalf("failing herdr: done %v, warnings %v", done, warnings)
	}
}

func TestKeepMarksAppliesOnlyWhatIsMissing(t *testing.T) {
	b := Binding{MailName: "IcyKnoll", Kind: "claude", Model: "opus", PaneID: "w1:p1"}
	full := map[string]string{"mail": "IcyKnoll", "model": "opus", "group": "g1"}
	for _, c := range []struct {
		name   string
		pane   herdr.PaneInfo
		calls  []string
		failed bool
	}{
		{"both there", herdr.PaneInfo{Label: "IcyKnoll opus", Tokens: full}, nil, false},
		{"another label stays", herdr.PaneInfo{Label: "reviewer", Tokens: full}, nil, false},
		{"no marks", herdr.PaneInfo{}, []string{"rename w1:p1 IcyKnoll opus", "report w1:p1 nhm claude mail=IcyKnoll model=opus"}, false},
		{"no label", herdr.PaneInfo{Tokens: full}, []string{"rename w1:p1 IcyKnoll opus"}, false},
		{"a token missing", herdr.PaneInfo{Label: "x", Tokens: map[string]string{"mail": "IcyKnoll"}},
			[]string{"report w1:p1 nhm claude mail=IcyKnoll model=opus"}, false},
		{"another agent's token", herdr.PaneInfo{Label: "x", Tokens: map[string]string{"mail": "BlueLake", "model": "opus"}},
			[]string{"report w1:p1 nhm claude mail=IcyKnoll model=opus"}, false},
		{"herdr fails", herdr.PaneInfo{}, []string{"rename w1:p1 IcyKnoll opus", "report w1:p1 nhm claude mail=IcyKnoll model=opus"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := &markRecorder{}
			if c.failed {
				m.fail = errors.New("boom")
			}
			done, warnings := KeepMarks(context.Background(), m, b, c.pane)
			if !slices.Equal(m.calls, c.calls) {
				t.Fatalf("calls %v, want %v", m.calls, c.calls)
			}
			if c.failed && (len(done) != 0 || len(warnings) != len(c.calls)) || !c.failed && (len(done) != len(c.calls) || len(warnings) != 0) {
				t.Fatalf("done %v, warnings %v", done, warnings)
			}
		})
	}
}
