// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/root.go (newVersionCmd's flags, runVersion's text,
// buildVersionResponse, goVersion, goPlatform) and internal/output/schema.go
// (VersionResponse, TimestampedResponse); see LICENSE in this directory and
// PROVENANCE.md. Local changes: the headline names nhm; after ntm's five
// lines nhm adds two, "herdr:" (the protocol it speaks) and "ntm:" (the
// commit it tracks), and the JSON carries them under "_nhm", as nhm's robot
// forms do; the response is built here, not through ntm's kernel.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/herdr/schema"
)

// VersionResponse is ntm's output.VersionResponse, the JSON of
// `nhm version --json`.
type VersionResponse struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Version     string        `json:"version"`
	Commit      string        `json:"commit,omitempty"`
	BuiltAt     string        `json:"built_at,omitempty"`
	BuiltBy     string        `json:"built_by,omitempty"`
	GoVersion   string        `json:"go_version"`
	Platform    string        `json:"platform"`
	NHM         VersionExtras `json:"_nhm"`
}

// VersionExtras are nhm's facts beside ntm's: the ntm commit whose grammar
// and robot types nhm tracks, and the herdr protocol it speaks.
type VersionExtras struct {
	NTMCommit     string `json:"ntm_commit"`
	HerdrProtocol int    `json:"herdr_protocol"`
}

// newVersionCmd is ntm's version verb on the shared parser: --short
// (-s), and the root's --json. Like ntm's, it takes no argument check.
func newVersionCmd() *cobra.Command {
	var short bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			return printVersion(cmd.OutOrStdout(), wantJSON(cmd), short)
		},
	}
	cmd.Flags().BoolVarP(&short, "short", "s", false, "print the version alone")
	return cmd
}

func buildVersionResponse() (VersionResponse, error) {
	s, err := schema.Load()
	if err != nil {
		return VersionResponse{}, err
	}
	return VersionResponse{
		GeneratedAt: time.Now().UTC(),
		Version:     Version,
		Commit:      Commit,
		BuiltAt:     Date,
		BuiltBy:     BuiltBy,
		GoVersion:   runtime.Version(),
		Platform:    fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		NHM:         VersionExtras{NTMCommit: NTMCommit, HerdrProtocol: s.Protocol},
	}, nil
}

// printVersion is ntm's runVersion: the JSON with --json (it wins over
// --short), the version alone with --short, else the headline and the
// details. ACFS's export-config.sh reads field 3 of line 1.
func printVersion(stdout io.Writer, asJSON, short bool) error {
	resp, err := buildVersionResponse()
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	if short {
		fmt.Fprintln(stdout, resp.Version)
		return nil
	}
	fmt.Fprintf(stdout, "nhm version %s\n", resp.Version)
	fmt.Fprintf(stdout, "  commit:    %s\n", resp.Commit)
	fmt.Fprintf(stdout, "  built:     %s\n", resp.BuiltAt)
	fmt.Fprintf(stdout, "  builder:   %s\n", resp.BuiltBy)
	fmt.Fprintf(stdout, "  go:        %s\n", resp.GoVersion)
	fmt.Fprintf(stdout, "  platform:  %s\n", resp.Platform)
	fmt.Fprintf(stdout, "  herdr:     protocol %d\n", resp.NHM.HerdrProtocol)
	fmt.Fprintf(stdout, "  ntm:       %.8s\n", resp.NHM.NTMCommit)
	return nil
}
