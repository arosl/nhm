package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/arosl/nhm/internal/integrations/quota"
)

const quotaUsage = `Usage: nhm quota [--socket PATH] [--json]
       nhm quota check <kind> [--limit PERCENT]
       nhm quota record-claude

quota prints each pool's five-hour and weekly windows, the reset times
(UTC), the observation's source and age, and the live agents per kind; on
an enrolled server it also stores them for status and attention.
check exits 1 when the kind's five-hour window is at least --limit full
(default 90) or the provider reports its limit reached; an unknown pool
passes. record-claude is Claude Code's statusLine command: it saves the
windows Claude hands it and prints a short status line; it never fails.
`

func runQuota(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "check":
			return runQuotaCheck(args[1:], stdout, stderr)
		case "record-claude":
			return runQuotaRecordClaude(stdout)
		}
	}
	fs := flag.NewFlagSet("quota", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, quotaUsage) }
	socket := fs.String("socket", "", "herdr API socket, to count live agents")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 0 {
		return exitRefused
	}
	ctx := context.Background()
	env := envelope{Command: "quota"}
	now := time.Now().UTC()
	// herdr counts the live agents; an enrolled server stores the result.
	// Neither is needed to read the pools.
	var counts map[string]int
	r, err := openReader(*socket, false)
	if err != nil {
		env.Warnings = append(env.Warnings, "live agents not counted: "+err.Error())
	} else {
		defer r.close()
		if agents, err := r.herdr.AgentList(ctx); err != nil {
			env.Warnings = append(env.Warnings, "live agents not counted: "+err.Error())
		} else {
			counts = map[string]int{}
			for _, a := range agents {
				if a.Agent != "" {
					counts[a.Agent]++
				}
			}
		}
	}
	obs := quota.Collect(quota.DefaultEnv(), now, counts)
	if r != nil && r.db != nil {
		if err := quota.Store(ctx, r.db.SQL(), obs); err != nil {
			env.Warnings = append(env.Warnings, err.Error())
		}
	}
	return printQuota(stdout, stderr, *asJSON, env, obs, now)
}

func printQuota(stdout, stderr io.Writer, asJSON bool, env envelope, obs []quota.Observation, now time.Time) int {
	env.Result = map[string]any{"generated_at": now, "pools": obs}
	return output(stdout, stderr, asJSON, env, nil, exitOK, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "POOL\tWINDOW\tUSED\tRESETS (UTC)\tAS OF (UTC)\tAGENTS")
		for _, o := range obs {
			pool := o.Pool
			if o.PlanType != "" {
				pool += " (" + o.PlanType + ")"
			}
			asOf := "-"
			if o.ObservedAt != nil {
				asOf = o.ObservedAt.UTC().Format(time.RFC3339) + " (" + age(now.Sub(*o.ObservedAt)) + " ago)"
			}
			rows := 0
			for _, win := range []struct {
				name string
				w    *quota.Window
			}{{"5h", o.FiveHour}, {"weekly", o.Weekly}} {
				if win.w == nil {
					continue
				}
				rows++
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", pool, win.name, usedCell(win.w), resetCell(win.w), asOf, o.Agents)
			}
			if rows == 0 {
				fmt.Fprintf(tw, "%s\t-\tunknown\t-\t-\t%d\n", pool, o.Agents)
			}
		}
		_ = tw.Flush()
		for _, o := range obs {
			if o.LimitReached != nil {
				stale := ""
				if o.LimitReached.Stale {
					stale = ", may have cleared since"
				}
				fmt.Fprintf(w, "  %s: limit reached: %s (as of %s%s)\n", o.Pool, o.LimitReached.Type, o.LimitReached.ObservedAt.Format(time.RFC3339), stale)
			}
			if o.Note != "" {
				fmt.Fprintf(w, "  %s: %s\n", o.Pool, o.Note)
			}
		}
	})
}

func usedCell(w *quota.Window) string {
	if w.UsedPct == nil {
		return "-"
	}
	pct := strconv.Itoa(int(math.Floor(*w.UsedPct))) + "%"
	if w.Reset {
		return "reset (was " + pct + ")"
	}
	return pct
}

func resetCell(w *quota.Window) string {
	if w.ResetsAt == nil {
		return "-"
	}
	return w.ResetsAt.UTC().Format(time.RFC3339)
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}

func runQuotaCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("quota check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, quotaUsage) }
	limit := fs.Int("limit", quota.DefaultLimitPct, "refuse at this percent of the 5-hour window")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 || *limit < 0 || *limit > 100 {
		fmt.Fprint(stderr, quotaUsage)
		return exitRefused
	}
	ok, why := quota.Check(quota.Collect(quota.DefaultEnv(), time.Now().UTC(), nil), pos[0], *limit)
	if !ok {
		fmt.Fprintln(stderr, why)
		return exitError
	}
	return exitOK
}

// runQuotaRecordClaude is the statusLine command. Claude shows whatever
// it prints, so it always exits 0 and prints at most one line.
func runQuotaRecordClaude(stdout io.Writer) int {
	input, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	line, _ := quota.RecordClaude(input, quota.DefaultEnv().RecordFile, time.Now())
	fmt.Fprintln(stdout, line)
	return exitOK
}
