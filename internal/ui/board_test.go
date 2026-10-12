package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/guards"
)

func boardModel() *snapshot.Model {
	return &snapshot.Model{
		Agents: []snapshot.Agent{
			{PaneID: "p1", Slot: "1.1", Kind: "claude", State: herdr.StatusIdle,
				Binding: &snapshot.BindingRef{ID: "b1", MailName: "IcyKnoll"},
				Mail:    &snapshot.MailState{Status: snapshot.Fresh, Unread: 2, PendingAcks: 1}},
			{PaneID: "p2", Slot: "1.2", Kind: "codex", State: herdr.StatusBlocked, HerdrName: "loose",
				Guards: map[string]guards.State{guards.DCG: guards.Untrusted, guards.RCH: guards.Configured}},
			{PaneID: "p3", Kind: "claude", State: herdr.StatusWorking, Limit: "usage limit reached",
				Binding: &snapshot.BindingRef{ID: "b3", MailName: "RedFox"}},
		},
		Absent: []bindings.Binding{{ID: "b4", MailName: "GoneOwl", Kind: "claude", Slot: "1.4"}},
	}
}

func TestBoardRows(t *testing.T) {
	rows := BoardRows(boardModel())
	var panes []string
	for _, r := range rows {
		panes = append(panes, r.PaneID)
	}
	if want := []string{"p2", "p1", "p3", "-"}; !reflect.DeepEqual(panes, want) {
		t.Fatalf("order = %v, want blocked first, absent last: %v", panes, want)
	}
	if r := rows[0]; r.MailName != "" || !strings.Contains(r.Text, "loose") || !strings.Contains(r.Text, "unguarded: dcg") ||
		strings.Contains(r.Text, "rch") {
		t.Errorf("unbound blocked row = %+v", r)
	}
	if r := rows[1]; r.MailName != "IcyKnoll" || r.BindingID != "b1" || !strings.Contains(r.Text, "✉ 2") ||
		!strings.Contains(r.Text, "(1 ack)") {
		t.Errorf("bound row = %+v", r)
	}
	if r := rows[2]; !strings.Contains(r.Text, "at limit") || !strings.HasPrefix(r.Text, "-    ") {
		t.Errorf("limit row without slot = %q", r.Text)
	}
	if r := rows[3]; r.BindingID != "b4" || !strings.Contains(r.Text, "absent") || !strings.Contains(r.Text, "GoneOwl") {
		t.Errorf("absent row = %+v", r)
	}
}

func TestBoardLineRoundTrip(t *testing.T) {
	for _, r := range BoardRows(boardModel()) {
		got, ok := ParseBoardLine(r.Line())
		if !ok || got != r {
			t.Errorf("round trip of %+v = %+v, %v", r, got, ok)
		}
	}
	for _, bad := range []string{"", "p1\tb1", "\t-\t-\ttext"} {
		if _, ok := ParseBoardLine(bad); ok {
			t.Errorf("ParseBoardLine(%q) accepted", bad)
		}
	}
}

func TestBoardHeader(t *testing.T) {
	m := boardModel()
	h := BoardHeader(Context{WorkspaceLabel: "acfs", PaneID: "p9"}, m)
	if !strings.HasPrefix(h, "workspace acfs · pane p9\n") || strings.Contains(h, "not fresh") {
		t.Errorf("header = %q", h)
	}
	for _, k := range BoardKeys {
		if !strings.Contains(h, k.Key+" "+k.What) {
			t.Errorf("header lacks %s", k.Key)
		}
	}
	m.Sources = map[string]snapshot.SourceHealth{"mail": {Status: "unavailable"}, "herdr": {Status: snapshot.Fresh}}
	if h := BoardHeader(Context{}, m); !strings.Contains(h, "not fresh: mail") || strings.Contains(h, "herdr") {
		t.Errorf("degraded header = %q", h)
	}
}

func TestMenuItems(t *testing.T) {
	scoped := MenuItems(Context{WorkspaceID: "w1"})
	free := MenuItems(Context{})
	if len(scoped) != len(free) {
		t.Fatal("the menu changes length with the workspace")
	}
	byVerb := func(items []MenuItem, verb string) MenuItem {
		for _, it := range items {
			if it.Args[0] == verb {
				return it
			}
		}
		t.Fatalf("no %s item", verb)
		return MenuItem{}
	}
	if got := byVerb(scoped, "limits").Args; !reflect.DeepEqual(got, []string{"limits", "w1", "--dry-run"}) {
		t.Errorf("scoped limits = %v", got)
	}
	if got := byVerb(free, "limits").Args; !reflect.DeepEqual(got, []string{"limits", "--dry-run"}) {
		t.Errorf("unscoped limits = %v", got)
	}
	if got := byVerb(scoped, "status").Args; !reflect.DeepEqual(got, []string{"status", "w1"}) {
		t.Errorf("scoped status = %v", got)
	}
	for _, it := range scoped {
		if it.Popup != (it.Args[0] == "ui") {
			t.Errorf("%v: Popup = %v", it.Args, it.Popup)
		}
		if it.Popup && !strings.HasSuffix(it.Args[1], "-popup") {
			t.Errorf("%v is not a popup body", it.Args)
		}
	}
}
