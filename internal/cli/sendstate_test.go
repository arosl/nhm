package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

func TestParseStatesBetweenIsIdleOrDone(t *testing.T) {
	cases := []struct {
		in   []string
		want []herdr.AgentStatus
	}{
		{nil, nil},
		{[]string{"between"}, []herdr.AgentStatus{herdr.StatusIdle, herdr.StatusDone}},
		{[]string{"done", "between", "idle"}, []herdr.AgentStatus{herdr.StatusDone, herdr.StatusIdle}},
		{[]string{"working", "blocked", "working"}, []herdr.AgentStatus{herdr.StatusWorking, herdr.StatusBlocked}},
	}
	for _, c := range cases {
		got, err := parseStates(c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseStates(%v) = %v %v, want %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"ready", "Between", "unknown", ""} {
		if _, err := parseStates([]string{bad}); err == nil || !strings.Contains(err.Error(), "between (idle or done)") {
			t.Errorf("parseStates(%q) = %v", bad, err)
		}
	}
}

// nhm send --state between selects the agents between turns.
func TestSendRequestTakesStateBetween(t *testing.T) {
	req, err := (sendFlags{when: "ready", states: listFlag{"between", "working"}}).request("x")
	if err != nil {
		t.Fatal(err)
	}
	want := []herdr.AgentStatus{herdr.StatusIdle, herdr.StatusDone, herdr.StatusWorking}
	if !reflect.DeepEqual(req.Selector.States, want) {
		t.Fatalf("states = %v, want %v", req.Selector.States, want)
	}
	if _, err := (sendFlags{when: "ready", states: listFlag{"sleepy"}}).request("x"); err == nil {
		t.Fatal("an unknown state was accepted")
	}
	if !strings.Contains(sendUsage, "--state idle|done|working|blocked|between") || !strings.Contains(sendUsage, "--state between is idle or done") {
		t.Fatal("send's usage does not name --state between")
	}
}
