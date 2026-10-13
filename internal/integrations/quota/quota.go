// Package quota reads how full each provider plan's usage windows are
// (plan-v8 2.9). It models quota by pool, not by agent kind: a pool is a
// provider account or credential profile. Today each kind's default
// profile is its own pool ("claude", "codex"); agy and pi expose no usage,
// so their pools are unknown.
//
// The sources are ACFS's (scripts/lib/agent_quota.sh):
//
//   - Claude hands its windows only to its statusLine command; `nhm quota
//     record-claude` (and ACFS's `agent_quota.sh record-claude`) writes
//     them to a record file. The newest record of the two files counts.
//   - Codex writes them into every session log ($CODEX_HOME/sessions/**/
//     rollout-*.jsonl, token_count events: rate_limits.primary is the
//     five-hour window, .secondary the weekly one).
//
// Only percentages, reset times and plan types are read, never a token,
// an account id or a session's content.
package quota

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/store"
)

// DefaultLimitPct is the fill at which `quota check` refuses a kind.
const DefaultLimitPct = 90

// codexMaxAge: session logs older than the weekly window cannot hold its
// state.
const codexMaxAge = 7 * 24 * time.Hour

// limitStaleAfter: a limit-reached record older than one five-hour window
// may have cleared with nothing newer logged to say so.
const limitStaleAfter = 5 * time.Hour

// Window is one usage window.
type Window struct {
	UsedPct       *float64   `json:"used_percent"`
	WindowMinutes *int       `json:"window_minutes,omitempty"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
	// Reset is true when the reset time has passed: UsedPct is then the
	// fill as last seen, not the fill now.
	Reset bool `json:"reset"`
}

// Limit is a provider's own "limit reached" record.
type Limit struct {
	Type       string    `json:"type"`
	ObservedAt time.Time `json:"observed_at"`
	// Stale: the record may have cleared since (a window reset after it,
	// or it is older than five hours).
	Stale bool `json:"stale"`
}

// Observation is one pool's state.
type Observation struct {
	Pool         string     `json:"pool_id"`
	Kind         string     `json:"kind"`
	PlanType     string     `json:"plan_type,omitempty"`
	ObservedAt   *time.Time `json:"observed_at,omitempty"`
	FiveHour     *Window    `json:"five_hour,omitempty"`
	Weekly       *Window    `json:"weekly,omitempty"`
	LimitReached *Limit     `json:"limit_reached,omitempty"`
	Source       string     `json:"source,omitempty"`
	// Note says why the pool is unknown, when it is.
	Note string `json:"note,omitempty"`
	// Agents counts the live agents of the kind, when herdr was asked.
	Agents int `json:"agents"`
}

// Known reports whether the pool has any observation.
func (o Observation) Known() bool { return o.ObservedAt != nil }

// Env says where the sources are. Empty fields take the defaults of the
// environment.
type Env struct {
	// AcfsHome holds ACFS's record: <AcfsHome>/state/quota/claude.json.
	AcfsHome string
	// RecordFile is nhm's own Claude record.
	RecordFile string
	// CodexHome holds Codex's session logs.
	CodexHome string
}

// DefaultEnv fills an Env from the environment.
func DefaultEnv() Env {
	e := Env{AcfsHome: os.Getenv("ACFS_HOME"), CodexHome: os.Getenv("CODEX_HOME")}
	home, _ := os.UserHomeDir()
	if e.AcfsHome == "" && home != "" {
		e.AcfsHome = filepath.Join(home, ".acfs")
	}
	if e.CodexHome == "" && home != "" {
		e.CodexHome = filepath.Join(home, ".codex")
	}
	// nhm's state root ignores a relative XDG_STATE_HOME: the statusline
	// that writes the record and the verbs that read it run in different
	// directories (nhm-mwo).
	if root, err := store.StateRoot(); err == nil {
		e.RecordFile = filepath.Join(root, "quota", "claude.json")
	}
	return e
}

// Collect reads every pool: claude, codex, and the unknown agy and pi.
// counts are the live agents per kind (nil when herdr was not asked).
func Collect(env Env, now time.Time, counts map[string]int) []Observation {
	claude := ReadClaude(now, env.RecordFile, acfsRecord(env.AcfsHome))
	codex, err := ReadCodex(env.CodexHome, now)
	if err != nil || codex == nil {
		note := "no Codex session log of the last 7 days has a rate_limits record"
		if err != nil {
			note = "Codex session logs: " + err.Error()
		}
		codex = &Observation{Pool: "codex", Kind: "codex", Note: note}
	}
	obs := []Observation{*claude, *codex,
		{Pool: "agy", Kind: "agy", Note: "agy exposes no usage"},
		{Pool: "pi", Kind: "pi", Note: "pi exposes no usage"},
	}
	for i := range obs {
		obs[i].Agents = counts[obs[i].Kind]
	}
	return obs
}

func acfsRecord(acfsHome string) string {
	if acfsHome == "" {
		return ""
	}
	return filepath.Join(acfsHome, "state", "quota", "claude.json")
}

// claudeRecord is the record-claude file:
// {"recorded_at": RFC 3339, "rate_limits": {"five_hour": {...}, "seven_day": {...}}}.
type claudeRecord struct {
	RecordedAt string `json:"recorded_at"`
	RateLimits *struct {
		FiveHour *claudeWindow `json:"five_hour"`
		SevenDay *claudeWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

type claudeWindow struct {
	UsedPercentage *float64        `json:"used_percentage"`
	ResetsAt       json.RawMessage `json:"resets_at"`
}

// ReadClaude reads the newest of the Claude record files. A pool without
// a readable record is unknown, with a note.
func ReadClaude(now time.Time, files ...string) *Observation {
	var best *Observation
	for _, f := range files {
		if f == "" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rec claudeRecord
		if json.Unmarshal(data, &rec) != nil || rec.RateLimits == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, rec.RecordedAt)
		if err != nil {
			continue
		}
		o := &Observation{Pool: "claude", Kind: "claude", ObservedAt: &at, Source: "claude statusLine (" + f + ")",
			FiveHour: claudeWin(rec.RateLimits.FiveHour, now), Weekly: claudeWin(rec.RateLimits.SevenDay, now)}
		if best == nil || at.After(*best.ObservedAt) {
			best = o
		}
	}
	if best == nil {
		return &Observation{Pool: "claude", Kind: "claude",
			Note: "no record: set `nhm quota record-claude` as Claude Code's statusLine command"}
	}
	return best
}

func claudeWin(w *claudeWindow, now time.Time) *Window {
	if w == nil {
		return nil
	}
	out := &Window{UsedPct: w.UsedPercentage, ResetsAt: resetTime(w.ResetsAt)}
	out.mark(now)
	return out
}

// resetTime reads a reset time given as epoch seconds or RFC 3339; any
// other value is unknown.
func resetTime(raw json.RawMessage) *time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var sec float64
	if json.Unmarshal(raw, &sec) == nil {
		t := time.Unix(int64(sec), 0).UTC()
		return &t
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

func (w *Window) mark(now time.Time) {
	if w.ResetsAt != nil && !w.ResetsAt.After(now) {
		w.Reset = true
	}
}

// codexEvent is the part of a session log line that holds the windows.
type codexEvent struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string `json:"type"`
		RateLimits *struct {
			Primary           *codexWindow `json:"primary"`
			Secondary         *codexWindow `json:"secondary"`
			PlanType          string       `json:"plan_type"`
			LimitReachedType  *string      `json:"rate_limit_reached_type"`
			LimitReachedType2 *string      `json:"limit_reached_type"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

type codexWindow struct {
	UsedPercent   *float64        `json:"used_percent"`
	WindowMinutes *int            `json:"window_minutes"`
	ResetsAt      json.RawMessage `json:"resets_at"`
}

// ReadCodex reads the newest rate_limits record of the session logs
// written in the last seven days. It returns nil when there is none.
func ReadCodex(codexHome string, now time.Time) (*Observation, error) {
	if codexHome == "" {
		return nil, nil
	}
	root := filepath.Join(codexHome, "sessions")
	type rec struct {
		at time.Time
		ev codexEvent
	}
	var all []rec
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil || now.Sub(info.ModTime()) > codexMaxAge {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			// One unreadable log (rotated away, say) must not hide the others.
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if !bytes.Contains(line, []byte(`"rate_limits"`)) {
				continue
			}
			var ev codexEvent
			if json.Unmarshal(line, &ev) != nil || ev.Type != "event_msg" || ev.Payload.Type != "token_count" || ev.Payload.RateLimits == nil {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
			if err != nil {
				continue
			}
			all = append(all, rec{at, ev})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, nil
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	newest := all[len(all)-1]
	w := newest
	for i := len(all) - 1; i >= 0; i-- {
		if rl := all[i].ev.Payload.RateLimits; rl.Primary != nil || rl.Secondary != nil {
			w = all[i]
			break
		}
	}
	rl := w.ev.Payload.RateLimits
	at := w.at
	o := &Observation{Pool: "codex", Kind: "codex", PlanType: rl.PlanType, ObservedAt: &at, Source: "codex session logs",
		FiveHour: codexWin(rl.Primary, now), Weekly: codexWin(rl.Secondary, now)}
	nrl := newest.ev.Payload.RateLimits
	if t := firstNonNil(nrl.LimitReachedType, nrl.LimitReachedType2); t != nil {
		o.LimitReached = &Limit{Type: *t, ObservedAt: newest.at}
		o.LimitReached.Stale = now.Sub(newest.at) > limitStaleAfter
		for _, win := range []*Window{o.FiveHour, o.Weekly} {
			if win != nil && win.Reset && win.ResetsAt != nil && win.ResetsAt.After(newest.at) {
				o.LimitReached.Stale = true
			}
		}
	}
	return o, nil
}

func firstNonNil(values ...*string) *string {
	for _, v := range values {
		if v != nil && *v != "" {
			return v
		}
	}
	return nil
}

func codexWin(w *codexWindow, now time.Time) *Window {
	if w == nil {
		return nil
	}
	out := &Window{UsedPct: w.UsedPercent, WindowMinutes: w.WindowMinutes, ResetsAt: resetTime(w.ResetsAt)}
	out.mark(now)
	return out
}

// Check decides whether a kind may start: false at a fresh limit-reached
// record, or when the five-hour window is at least limitPct full and has
// not reset. A kind without a known pool passes; the caller decides about
// unknown pools (plan-v8 2.2).
func Check(obs []Observation, kind string, limitPct int) (ok bool, reason string) {
	for _, o := range obs {
		if o.Kind != kind {
			continue
		}
		if o.LimitReached != nil && !o.LimitReached.Stale {
			return false, fmt.Sprintf("%s: limit reached: %s (as of %s)", kind, o.LimitReached.Type, o.LimitReached.ObservedAt.Format(time.RFC3339))
		}
		w := o.FiveHour
		if w == nil || w.Reset || w.UsedPct == nil {
			return true, ""
		}
		if *w.UsedPct >= float64(limitPct) {
			resets := "-"
			if w.ResetsAt != nil {
				resets = w.ResetsAt.Format(time.RFC3339)
			}
			return false, fmt.Sprintf("%s: 5-hour window %d%% used (limit %d%%), resets %s", kind, int(math.Floor(*w.UsedPct)), limitPct, resets)
		}
		return true, ""
	}
	return true, ""
}

// RecordClaude is the statusLine command's work: it saves the windows of
// Claude's statusLine input to path, and returns the status line to print.
// It never fails the status line: a bad input saves nothing and still
// returns what it can.
func RecordClaude(input []byte, path string, now time.Time) (line string, err error) {
	var in struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
		RateLimits *struct {
			FiveHour json.RawMessage `json:"five_hour"`
			SevenDay json.RawMessage `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(input, &in) != nil {
		return "", nil
	}
	var parts []string
	if in.Model != nil && in.Model.DisplayName != "" {
		parts = append(parts, in.Model.DisplayName)
	}
	if in.RateLimits != nil {
		for _, p := range []struct {
			label string
			raw   json.RawMessage
		}{{"5h", in.RateLimits.FiveHour}, {"7d", in.RateLimits.SevenDay}} {
			var w claudeWindow
			if json.Unmarshal(p.raw, &w) == nil && w.UsedPercentage != nil {
				parts = append(parts, fmt.Sprintf("%s %d%%", p.label, int(math.Floor(*w.UsedPercentage))))
			}
		}
		rec := map[string]any{"recorded_at": now.UTC().Format(time.RFC3339),
			"rate_limits": map[string]json.RawMessage{"five_hour": orNull(in.RateLimits.FiveHour), "seven_day": orNull(in.RateLimits.SevenDay)}}
		data, merr := json.Marshal(rec)
		if merr == nil && path != "" {
			err = writeRecord(path, data)
		}
	}
	return strings.Join(parts, " · "), err
}

func orNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

func writeRecord(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".claude.json.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
