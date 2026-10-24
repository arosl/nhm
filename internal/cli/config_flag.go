package cli

import (
	"io"
	"path/filepath"
	"strings"
)

// configFirst sets NHM_CONFIG from the call's --config before Run builds
// the root that executes the call, and returns what restores it. spawn and
// add take a flag per kind, and the config's [kinds.*] tables add kinds,
// so the kind registry must read that config before those flags exist;
// rootOptions.apply sets NHM_CONFIG only after the parse.
//
// A first root parses the call as the real one does: the verb's flags say
// which words are values, so a "--config" that is another flag's value is
// not taken, and an unknown flag (maybe a kind of that config) is skipped.
// A legacy verb parses its own flags after apply, so it needs none of this.
func configFirst(args []string) (restore func()) {
	restore = func() {}
	if !hasConfigWord(args) {
		return restore
	}
	r := newRoot(io.Discard, io.Discard)
	cmd, rest, err := r.cmd.Find(args)
	if err != nil || cmd.DisableFlagParsing {
		return restore
	}
	cmd.FParseErrWhitelist.UnknownFlags = true
	if err := cmd.ParseFlags(rest); err != nil || r.opts.config == "" {
		return restore
	}
	path, err := filepath.Abs(r.opts.config)
	if err != nil {
		return restore // apply reports it
	}
	o := &rootOptions{}
	o.setenv("NHM_CONFIG", path)
	return o.restore[0]
}

// hasConfigWord reports a --config word before any "--".
func hasConfigWord(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--config" || strings.HasPrefix(a, "--config=") {
			return true
		}
	}
	return false
}
