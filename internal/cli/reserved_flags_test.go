package cli

import (
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/spf13/pflag"

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
	// Each verb's flag names; spawn and add are on the shared parser.
	names := func(verb string) []string {
		var out []string
		if verb == "spawn" {
			fs := pflag.NewFlagSet(verb, pflag.ContinueOnError)
			addSpawnFlags(fs)
			fs.VisitAll(func(f *pflag.Flag) { out = append(out, f.Name) })
			return out
		}
		fs := flag.NewFlagSet(verb, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		if verb == "send" {
			addSendFlags(fs)
		} else {
			addRobotSpawnFlags(fs)
		}
		fs.VisitAll(func(f *flag.Flag) { out = append(out, f.Name) })
		return out
	}
	for _, verb := range []string{"send", "spawn", "robot-spawn"} {
		for _, flagName := range names(verb) {
			name := flagName
			if verb == "robot-spawn" {
				name = strings.TrimPrefix(name, "spawn-")
			}
			if isKindFlag[name] {
				continue
			}
			if !kinds.Reserved(name) {
				t.Errorf("%s flag --%s is not reserved: a kind from config could take it", verb, flagName)
			}
		}
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
		func(fs *flag.FlagSet) { addRobotSpawnFlags(fs) },
	} {
		fs := flag.NewFlagSet("x", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		add(fs)
	}
	addSpawnFlags(pflag.NewFlagSet("spawn", pflag.ContinueOnError))
}
