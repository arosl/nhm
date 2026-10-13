package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/integrations/cass"
	"github.com/arosl/nhm/internal/integrations/cm"
)

// loadEnrichConfig reads ~/.config/nhm/config.toml for the enrichment.
func loadEnrichConfig() (enrich.Config, error) {
	path, err := enrich.ConfigPath()
	if err != nil {
		return enrich.Config{}, err
	}
	return enrich.LoadConfig(path)
}

// cmRunner and cassRunner make the runners of the cm and cass binaries;
// tests replace them.
var (
	cmRunner   = cm.Exec
	cassRunner = cass.Exec
)

// enrichClients are the cm and cass clients and the budgets of a config.
func enrichClients(cfg enrich.Config) (enrich.CM, enrich.CASS, enrich.Budgets) {
	return cm.Client{Run: cmRunner(cfg.CMBinary)}, cass.Client{Run: cassRunner(cfg.CASS.Binary)}, cfg.Budgets()
}

// enrichment resolves the send flags. Conflicting flags refuse; a flag
// that is not given leaves the template and the config to decide.
func (f sendFlags) enrichment(cfg enrich.Config) (send.Enrichment, error) {
	e := send.Enrichment{DefaultCM: cfg.Memory.CM, DefaultCASS: cfg.CASS.Context, Query: f.memoryQuery,
		CheckDays: cfg.CASS.CheckDays, Check: cfg.CheckOn()}
	switch {
	case f.withCASS && f.noCASS:
		return e, errors.New("give --with-cass or --no-cass, not both")
	case f.cassCheck && f.noCassCheck:
		return e, errors.New("give --cass-check or --no-cass-check, not both")
	case f.cassCheck && f.noCASS:
		return e, errors.New("--no-cass turns the duplicate check off; drop --cass-check")
	}
	if f.memorySet {
		e.CM = enrich.Off
		if f.withMemory {
			e.CM = enrich.BestEffort
		}
	}
	switch {
	case f.withCASS:
		e.CASS = enrich.BestEffort
	case f.noCASS:
		e.CASS = enrich.Off
	}
	switch {
	case f.cassCheck:
		e.Check = true
	case f.noCassCheck, f.noCASS:
		e.Check = false
	}
	return e, nil
}

const memoryUsage = `Usage: nhm memory <query> [--project DIR] [--cm MODE] [--cass MODE] [--json]

Show the block that send --with-memory --with-cass would put before a
prompt for this query: CM's rules and CASS's hits, each with its id,
version and hash, and the items left out with the reason. CM ranks the
query as it is; CASS gets its keywords, any of them (a query with no
keywords skips CASS). Nothing is sent and nothing is written. A MODE is
off, best-effort or required
(default best-effort for both). The block of a real send also leaves
out what the agent's current context already got.
`

// memoryResult is nhm memory's JSON result.
type memoryResult struct {
	Query string        `json:"query"`
	CM    enrich.Report `json:"cm"`
	CASS  enrich.Report `json:"cass"`
	Items []enrich.Item `json:"items"`
	Block string        `json:"block"`
}

func runMemory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, memoryUsage) }
	project := fs.String("project", "", "the project (default: the cwd's git work tree)")
	cmMode := fs.String("cm", string(enrich.BestEffort), "off, best-effort or required")
	cassMode := fs.String("cass", string(enrich.BestEffort), "off, best-effort or required")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	env := envelope{Command: "memory"}
	refuse := func(err error) int { return output(stdout, stderr, *asJSON, env, err, exitRefused, nil) }
	query := strings.TrimSpace(strings.Join(pos, " "))
	if query == "" {
		return refuse(errors.New("give a query"))
	}
	cmM, err := enrich.ParseMode(*cmMode)
	if err != nil {
		return refuse(fmt.Errorf("--cm: %w", err))
	}
	cassM, err := enrich.ParseMode(*cassMode)
	if err != nil {
		return refuse(fmt.Errorf("--cass: %w", err))
	}
	cfg, err := loadEnrichConfig()
	if err != nil {
		return refuse(err)
	}
	ctx := context.Background()
	root := *project
	if s, err := projectScope(ctx, "", *project); err == nil {
		root = s.ProjectRoot
	} else if *project != "" {
		return refuse(err)
	}
	cmc, cs, budgets := enrichClients(cfg)
	r := enrich.NewRetriever(cmc, cs, budgets, time.Now())
	q := enrich.QueryOf(query)
	retr := r.Retrieve(ctx, enrich.Request{Query: q, Workspace: root, CM: cmM, CASS: cassM})
	plan := enrich.PlanFor(retr, nil, budgets.BlockBytes)
	res := memoryResult{Query: q, CM: retr.CM, CASS: retr.CASS, Items: append(append([]enrich.Item{}, plan.CM...), plan.CASS...),
		Block: plan.Block}
	env.Result = res
	for _, rep := range []enrich.Report{retr.CM, retr.CASS} {
		if rep.Error != "" && rep.Mode != enrich.Required {
			env.Warnings = append(env.Warnings, "best-effort enrichment failed: "+rep.Error)
		}
	}
	if err := retr.Err(); err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) { printMemory(w, res) })
}

func printMemory(w io.Writer, res memoryResult) {
	if res.Block == "" {
		fmt.Fprintln(w, "no block: nothing was found for this query")
	} else {
		fmt.Fprintln(w, res.Block)
	}
	for _, it := range res.Items {
		line := fmt.Sprintf("%s %s %s v%s sha256:%.12s %dB", it.Status, it.Source, it.ID, it.Version, it.SHA256, it.Bytes)
		if it.Reason != "" {
			line += " (" + strings.ReplaceAll(it.Reason, "_", " ") + ")"
		}
		fmt.Fprintln(w, line)
	}
}
