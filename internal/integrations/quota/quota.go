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
// Each record is of a credential (credential.go): record-claude writes the
// active one into its record, and a Codex log's session_meta names the
// account that made the session. A caam switch or a new login invalidates
// the pool's records of the old credential (nhm-kyt), so the pool is
// unknown until a record of the new one comes.
//
// Only percentages, reset times, plan types and credentials' names are
// read and kept, never a token or a session's content. An account id is
// read only to make its fingerprint.
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
	"os/exec"
	"path/filepath"
	"slices"
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
	// Credential is the pool's active credential, the one its records
	// must be of; nil when nhm cannot name it.
	Credential *Credential `json:"credential,omitempty"`
	// Superseded: the pool has records, all of another credential, so
	// it is unknown.
	Superseded bool `json:"superseded,omitempty"`
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
	// CodexHome holds Codex's session logs and its auth.json.
	CodexHome string
	// ClaudeDir holds Claude's .credentials.json, and ClaudeState is
	// Claude's .claude.json, whose oauthAccount names the account.
	ClaudeDir   string
	ClaudeState string
	// Caam is the caam binary, "" without caam.
	Caam string
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
	// As Claude Code and caam: CLAUDE_CONFIG_DIR holds both files.
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		e.ClaudeDir, e.ClaudeState = dir, filepath.Join(dir, ".claude.json")
	} else if home != "" {
		e.ClaudeDir, e.ClaudeState = filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")
	}
	// Never a caam in the working directory (exec.ErrDot).
	if bin, err := exec.LookPath("caam"); err == nil {
		e.Caam = bin
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
	profiles := caamProfiles(env.Caam, "", caamTimeout)
	claudeCred, codexCred := credentialOf(env, "claude", profiles), credentialOf(env, "codex", profiles)
	claude := ReadClaude(now, claudeCred, env.RecordFile, acfsRecord(env.AcfsHome))
	codex, err := ReadCodex(env.CodexHome, codexCred, now)
	if err != nil || codex == nil {
		note := "no Codex session log of the last 7 days has a rate_limits record"
		if err != nil {
			note = "Codex session logs: " + err.Error()
		}
		codex = &Observation{Pool: "codex", Kind: "codex", Credential: codexCred, Note: note}
	}
	obs := []Observation{*claude, *codex,
		{Pool: "agy", Kind: "agy", Note: "agy exposes no usage"},
		{Pool: "pi", Kind: "pi", Note: "nhm does not read pi's usage: pi spends the quota of the provider it runs on"},
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

// claudeRecord is the record-claude file: {"recorded_at": RFC 3339,
// "credential": {...}, "rate_limits": {"five_hour": {...}, "seven_day": {...}}}.
// ACFS's record and nhm's before nhm-kyt have no credential.
type claudeRecord struct {
	RecordedAt string      `json:"recorded_at"`
	Credential *Credential `json:"credential"`
	RateLimits *struct {
		FiveHour *claudeWindow `json:"five_hour"`
		SevenDay *claudeWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

type claudeWindow struct {
	UsedPercentage *float64        `json:"used_percentage"`
	ResetsAt       json.RawMessage `json:"resets_at"`
}

// ReadClaude reads the newest of the Claude record files that is of the
// active credential (ofActive). A pool without a readable record is
// unknown, with a note, as is one whose records are all of another
// credential.
func ReadClaude(now time.Time, active *Credential, files ...string) *Observation {
	type record struct {
		at   time.Time
		file string
		rec  claudeRecord
	}
	var recs []record
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
		recs = append(recs, record{at, f, rec})
	}
	// Newest first; of two at one time, the first file.
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].at.After(recs[j].at) })
	creds := make([]*Credential, len(recs))
	for i, r := range recs {
		creds[i] = r.rec.Credential
	}
	kept, other := ofActive(active, creds)
	for i, r := range recs {
		if !kept[i] {
			continue
		}
		return &Observation{Pool: "claude", Kind: "claude", ObservedAt: &r.at, Source: "claude statusLine (" + r.file + ")", Credential: active,
			FiveHour: claudeWin(r.rec.RateLimits.FiveHour, now), Weekly: claudeWin(r.rec.RateLimits.SevenDay, now)}
	}
	if other != nil {
		return superseded("claude", active, other)
	}
	return &Observation{Pool: "claude", Kind: "claude", Credential: active,
		Note: "no record: set `nhm quota record-claude` as Claude Code's statusLine command"}
}

// superseded is a pool whose records are all of another credential than
// the active one: a caam switch or a new login made them another
// account's (plan-v8 2.9).
func superseded(kind string, active, other *Credential) *Observation {
	// The note names the field match compared.
	was, now := "fingerprint "+other.Fingerprint, "fingerprint "+active.Fingerprint
	if other.Profile != "" && active.Profile != "" {
		was, now = "profile "+other.Profile, "profile "+active.Profile
	}
	return &Observation{Pool: kind, Kind: kind, Credential: active, Superseded: true,
		Note: fmt.Sprintf("the newest record is of %s, not of the active %s: a credential switch invalidates it", was, now)}
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

// codexMeta is the part of a session log's session_meta line that names
// the account that made the session.
type codexMeta struct {
	Type    string `json:"type"`
	Payload struct {
		CreatorAccountID string `json:"creator_account_id"`
	} `json:"payload"`
}

// ReadCodex reads the newest rate_limits record of the session logs
// written in the last seven days that is of the active credential
// (ofActive): a record is of the account its log's session_meta names. It
// returns nil when there is no record.
func ReadCodex(codexHome string, active *Credential, now time.Time) (*Observation, error) {
	if codexHome == "" {
		return nil, nil
	}
	root := filepath.Join(codexHome, "sessions")
	type rec struct {
		at   time.Time
		ev   codexEvent
		cred *Credential
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
		var cred *Credential
		for sc.Scan() {
			line := sc.Bytes()
			if bytes.Contains(line, []byte(`"session_meta"`)) {
				var meta codexMeta
				if json.Unmarshal(line, &meta) == nil && meta.Type == "session_meta" {
					cred = nil
					if fp := accountFingerprint("codex", meta.Payload.CreatorAccountID); fp != "" {
						cred = &Credential{Fingerprint: fp}
					}
					continue
				}
			}
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
			all = append(all, rec{at, ev, cred})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, nil
	}
	// Newest first; of two at one time, the later in the walk.
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	slices.Reverse(all)
	creds := make([]*Credential, len(all))
	for i, r := range all {
		creds[i] = r.cred
	}
	kept, other := ofActive(active, creds)
	var mine []rec
	for i, r := range all {
		if kept[i] {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return superseded("codex", active, other), nil
	}
	newest := mine[0]
	w := newest
	for _, r := range mine {
		if rl := r.ev.Payload.RateLimits; rl.Primary != nil || rl.Secondary != nil {
			w = r
			break
		}
	}
	rl := w.ev.Payload.RateLimits
	at := w.at
	o := &Observation{Pool: "codex", Kind: "codex", PlanType: rl.PlanType, ObservedAt: &at, Source: "codex session logs", Credential: active,
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
// Claude's statusLine input to env.RecordFile, with Claude's active
// credential, and returns the status line to print. It never fails the
// status line: a bad input saves nothing and still returns what it can.
func RecordClaude(input []byte, env Env, now time.Time) (line string, err error) {
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
		if env.RecordFile == "" {
			return strings.Join(parts, " · "), nil
		}
		if c := ActiveCredential(env, "claude"); c != nil {
			rec["credential"] = c
		}
		data, merr := json.Marshal(rec)
		if merr == nil {
			err = writeRecord(env.RecordFile, data)
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
