package cli

import (
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/integrations/kinds"
)

// The kinds' names and flags become flags of send and spawn, and
// "spawn-<name>" flags of --robot-spawn. Every other flag of those verbs
// must be reserved in the kind registry, so that a kind from config can
// never take one; the reserved set cannot drift from the real flag sets.
func TestEveryVerbFlagIsReservedForKinds(t *testing.T) {
	isKindFlag := map[string]bool{}
	for _, a := range kinds.All() {
		for _, n := range append([]string{a.Name}, a.Flags...) {
			isKindFlag[n] = true
		}
	}
	sets := map[string]func(*flag.FlagSet){
		"send":        func(fs *flag.FlagSet) { addSendFlags(fs) },
		"spawn":       func(fs *flag.FlagSet) { addSpawnFlags(fs) },
		"robot-spawn": func(fs *flag.FlagSet) { addRobotSpawnFlags(fs) },
	}
	for verb, add := range sets {
		fs := flag.NewFlagSet(verb, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		add(fs)
		fs.VisitAll(func(f *flag.Flag) {
			name := f.Name
			if verb == "robot-spawn" {
				name = strings.TrimPrefix(name, "spawn-")
			}
			if isKindFlag[name] {
				return
			}
			if !kinds.Reserved(name) {
				t.Errorf("%s flag --%s is not reserved: a kind from config could take it", verb, f.Name)
			}
		})
	}
}

// A kind from config named after a verb flag is refused; it never
// panics a verb with "flag redefined".
func TestKindNamedAfterAFlagDoesNotPanic(t *testing.T) {
	r := kinds.NewRegistry()
	for _, name := range []string{"no-cass-context", "effort", "force", "json"} {
		if err := r.Register(kinds.Adapter{Name: name}); err == nil {
			t.Errorf("kind %q registered", name)
		}
	}
	for _, add := range []func(*flag.FlagSet){
		func(fs *flag.FlagSet) { addSendFlags(fs) },
		func(fs *flag.FlagSet) { addSpawnFlags(fs) },
		func(fs *flag.FlagSet) { addRobotSpawnFlags(fs) },
	} {
		fs := flag.NewFlagSet("x", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		add(fs)
	}
}
