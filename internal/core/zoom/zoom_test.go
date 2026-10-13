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
	calls []string
	errs  map[string]error
	tab   string
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
	return herdr.ZoomResult{PaneID: pane, Zoomed: true, Layout: herdr.ZoomLayout{WorkspaceID: "w1", TabID: f.tab, Zoomed: true}}, nil
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
