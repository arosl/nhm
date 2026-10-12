package owner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/store"
)

// Handback gives the instance back to the Bash (plan-v8 section 1). Under
// the exclusive owner lock it builds the Bash's state from nhm's: the
// registration tokens of the bindings, each active agent's wake state and
// the keep list, and has `acfs agents import` write it. It refuses, and
// nhm stays the owner, when that state cannot carry everything: a nudge
// in flight, or a pending message whose delivery event Agent Mail no
// longer has (the Bash would never wake for it). The tokens go to the
// Bash in files of a private directory that is removed afterwards. A dry
// run builds and checks the state and writes nothing.
func Handback(ctx context.Context, env Env, dry bool) (Report, error) {
	rep := newReport(dry)
	if dry {
		o, err := core.ReadOwner(env.InstanceDir)
		if err != nil {
			return rep, err
		}
		rep.Owner = o
		if o.Owner != core.OwnerNhm {
			return rep, refused("the owner is %s, not nhm: nothing to hand back", o.Owner)
		}
		return rep, handback(ctx, env, &rep)
	}
	o, err := core.SwitchOwner(ctx, env.InstanceDir, core.OwnerBash, env.now(), func(core.Owner) error {
		return handback(ctx, env, &rep)
	})
	rep.Owner = o
	if err != nil && errors.Is(err, core.ErrAlreadyOwner) && !errors.Is(err, ErrRefused) {
		// The same answer as the dry run's: a refusal, not a failure.
		err = fmt.Errorf("%w: %w", ErrRefused, err)
	}
	return rep, err
}

func handback(ctx context.Context, env Env, rep *Report) error {
	keep, err := env.Keep()
	if err != nil {
		return fmt.Errorf("nhm's keep list cannot be read: %w", err)
	}
	rep.Keep = append(rep.Keep, keep...)
	st := State{Format: StateFormat, Version: StateVersion, ExportedAt: env.now().UTC().Format(time.RFC3339),
		Tokens: []TokenRef{}, Wake: []WakeEntry{}, Keep: rep.Keep}

	// A handback that a signal or a crash ended can leave its token
	// directory behind: sweep any that is old enough to be no live run's.
	sweepHandbackDirs(env.InstanceDir, env.now())
	dir, err := os.MkdirTemp(env.InstanceDir, handbackDirPrefix)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	list, err := env.Bindings.List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active, bindings.Displaced}})
	if err != nil {
		return err
	}
	// One token per identity: the active binding's first (List is newest
	// first).
	seen := map[string]bool{}
	ordered := append(filter(list, bindings.Active), filter(list, bindings.Displaced)...)
	var errs []error
	for _, b := range ordered {
		k := key(b.MailProjectKey, b.MailName)
		if seen[k] {
			continue
		}
		token, err := store.ReadSecret(env.InstanceDir, b.ID)
		if errors.Is(err, store.ErrNoSecret) || (err == nil && token == "") {
			continue
		}
		if err != nil {
			errs = append(errs, refused("%s: nhm's token cannot be read, and handing back would lose it: %v", b.MailName, err))
			continue
		}
		seen[k] = true
		if !tokenName.MatchString(b.MailName) {
			errs = append(errs, refused("%s: the Bash cannot keep a token under that name", b.MailName))
			continue
		}
		file := filepath.Join(dir, "token-"+b.ID)
		if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
			return err
		}
		project := b.MailProjectKey
		st.Tokens = append(st.Tokens, TokenRef{ProjectKey: &project, ProjectHash: ProjectHash(project), Name: b.MailName, File: file})
		rep.Tokens = append(rep.Tokens, Item{Name: b.MailName, ProjectKey: project, BindingID: b.ID, Action: ActionExported})
	}
	for _, b := range filter(list, bindings.Active) {
		// The tail first: mail delivered after it is after the Bash's
		// cursor whatever happens next, so mail that arrives while the
		// inbox is read below can never fall between the two.
		tail, err := env.Events.Tail(ctx, b.MailProjectKey, b.MailName)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: the delivery cursor cannot be read: %w", b.MailName, err))
			continue
		}
		// Settle stale attempts and mark read mail read, so only mail
		// still unread counts as pending.
		if env.RefreshWake != nil {
			if err := env.RefreshWake(ctx, b); err != nil {
				errs = append(errs, fmt.Errorf("%s: the wake state cannot be brought up to date: %w", b.MailName, err))
				continue
			}
		}
		w, it, err := exportWake(ctx, env, b, tail)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		st.Wake = append(st.Wake, w)
		rep.Wake = append(rep.Wake, it)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// The document must be one the Bash's own check accepts.
	if _, err := ParseState(data); err != nil {
		return err
	}
	rep.Notes = append(rep.Notes, "overdue acks: the Bash wakes each agent once for any it has")
	if rep.DryRun {
		return nil
	}
	file := filepath.Join(dir, "state.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return err
	}
	if err := env.Bash.Import(ctx, file); err != nil {
		// `acfs agents import` refuses with a non-zero exit, as it fails;
		// either way it wrote nothing.
		return refused("the Bash refused the state: %v", err)
	}
	return nil
}

// handbackDirPrefix names the private directories that carry the tokens
// to the Bash.
const handbackDirPrefix = ".handback."

// staleHandbackDir is the age past which a token directory is no live
// handback's: a handback ends within its command timeouts.
const staleHandbackDir = 10 * time.Minute

// sweepHandbackDirs removes the token directories of handbacks that
// ended without their cleanup (a signal, a crash).
func sweepHandbackDirs(instanceDir string, now time.Time) {
	entries, err := os.ReadDir(instanceDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), handbackDirPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < staleHandbackDir {
			continue
		}
		_ = os.RemoveAll(filepath.Join(instanceDir, e.Name()))
	}
}

func filter(list []bindings.Binding, st bindings.Status) []bindings.Binding {
	var out []bindings.Binding
	for _, b := range list {
		if b.Status == st {
			out = append(out, b)
		}
	}
	return out
}

// exportWake is an agent's wake state for the Bash: its cursor sits just
// before the first message nhm has not announced, so the Bash wakes the
// agent for exactly that mail; with none pending, at tail. tail was read
// before the wake rows were brought up to date, and the cursor never
// passes it: mail delivered since then is new to the Bash.
func exportWake(ctx context.Context, env Env, b bindings.Binding, tail int64) (WakeEntry, Item, error) {
	project := b.MailProjectKey
	it := Item{Name: b.MailName, ProjectKey: project, BindingID: b.ID, Action: ActionExported}
	// The last nudge (the Bash's last_wake) and any nudge in flight.
	rows, err := env.DB.SQL().QueryContext(ctx, `SELECT COALESCE(last_outcome, ''), COALESCE(last_attempt_at, '')
		FROM wake WHERE binding_id = ?`, b.ID)
	if err != nil {
		return WakeEntry{}, it, err
	}
	var pending []int64
	var last time.Time
	var inFlight bool
	for rows.Next() {
		var outcome, attempt string
		if err := rows.Scan(&outcome, &attempt); err != nil {
			_ = rows.Close()
			return WakeEntry{}, it, err
		}
		if t, err := time.Parse(store.TimeFormat, attempt); err == nil && t.After(last) {
			last = t
		}
		if outcome == attemptStarted {
			inFlight = true
		}
	}
	rerr := rows.Err()
	if err := rows.Close(); err != nil {
		return WakeEntry{}, it, err
	}
	if rerr != nil {
		return WakeEntry{}, it, rerr
	}
	if inFlight {
		return WakeEntry{}, it, refused("%s: a nudge is in flight; hand back when it has ended", b.MailName)
	}
	if pending, err = pendingIDs(ctx, env, b.ID); err != nil {
		return WakeEntry{}, it, err
	}
	cursor := tail
	if len(pending) > 0 {
		events, err := env.Events.After(ctx, project, b.MailName, 0)
		if err != nil {
			return WakeEntry{}, it, fmt.Errorf("%s: the delivery events cannot be read: %w", b.MailName, err)
		}
		at := map[int64]int64{}
		for _, e := range events {
			if c, ok := at[e.MessageID]; e.recipient() && (!ok || e.Cursor < c) {
				at[e.MessageID] = e.Cursor
			}
		}
		for _, id := range pending {
			c, ok := at[id]
			if !ok {
				return WakeEntry{}, it, refused("%s: message %d is unread and pending, but Agent Mail has no delivery event for it: the Bash would never wake for it", b.MailName, id)
			}
			if c-1 < cursor {
				cursor = c - 1
			}
		}
		it.New = len(pending)
	}
	lastWake := int64(0)
	if !last.IsZero() {
		lastWake = last.Unix()
	}
	return WakeEntry{ProjectKey: &project, ProjectHash: ProjectHash(project), Name: b.MailName,
		State: WakeState{Cursor: &cursor, LastWake: &lastWake, Overdue: []int64{}}}, it, nil
}

// pendingIDs are a recipient's rows that no nudge announced and that it
// has not read.
func pendingIDs(ctx context.Context, env Env, bindingID string) ([]int64, error) {
	rows, err := env.DB.SQL().QueryContext(ctx, `SELECT message_id FROM wake WHERE binding_id = ?
		AND announced_at IS NULL AND COALESCE(last_outcome, '') <> ?`, bindingID, wakeOutcomeRead)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("wake row %q: %w", s, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
