// Package cli parses nhm's command line and renders its output. It holds
// no behaviour of its own; later verbs call internal/core.
package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/arosl/nhm/internal/herdr/schema"
)

// Build facts, set at link time by the release build:
//
//	-ldflags "-X github.com/arosl/nhm/internal/cli.Version=0.1.0 ..."
var (
	Version = "dev"
	Commit  = "unknown"
)

// NTMCommit is the ntm commit whose grammar and robot types nhm tracks.
const NTMCommit = "9d5ed97796e37fa7b7c89bc3ba4087c144f0102c"

const usage = `nhm: the herdr wrapper of the ACFS flywheel (what ntm is to tmux).

Usage:
  nhm version [--json]   print the version
  nhm help               print this text

The other verbs come with the next releases.
`

// Run executes nhm with args (without the program name) and returns the
// exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "--version":
		return runVersion(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "nhm: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// VersionInfo is the JSON form of `nhm version --json`.
type VersionInfo struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	NTMCommit     string `json:"ntm_commit"`
	HerdrProtocol int    `json:"herdr_protocol"`
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		default:
			fmt.Fprintf(stderr, "nhm version: unknown flag %q\n", a)
			return 2
		}
	}
	s, err := schema.Load()
	if err != nil {
		fmt.Fprintf(stderr, "nhm version: %v\n", err)
		return 1
	}
	info := VersionInfo{Version: Version, Commit: Commit, NTMCommit: NTMCommit, HerdrProtocol: s.Protocol}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			fmt.Fprintf(stderr, "nhm version: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "nhm %s (commit %s, herdr protocol %d, ntm %.8s)\n", info.Version, info.Commit, info.HerdrProtocol, info.NTMCommit)
	return 0
}
