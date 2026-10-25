package zoom

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// fakeHerdr records the calls in order; an error set for a method makes
// that method fail.
type fakeHerdr struct {
	calls  []string
	errs   map[string]error
	tab    string
	reason string
}

func (f *fakeHerdr) WorkspaceFocus(_ context.Context, ws string) error {
	f.calls = append(f.calls, "workspace.focus "+ws)
	return f.errs["workspace.focus"]
}

func (f *fakeHerdr) PaneZoom(_ context.Context, pane, mode string) (herdr.ZoomResult, error) {
	f.calls = append(f.calls, "pane.zoom "+pane+" "+mode)
	if err := f.errs["pane.zoom"]; err != nil {
		return herdr.ZoomResult{}, err
	}
	zoomed := f.reason != herdr.ZoomSinglePane
	return herdr.ZoomResult{PaneID: pane, Zoomed: zoomed, Reason: f.reason,
		Layout: herdr.ZoomLayout{WorkspaceID: "w1", TabID: f.tab, Zoomed: zoomed}}, nil
}

func (f *fakeHerdr) TabFocus(_ context.Context, tab string) (herdr.TabInfo, error) {
	f.calls = append(f.calls, "tab.focus "+tab)
	return herdr.TabInfo{TabID: tab}, f.errs["tab.focus"]
}

func TestPaneZoomsAndBringsItsTabInFront(t *testing.T) {
	h := &fakeHerdr{tab: "w1:t2"}
	z, err := Pane(context.Background(), h, "w1", "w1:p2")
	if err != nil || !z.Zoomed {
		t.Fatalf("zoom = %+v %v", z, err)
	}
	if got := strings.Join(h.calls, ";"); got != "workspace.focus w1;pane.zoom w1:p2 on;tab.focus w1:t2" {
		t.Fatalf("calls = %s", got)
	}
}

// A failed workspace focus or zoom is the zoom's error, never an
// *Unshown: the pane is not zoomed.
func TestFailedZoomIsNotUnshown(t *testing.T) {
	for _, method := range []string{"workspace.focus", "pane.zoom"} {
		boom := errors.New("herdr: " + method + " failed")
		h := &fakeHerdr{tab: "w1:t2", errs: map[string]error{method: boom}}
		_, err := Pane(context.Background(), h, "w1", "w1:p2")
		var unshown *Unshown
		if !errors.Is(err, boom) || errors.As(err, &unshown) {
			t.Errorf("%s fails: zoom = %v", method, err)
		}
		if strings.Contains(strings.Join(h.calls, ";"), "tab.focus") {
			t.Errorf("%s fails: the tab was focused: %v", method, h.calls)
		}
	}
}

// The pane is zoomed but its tab stays behind: an *Unshown with herdr's
// reason.
func TestTabBehindIsUnshown(t *testing.T) {
	h := &fakeHerdr{tab: "w1:t2", errs: map[string]error{"tab.focus": errors.New("no such tab")}}
	z, err := Pane(context.Background(), h, "w1", "w1:p2")
	var unshown *Unshown
	if !errors.As(err, &unshown) || unshown.PaneID != "w1:p2" || unshown.Reason != "no such tab" || !z.Zoomed {
		t.Fatalf("zoom = %+v %v", z, err)
	}
	if err.Error() != "zoomed w1:p2, but herdr did not bring its tab in front: no such tab" {
		t.Fatalf("message = %q", err)
	}
	h = &fakeHerdr{}
	if _, err := Pane(context.Background(), h, "w1", "w1:p2"); !errors.As(err, &unshown) ||
		strings.Contains(strings.Join(h.calls, ";"), "tab.focus") {
		t.Fatalf("no tab in the layout = %v %v", err, h.calls)
	}
}

// A pane alone in its tab is not zoomed (herdr: single_pane), but its tab
// comes in front: an *Alone, not a failure. A tab that stays behind is
// still an *Unshown.
func TestPaneAloneInItsTab(t *testing.T) {
	h := &fakeHerdr{tab: "w1:t2", reason: herdr.ZoomSinglePane}
	z, err := Pane(context.Background(), h, "w1", "w1:p2")
	var alone *Alone
	if !errors.As(err, &alone) || alone.PaneID != "w1:p2" || z.Zoomed || err.Error() != "w1:p2 is alone in its tab: nothing to zoom" {
		t.Fatalf("zoom = %+v %v", z, err)
	}
	if got := strings.Join(h.calls, ";"); got != "workspace.focus w1;pane.zoom w1:p2 on;tab.focus w1:t2" {
		t.Fatalf("calls = %s", got)
	}
	h = &fakeHerdr{tab: "w1:t2", reason: herdr.ZoomSinglePane, errs: map[string]error{"tab.focus": errors.New("no such tab")}}
	var unshown *Unshown
	if _, err := Pane(context.Background(), h, "w1", "w1:p2"); !errors.As(err, &unshown) || errors.As(err, &alone) {
		t.Fatalf("alone, with its tab behind = %v", err)
	}
	h = &fakeHerdr{tab: "w1:t2", reason: "already_zoomed"}
	if _, err := Pane(context.Background(), h, "w1", "w1:p2"); err != nil {
		t.Fatalf("already zoomed = %v", err)
	}
}

// Show focuses the workspace, then brings the zoomed pane's tab in front;
// a failed focus is the error, a tab that stays behind an *Unshown.
func TestShowFocusesTheWorkspaceThenTheTab(t *testing.T) {
	z := herdr.ZoomResult{PaneID: "w1:p2", Layout: herdr.ZoomLayout{WorkspaceID: "w1", TabID: "w1:t2"}}
	h := &fakeHerdr{}
	if err := Show(context.Background(), h, "w1", "w1:p2", z); err != nil {
		t.Fatalf("show = %v", err)
	}
	if got := strings.Join(h.calls, ";"); got != "workspace.focus w1;tab.focus w1:t2" {
		t.Fatalf("calls = %s", got)
	}
	boom := errors.New("herdr: workspace.focus failed")
	h = &fakeHerdr{errs: map[string]error{"workspace.focus": boom}}
	if err := Show(context.Background(), h, "w1", "w1:p2", z); !errors.Is(err, boom) || strings.Contains(strings.Join(h.calls, ";"), "tab.focus") {
		t.Fatalf("failed focus = %v, calls %v", err, h.calls)
	}
	var unshown *Unshown
	h = &fakeHerdr{errs: map[string]error{"tab.focus": errors.New("no such tab")}}
	if err := Show(context.Background(), h, "w1", "w1:p2", z); !errors.As(err, &unshown) || unshown.PaneID != "w1:p2" {
		t.Fatalf("tab behind = %v", err)
	}
	if err := Show(context.Background(), &fakeHerdr{}, "w1", "w1:p2", herdr.ZoomResult{}); !errors.As(err, &unshown) {
		t.Fatalf("no tab = %v", err)
	}
}

// Only zooms the pane and focuses nothing, as ntm's zoom under --json.
func TestOnlyZoomsAndFocusesNothing(t *testing.T) {
	h := &fakeHerdr{tab: "w1:t2"}
	z, err := Only(context.Background(), h, "w1:p2")
	if err != nil || !z.Zoomed {
		t.Fatalf("zoom = %+v %v", z, err)
	}
	if got := strings.Join(h.calls, ";"); got != "pane.zoom w1:p2 on" {
		t.Fatalf("calls = %s", got)
	}
	h = &fakeHerdr{tab: "w1:t2", reason: herdr.ZoomSinglePane}
	var alone *Alone
	if _, err := Only(context.Background(), h, "w1:p2"); !errors.As(err, &alone) {
		t.Fatalf("single pane = %v", err)
	}
	boom := errors.New("herdr: pane.zoom failed")
	h = &fakeHerdr{errs: map[string]error{"pane.zoom": boom}}
	if _, err := Only(context.Background(), h, "w1:p2"); !errors.Is(err, boom) {
		t.Fatalf("failed zoom = %v", err)
	}
}
