package owner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/store"
)

// Mail calls an Agent Mail tool; *agentmail.Client has it.
type Mail interface {
	Call(ctx context.Context, tool string, args, out any) error
}

// Env is what takeover and handback run against.
type Env struct {
	DB          *store.DB
	Bindings    *bindings.Store
	InstanceDir string
	Bash        Bash
	Events      Events
	Mail        Mail
	// ImportBindings reads the agents the Bash spawned into bindings
	// (bindings.ImportOnly), or with dry says what it would import.
	ImportBindings func(ctx context.Context, dry bool) (bindings.ImportReport, error)
	// Keep is nhm's keep list (the [reap] keep of its config and ACFS's
	// agents.toml).
	Keep func() ([]string, error)
	// RefreshWake brings a binding's wake rows up to date before a
	// handback: stale attempts settled, read mail marked read (wake's
	// ResolveStale and ReadInbox). nil skips it.
	RefreshWake func(ctx context.Context, b bindings.Binding) error
	Now         func() time.Time
}

// ErrRefused marks a takeover or handback that a rule refused; the owner
// stays as it was. Any other error is a failure to read or write.
var ErrRefused = errors.New("refused")

func refused(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Token and wake actions.
const (
	ActionKept      = "kept"
	ActionSame      = "already_kept"
	ActionReplaced  = "replaced"
	ActionACFSOnly  = "acfs_only"
	ActionImported  = "imported"
	ActionNoBinding = "no_binding"
	ActionWould     = "would_import"
	ActionExported  = "exported"
	// OutcomeImported is wake.last_outcome of mail the Bash woke for.
	OutcomeImported = "imported"
	inboxLimit      = 1000
	attemptStarted  = "attempt_started"
	wakeOutcomeRead = "read"
)

// Item is what a takeover or handback did with one token or wake state.
type Item struct {
	Name       string `json:"mail_name"`
	ProjectKey string `json:"mail_project_key,omitempty"`
	BindingID  string `json:"binding_id,omitempty"`
	Action     string `json:"action"`
	Reason     string `json:"reason,omitempty"`
	// Handled counts the unread messages the Bash already woke the agent
	// for (now announced); New those it had not (nhm's wake nudges them).
	Handled int `json:"handled,omitempty"`
	New     int `json:"new,omitempty"`
}

// Report is a takeover's or a handback's result.
type Report struct {
	DryRun   bool                   `json:"dry_run"`
	Owner    core.Owner             `json:"owner"`
	Bindings *bindings.ImportReport `json:"bindings,omitempty"`
	Tokens   []Item                 `json:"tokens"`
	Wake     []Item                 `json:"wake"`
	Keep     []string               `json:"keep"`
	Notes    []string               `json:"notes"`
}

func newReport(dry bool) Report {
	return Report{DryRun: dry, Tokens: []Item{}, Wake: []Item{}, Keep: []string{}, Notes: []string{}}
}

// Takeover makes nhm the owner (plan-v8 section 5 step 6). Under the
// exclusive owner lock, so the Bash's mutators wait and then refuse, it
// imports the Bash's state: the agents it spawned (the oo26 bindings), the
// registration tokens, the wake state (the mail the Bash already woke an
// agent for is announced, so nhm does not nudge for it again) and the
// keep list, which nhm's keep list must cover. It reads and checks every
// entry before it writes any token or wake row: an entry that cannot be
// imported refuses the takeover with nothing of them written, and the
// owner stays the Bash. Only the bindings of the agents the Bash spawned
// are written first, as `admin recover reconcile` would write them. A dry
// run reads and checks everything and writes nothing.
func Takeover(ctx context.Context, env Env, dry bool) (Report, error) {
	rep := newReport(dry)
	if dry {
		o, err := core.ReadOwner(env.InstanceDir)
		if err != nil {
			return rep, err
		}
		rep.Owner = o
		if o.Owner == core.OwnerNhm {
			return rep, refused("nhm owns the instance already (generation %d)", o.Generation)
		}
		_, err = planTakeover(ctx, env, &rep)
		return rep, err
	}
	o, err := core.SwitchOwner(ctx, env.InstanceDir, core.OwnerNhm, env.now(), func(core.Owner) error {
		p, err := planTakeover(ctx, env, &rep)
		if err != nil {
			return err
		}
		return p.apply(ctx, env)
	})
	rep.Owner = o
	if err != nil && errors.Is(err, core.ErrAlreadyOwner) && !errors.Is(err, ErrRefused) {
		// The same answer as the dry run's: a refusal, not a failure.
		err = fmt.Errorf("%w: %w", ErrRefused, err)
	}
	return rep, err
}

// takeoverPlan is what a takeover writes once every entry has passed.
type takeoverPlan struct {
	secrets map[string]string  // binding id -> token
	wake    map[string][]int64 // binding id -> message ids to announce
}

func planTakeover(ctx context.Context, env Env, rep *Report) (*takeoverPlan, error) {
	data, err := env.Bash.Export(ctx)
	if err != nil {
		return nil, fmt.Errorf("the Bash's state cannot be exported: %w", err)
	}
	st, err := ParseState(data)
	if err != nil {
		return nil, refused("%v", err)
	}
	rep.Keep = append(rep.Keep, st.Keep...)
	have, err := env.Keep()
	if err != nil {
		return nil, fmt.Errorf("nhm's keep list cannot be read: %w", err)
	}
	if missing := notIn(st.Keep, have); len(missing) > 0 {
		return nil, refused("nhm's keep list lacks %s: add them to [reap] keep in ~/.config/nhm/config.toml or ACFS's agents.toml first",
			strings.Join(missing, ", "))
	}
	imp, err := env.ImportBindings(ctx, rep.DryRun)
	rep.Bindings = &imp
	if err != nil {
		return nil, fmt.Errorf("the Bash's agents cannot be imported: %w", err)
	}
	idx, err := loadIndex(ctx, env)
	if err != nil {
		return nil, err
	}
	if rep.DryRun {
		// What the import would bind, so the dry run checks it too.
		for _, it := range imp.Items {
			if it.Action == bindings.ActionWouldImport && it.ProjectKey != "" && it.MailName != "" {
				idx.add(bindings.Binding{MailName: it.MailName, MailProjectKey: it.ProjectKey, Status: bindings.Active})
			}
		}
	}
	p := &takeoverPlan{secrets: map[string]string{}, wake: map[string][]int64{}}
	var errs []error
	for _, t := range st.Tokens {
		it, err := planToken(env, idx, t, p, rep.DryRun)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rep.Tokens = append(rep.Tokens, it)
	}
	for _, w := range st.Wake {
		it, err := planWake(ctx, env, idx, w, p, rep.DryRun)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rep.Wake = append(rep.Wake, it)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return p, nil
}

// apply writes the tokens, then the wake rows in one transaction.
func (p *takeoverPlan) apply(ctx context.Context, env Env) error {
	for id, token := range p.secrets {
		if err := store.WriteSecret(env.InstanceDir, id, token); err != nil {
			return fmt.Errorf("the token of %s: %w", id, err)
		}
	}
	tx, err := env.DB.SQL().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	ts := store.FormatTime(env.now())
	for id, msgs := range p.wake {
		for _, m := range msgs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO wake (binding_id, message_id, first_seen_at, announced_at, last_outcome)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (binding_id, message_id) DO UPDATE SET announced_at = COALESCE(wake.announced_at, excluded.announced_at)
				WHERE COALESCE(wake.last_outcome, '') <> ?`,
				id, strconv.FormatInt(m, 10), ts, ts, OutcomeImported, attemptStarted); err != nil {
				return fmt.Errorf("the wake state of %s: %w", id, err)
			}
		}
	}
	return tx.Commit()
}

// notIn lists the names of want that have lacks, ignoring case.
func notIn(want, have []string) []string {
	set := map[string]bool{}
	for _, h := range have {
		set[strings.ToLower(h)] = true
	}
	var out []string
	for _, w := range want {
		if !set[strings.ToLower(w)] {
			out = append(out, w)
		}
	}
	return out
}

// index finds bindings by project and name, and projects by their hash.
type index struct {
	byKey    map[string][]bindings.Binding
	projects map[string]string
}

func key(project, name string) string { return project + "\x00" + strings.ToLower(name) }

func loadIndex(ctx context.Context, env Env) (*index, error) {
	list, err := env.Bindings.List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active, bindings.Displaced}})
	if err != nil {
		return nil, err
	}
	idx := &index{byKey: map[string][]bindings.Binding{}, projects: map[string]string{}}
	for _, b := range list {
		idx.add(b)
	}
	return idx, nil
}

func (idx *index) add(b bindings.Binding) {
	k := key(b.MailProjectKey, b.MailName)
	idx.byKey[k] = append(idx.byKey[k], b)
	idx.projects[ProjectHash(b.MailProjectKey)] = b.MailProjectKey
}

// project is an entry's project key, as nhm keys it: a hash-only entry
// takes the project of a binding with that hash, and a path through a
// symlink the canonical path nhm's bindings use.
func (idx *index) project(k *string, h string) string {
	p := projectOf(k)
	if p == "" {
		return idx.projects[h]
	}
	if _, ok := idx.projects[ProjectHash(p)]; !ok {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			if _, ok := idx.projects[ProjectHash(real)]; ok {
				return real
			}
		}
	}
	return p
}

// find is the binding of an identity, the active one first.
func (idx *index) find(project, name string, activeOnly bool) (bindings.Binding, bool) {
	var displaced *bindings.Binding
	for _, b := range idx.byKey[key(project, name)] {
		if b.Status == bindings.Active {
			return b, true
		}
		if displaced == nil {
			b := b
			displaced = &b
		}
	}
	if displaced != nil && !activeOnly {
		return *displaced, true
	}
	return bindings.Binding{}, false
}

func readToken(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if first = strings.TrimSpace(first); first == "" {
		return "", errors.New("the file is empty")
	}
	return first, nil
}

func planToken(env Env, idx *index, t TokenRef, p *takeoverPlan, dry bool) (Item, error) {
	it := Item{Name: t.Name}
	token, err := readToken(t.File)
	if err != nil {
		return it, refused("the token of %s (%s) cannot be read: %v", t.Name, t.File, err)
	}
	project := idx.project(t.ProjectKey, t.ProjectHash)
	it.ProjectKey = project
	if project == "" {
		it.Action, it.Reason = ActionACFSOnly, "no record names its project (hash "+t.ProjectHash+"); the token stays in ACFS's state only"
		return it, nil
	}
	b, ok := idx.find(project, t.Name, false)
	if !ok {
		// No agent nhm knows holds the identity, and the token record
		// names no kind to bind it with. Takeover deletes nothing of the
		// Bash's, so the token stays where it is.
		it.Action, it.Reason = ActionACFSOnly, "no binding for the identity; the token stays in ACFS's state"
		return it, nil
	}
	it.BindingID = b.ID
	if dry && b.ID == "" {
		it.Action = ActionWould
		return it, nil
	}
	old, err := store.ReadSecret(env.InstanceDir, b.ID)
	switch {
	case err == nil && old == token:
		it.Action = ActionSame
		return it, nil
	case err == nil:
		// The Bash owned the identity until now: its token is the one the
		// latest registration gave.
		it.Action, it.Reason = ActionReplaced, "nhm kept another token for this binding; the Bash's replaces it"
	case errors.Is(err, store.ErrNoSecret):
		it.Action = ActionKept
	default:
		return it, fmt.Errorf("nhm's token for %s cannot be read: %w", t.Name, err)
	}
	if dry {
		it.Reason = strings.TrimSpace("would: " + it.Reason)
		return it, nil
	}
	p.secrets[b.ID] = token
	return it, nil
}

func planWake(ctx context.Context, env Env, idx *index, w WakeEntry, p *takeoverPlan, dry bool) (Item, error) {
	it := Item{Name: w.Name}
	project := idx.project(w.ProjectKey, w.ProjectHash)
	it.ProjectKey = project
	b, ok := idx.find(project, w.Name, true)
	if project == "" || !ok {
		it.Action, it.Reason = ActionNoBinding, "no active binding: the agent is gone, and its wake state with it"
		return it, nil
	}
	it.BindingID = b.ID
	unread, err := unreadIDs(ctx, env.Mail, project, w.Name)
	if err != nil {
		return it, fmt.Errorf("the inbox of %s cannot be read: %w", w.Name, err)
	}
	// The Bash has woken the agent for a message only when the message's
	// own delivery event is at or before its cursor. An unread message
	// with no event (pruned, or under another key) proves nothing: it is
	// new, and nhm's wake nudges for it, as a handback refuses it.
	events, err := env.Events.After(ctx, project, w.Name, 0)
	if err != nil {
		return it, fmt.Errorf("the delivery events of %s cannot be read: %w", w.Name, err)
	}
	delivered := map[int64]int64{}
	for _, e := range events {
		if c, ok := delivered[e.MessageID]; e.recipient() && (!ok || e.Cursor < c) {
			delivered[e.MessageID] = e.Cursor
		}
	}
	var handled []int64
	for _, id := range unread {
		if c, ok := delivered[id]; ok && c <= *w.State.Cursor {
			handled = append(handled, id)
		} else {
			it.New++
		}
	}
	it.Handled = len(handled)
	it.Action = ActionImported
	if dry {
		it.Action = ActionWould
		return it, nil
	}
	p.wake[b.ID] = append(p.wake[b.ID], handled...)
	return it, nil
}

// unreadIDs lists a recipient's unread message ids without consuming them.
func unreadIDs(ctx context.Context, mail Mail, project, agent string) ([]int64, error) {
	var msgs []struct {
		ID     int64  `json:"id"`
		ReadTS string `json:"read_ts"`
	}
	if err := mail.Call(ctx, "fetch_inbox", map[string]any{"project_key": project, "agent_name": agent,
		"unread_only": true, "mark_read": false, "limit": inboxLimit, "include_bodies": false}, &msgs); err != nil {
		return nil, err
	}
	if len(msgs) >= inboxLimit {
		return nil, refused("%s has %d or more unread messages: read some first, so none is nudged twice", agent, inboxLimit)
	}
	ids := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		if m.ReadTS == "" {
			ids = append(ids, m.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}
