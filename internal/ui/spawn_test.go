package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/integrations/kinds"
)

// script answers the wizard's questions in order.
type script struct {
	answers [][]string
	asked   []string
}

func (s *script) next(q string) []string {
	s.asked = append(s.asked, q)
	if len(s.answers) == 0 {
		return nil
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a
}

func (s *script) Choose(h string, _ []string, _ bool) ([]string, error) { return s.next(h), nil }
func (s *script) Input(h, _, _ string) (string, error)                  { return strings.Join(s.next(h), ""), nil }
func (s *script) Write(h, _ string) (string, error)                     { return strings.Join(s.next(h), ""), nil }
func (s *script) Confirm(q string) (bool, error)                        { return strings.Join(s.next(q), "") == "y", nil }

func TestSpawnWizard(t *testing.T) {
	ks := kinds.NewRegistry().All()
	s := &script{answers: [][]string{
		{"claude", "agy"}, // kinds
		{"2"}, {"opus"},   // claude: count, model
		{"1"},           // agy: count (no model flag)
		{"swarm"}, {""}, // label, group
		{kickoffOwn}, {"Do X."},
		{memoryBoth},
	}}
	a, err := SpawnWizard(s, "workspace w1", ks)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Agents) != 2 || a.Agents[0] != (SpawnAgent{Kind: "claude", Count: 2, Model: "opus"}) || a.Agents[1] != (SpawnAgent{Kind: "agy", Count: 1}) ||
		a.Label != "swarm" || a.Prompt != "Do X." || !a.Memory || !a.CASS {
		t.Fatalf("answers = %+v", a)
	}
	got := strings.Join(SpawnArgs("w1", true, a), " ")
	if got != "nhm add w1 --agent claude=2:opus --agent agy=1 --label swarm --prompt Do X. --with-cass" {
		t.Fatalf("args = %s", got)
	}
	// No kickoff skips the memory question.
	s = &script{answers: [][]string{{"codex"}, {"1"}, {""}, {""}, {""}, {kickoffNone}}}
	if a, err = SpawnWizard(s, "h", ks); err != nil || !a.NoKickoff || len(s.answers) != 0 {
		t.Fatalf("no kickoff = %+v %v", a, err)
	}
	if got := strings.Join(SpawnArgs("s", false, a), " "); got != "nhm spawn s --agent codex=1 --no-kickoff" {
		t.Fatalf("args = %s", got)
	}
	// Nothing chosen, a bad count, an empty own prompt.
	for _, answers := range [][][]string{
		{{}},
		{{"claude"}, {"x"}},
		{{"claude"}, {"1"}, {""}, {""}, {""}, {kickoffOwn}, {"  "}},
	} {
		if _, err := SpawnWizard(&script{answers: answers}, "h", ks); err == nil {
			t.Errorf("%v was accepted", answers)
		}
	}
	if _, err := SpawnWizard(&script{answers: [][]string{{}}}, "h", ks); !errors.Is(err, ErrCancelled) {
		t.Fatalf("empty choice = %v", err)
	}
}
