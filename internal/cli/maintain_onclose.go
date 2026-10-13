package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/herdr"
)

// maintainConfig is [maintain] in nhm's config.
type maintainConfig struct {
	// RecycleOnClose turns on the recycle on close (nhm-abn).
	RecycleOnClose bool
	// RecycleTemplate is the successor's kickoff, a template or palette
	// key; "" is ACFS's default_new_agent.
	RecycleTemplate string
}

// loadMaintainConfig reads [maintain] from nhm's config:
//
//	[maintain]
//	recycle_on_close = true           # default false
//	recycle_template = "KEY"          # default: ACFS's default_new_agent
//
// A missing file or section is the defaults. An unknown key, or a value
// of another type, fails, so that a typo does not leave it off unseen.
func loadMaintainConfig() (maintainConfig, error) {
	var c maintainConfig
	path, err := enrich.ConfigPath()
	if err != nil {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	var f struct {
		Maintain map[string]any `toml:"maintain"`
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	for key, v := range f.Maintain {
		switch key {
		case "recycle_on_close":
			on, ok := v.(bool)
			if !ok {
				return c, fmt.Errorf("%s: maintain.recycle_on_close is true or false", path)
			}
			c.RecycleOnClose = on
		case "recycle_template":
			s, ok := v.(string)
			if !ok {
				return c, fmt.Errorf("%s: maintain.recycle_template is a template key", path)
			}
			c.RecycleTemplate = strings.TrimSpace(s)
		default:
			return c, fmt.Errorf("%s: [maintain] takes recycle_on_close and recycle_template, not %s", path, key)
		}
	}
	return c, nil
}

// onCloseFile is where the recycle on close keeps what it saw, per
// binding, in the instance directory. nhm maintain's singleton lock makes
// it the only writer.
const onCloseFile = "recycle_on_close.json"

// onCloseWait is how long a recycle on close waits for the turn to end:
// the agent was between turns on two checks a minute apart.
const onCloseWait = time.Minute

// recycleOnClose is one pass of the recycle on close (nhm-abn): for each
// active binding, lifecycle.OnCloseDue decides, and a due recycle starts
// as the UI runner's recycle job, as nhm recycle --self does. It returns
// what it started and its warnings. A project with no bead database is
// not watched.
func recycleOnClose(ctx context.Context, ie *instanceEnv, cfg maintainConfig, lenv lifecycle.Env, now time.Time) (started, warnings []string) {
	path := filepath.Join(ie.inst.Dir, onCloseFile)
	state, err := readOnClose(path)
	if err != nil {
		warnings = append(warnings, "recycle on close: "+err.Error()+"; it starts afresh")
		state = map[string]lifecycle.OnCloseSeen{}
	}
	active, err := lenv.Bindings.List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return nil, append(warnings, "recycle on close: "+err.Error())
	}
	agents, err := ie.herdr.AgentList(ctx)
	if err != nil {
		return nil, append(warnings, "recycle on close: herdr: "+err.Error())
	}
	status := map[string]herdr.AgentStatus{}
	for _, a := range agents {
		status[a.PaneID] = a.AgentStatus
	}
	next := map[string]lifecycle.OnCloseSeen{}
	for _, b := range active {
		seen := state[b.ID]
		st, ok := status[b.PaneID]
		if b.PaneID == "" || !ok {
			// No agent in herdr now: keep what was seen, decide nothing.
			if seen.Generation != 0 {
				next[b.ID] = seen
			}
			continue
		}
		due, err := lifecycle.OnCloseDue(ctx, lenv.Facts, b, st, &seen, now)
		switch {
		case errors.Is(err, lifecycle.ErrNoBeadDB):
			continue
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("%s: recycle on close: %v", b.MailName, err))
		case due != nil:
			req := lifecycle.RecycleRequest{Target: b.ID, ExpectedGeneration: b.Generation, Template: cfg.RecycleTemplate,
				WaitTurn: onCloseWait}
			if _, err := lifecycle.CheckRecycle(ctx, lenv, req); err != nil {
				// Refused for this generation: said once, not tried again.
				seen.Recycled = "refused"
				warnings = append(warnings, fmt.Sprintf("%s: bead %s closed, but its recycle is refused: %v", b.MailName,
					strings.Join(due, ", "), err))
				break
			}
			id, err := startRecycleJob(recycleJobClient(ie, b), req)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: recycle on close did not start: %v", b.MailName, err))
				break
			}
			seen.Recycled = id
			started = append(started, fmt.Sprintf("%s: bead %s closed; recycle started as %s", b.MailName, strings.Join(due, ", "), id))
		}
		next[b.ID] = seen
	}
	if err := writeOnClose(path, next); err != nil {
		warnings = append(warnings, "recycle on close: "+err.Error())
	}
	return started, warnings
}

func readOnClose(path string) (map[string]lifecycle.OnCloseSeen, error) {
	state := map[string]lifecycle.OnCloseSeen{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return state, nil
}

// writeOnClose replaces the state file in one rename.
func writeOnClose(path string, state map[string]lifecycle.OnCloseSeen) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
