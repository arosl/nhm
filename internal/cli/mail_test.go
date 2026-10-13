package cli

import (
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
)

// The inbox's agent: the flag, then the variables, then the pane's
// binding; spawn's AGENT_MAIL_AGENT does not count once the pane is bound
// to another agent (nhm-4c4).
func TestInboxAgent(t *testing.T) {
	icy := &bindings.Binding{MailName: "IcyKnoll"}
	blue := &bindings.Binding{MailName: "BlueLake"}
	spawned := map[string]string{"AGENT_MAIL_AGENT": "IcyKnoll", "NHM_AGENT": "IcyKnoll"}
	for _, c := range []struct {
		name string
		flag string
		self *bindings.Binding
		env  map[string]string
		want string
	}{
		{"flag first", "RedStone", icy, spawned, "RedStone"},
		{"spawned pane", "", icy, spawned, "IcyKnoll"},
		{"spawned pane, case", "", &bindings.Binding{MailName: "icyknoll"}, spawned, "IcyKnoll"},
		{"pane bound again", "", blue, spawned, "BlueLake"},
		{"pane bound again, AGENT_NAME set", "", blue,
			map[string]string{"AGENT_MAIL_AGENT": "IcyKnoll", "NHM_AGENT": "IcyKnoll", "AGENT_NAME": "GreenPike"}, "GreenPike"},
		{"set by hand", "", blue, map[string]string{"AGENT_MAIL_AGENT": "IcyKnoll"}, "IcyKnoll"},
		{"no pane", "", nil, spawned, "IcyKnoll"},
		{"binding only", "", blue, nil, "BlueLake"},
		{"nothing", "", nil, nil, ""},
	} {
		got := inboxAgent(c.flag, c.self, func(k string) string { return c.env[k] })
		if got != c.want {
			t.Errorf("%s: agent = %q, want %q", c.name, got, c.want)
		}
	}
}
