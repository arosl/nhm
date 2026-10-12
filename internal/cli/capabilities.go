package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/arosl/nhm/compat"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/robot"
)

// capability is one form nhm answers or does not answer yet.
type capability struct {
	Form      string `json:"form"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// capabilities lists the forms of the inventory, with a reason for every
// form that is not implemented: never inferred from a failure.
func capabilities() []capability {
	var out []capability
	for _, r := range compat.Rows() {
		c := capability{Form: strings.Trim(r.Form, "`"), Available: r.Disposition == "implemented"}
		if !c.Available {
			c.Reason = r.Disposition + ": " + r.Note
		}
		out = append(out, c)
	}
	return out
}

const capabilitiesUsage = `Usage: nhm capabilities [--json]

What this nhm answers: the ntm commit it tracks, the herdr protocol, the
robot schema ids, and every ntm form of the inventory with whether it is
available, and why not.
`

func runCapabilities(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capabilities", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, capabilitiesUsage) }
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 0 {
		return exitRefused
	}
	caps := capabilities()
	res := map[string]any{"version": Version, "commit": Commit, "ntm_commit": NTMCommit, "herdr_protocol": herdr.Protocol,
		"schema_ids": []string{robot.StatusSchemaID, robot.SnapshotSchemaID}, "forms": caps}
	return output(stdout, stderr, *asJSON, envelope{Command: "capabilities", Result: res}, nil, exitOK, func(w io.Writer) {
		fmt.Fprintf(w, "nhm %s, herdr protocol %d, ntm %.8s\n", Version, herdr.Protocol, NTMCommit)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, c := range caps {
			state := "yes"
			if !c.Available {
				state = "no: " + c.Reason
			}
			fmt.Fprintf(tw, "  %s\t%s\n", c.Form, state)
		}
		_ = tw.Flush()
	})
}

const compatibilityUsage = `Usage: nhm compatibility status [--json]

Compare this nhm with the ntm commit it tracks: how many forms of the
inventory are implemented, deferred to a bead, or waived by a ruling.
`

func runCompatibility(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "status" {
		fmt.Fprint(stderr, compatibilityUsage)
		return exitRefused
	}
	fs := flag.NewFlagSet("compatibility status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, compatibilityUsage) }
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil || len(pos) > 0 {
		return exitRefused
	}
	rows := compat.Rows()
	counts := compat.Counts(rows)
	res := map[string]any{"ntm_commit": NTMCommit, "nhm_version": Version, "counts": counts, "forms": rows}
	return output(stdout, stderr, *asJSON, envelope{Command: "compatibility status", Result: res}, nil, exitOK, func(w io.Writer) {
		fmt.Fprintf(w, "ntm %s: %d implemented, %d deferred, %d waived\n", NTMCommit, counts["implemented"], counts["deferred"], counts["waived"])
		for _, r := range rows {
			if r.Disposition != "implemented" {
				fmt.Fprintf(w, "  %-9s %s: %s\n", r.Disposition, strings.Trim(r.Form, "`"), r.Note)
			}
		}
	})
}
