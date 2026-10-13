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
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
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
	// (bindings.ImportOnly), or with dry says what it would import. snap,
	// when set, is the herdr state to decide on instead of a fresh read
	// (ImportRequest.Snapshot).
	ImportBindings func(ctx context.Context, dry bool, snap *herdr.SessionSnapshot) (bindings.ImportReport, error)
	// Keep is nhm's keep list (the [reap] keep of its config and ACFS's
	// agents.toml).
	Keep func() ([]string, error)
	// RefreshWake brings a binding's wake rows up to date before a
	// handback: stale attempts settled, read mail marked read (wake's
	// ResolveStale and ReadInbox). nil skips it.
	RefreshWake func(ctx context.Context, b bindings.Binding) error
	Now         func() time.Time
	// Exclude names the agents a takeover may leave behind (takeover
	// --exclude): an Agent Mail name, a herdr name or a pane id, any case.
	// Every other agent nhm cannot import refuses the takeover.
	Exclude []string
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
	ActionKept     = "kept"
	ActionSame     = "already_kept"
	ActionReplaced = "replaced"
	ActionACFSOnly = "acfs_only"
	// ActionStale: Agent Mail refuses the Bash's token (the agent
	// registered again after its spawn), and nhm has no current one: no
	// token is kept, and retire leaves the identity active.
	ActionStale     = "stale"
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
	// Excluded lists the agents the takeover left behind because
	// --exclude named them.
	Excluded []string `json:"excluded"`
	Notes    []string `json:"notes"`
}

func newReport(dry bool) Report {
	return Report{DryRun: dry, Tokens: []Item{}, Wake: []Item{}, Keep: []string{}, Excluded: []string{}, Notes: []string{}}
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
		_, err = planTakeover(ctx, env, &rep, nil)
		return rep, err
	}
	o, err := core.SwitchOwner(ctx, env.InstanceDir, core.OwnerNhm, env.now(), func(core.Owner) error {
		// Check everything as the dry run does first, which writes
		// nothing: a refusal must leave no binding, operation row or
		// identity-file repair behind (acfs-9hkh.80). The exclusive owner
		// lock keeps the Bash's state as it is until the real pass, but
		// not herdr's: a pane can exit or a tab be renamed in between. So
		// the real pass decides on the herdr state the check saw, and
		// applies the plan the check passed instead of a new one
		// (acfs-9hkh.85).
		check := newReport(true)
		if _, err := planTakeover(ctx, env, &check, nil); err != nil {
			rep.Bindings, rep.Tokens, rep.Wake, rep.Keep, rep.Excluded, rep.Notes =
				check.Bindings, check.Tokens, check.Wake, check.Keep, check.Excluded, check.Notes
			return err
		}
		var seen *herdr.SessionSnapshot
		if check.Bindings != nil {
			seen = check.Bindings.Snapshot
		}
		p, err := planTakeover(ctx, env, &rep, seen)
		if err != nil {
			return err
		}
		if err := samePlan(check.Bindings, rep.Bindings); err != nil {
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

// samePlan checks that the real import did what the dry check planned:
// each pane would_import is now imported, every other decision the same.
// With one herdr snapshot only Agent Mail's identity files or the Bash's
// state could differ, and the owner lock holds the Bash; it reports what
// changed instead of switching the owner over it.
func samePlan(check, real *bindings.ImportReport) error {
	if check == nil || real == nil {
		return nil
	}
	want := map[string]string{}
	for _, it := range check.Items {
		a := it.Action
		if a == bindings.ActionWouldImport {
			a = bindings.ActionImported
		}
		want[it.PaneID] = a + " " + it.MailName
	}
	var diffs []string
	for _, it := range real.Items {
		if w, ok := want[it.PaneID]; !ok || w != it.Action+" "+it.MailName {
			diffs = append(diffs, fmt.Sprintf("pane %s: checked %q, did %q", it.PaneID, w, it.Action+" "+it.MailName))
		}
		delete(want, it.PaneID)
	}
	for pane, w := range want {
		diffs = append(diffs, fmt.Sprintf("pane %s: checked %q, did nothing", pane, w))
	}
	if len(diffs) == 0 {
		return nil
	}
	sort.Strings(diffs)
	return fmt.Errorf("the import changed between the check and the takeover (the Bash stays the owner; `nhm admin recover reconcile` shows the bindings): %s",
		strings.Join(diffs, "; "))
}

func planTakeover(ctx context.Context, env Env, rep *Report, snap *herdr.SessionSnapshot) (*takeoverPlan, error) {
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
	imp, err := env.ImportBindings(ctx, rep.DryRun, snap)
	if err != nil && herdr.ServerDown(err) {
		// No herdr server runs (a fresh install: ACFS starts none), so no
		// pane holds an agent to import. When the Bash keeps no token and
		// no wake state either, nothing is left behind and only the owner
		// changes (nhm-swm, ACFS acfs-9hkh.93); otherwise its agents need
		// the server for their import.
		if len(st.Tokens) > 0 || len(st.Wake) > 0 {
			return nil, refused("herdr is not running, and the Bash keeps %d tokens and %d wake entries: start herdr, "+
				"so the takeover can import its agents", len(st.Tokens), len(st.Wake))
		}
		// The empty snapshot is the herdr state this decided on: the real
		// pass imports from it and never dials again (acfs-9hkh.85).
		imp, err = bindings.ImportReport{DryRun: rep.DryRun, Items: []bindings.ImportItem{}, Warnings: []string{},
			Snapshot: &herdr.SessionSnapshot{}}, nil
		rep.Notes = append(rep.Notes, "herdr is not running: no pane to import, and the Bash keeps no token or wake state")
	}
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
		it, err := planToken(ctx, env, idx, t, p, rep.DryRun)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rep.Tokens = append(rep.Tokens, it)
		if it.Action == ActionStale {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s (%s) has no current registration token: `nhm retire` will leave its "+
				"Agent Mail identity active. To retire the identity, call retire_agent with its newest token, or have the agent "+
				"call retire_agent itself before you retire it", t.Name, it.ProjectKey))
		}
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
	if err := leftBehind(env, rep, imp); err != nil {
		return nil, err
	}
	return p, nil
}

// leftBehind refuses a takeover that would leave one of the Bash's agents
// behind: an agent the import skipped, or a token no binding takes (it
// stays in ACFS's state, and the agent with it). An agent --exclude names
// is left behind on purpose and recorded in the report (acfs-9hkh.80).
func leftBehind(env Env, rep *Report, imp bindings.ImportReport) error {
	// The agents left behind: each skipped pane and each token no binding
	// takes. A skipped pane and its token are one agent.
	type agent struct {
		names   []string // the Agent Mail name, the herdr name
		project string   // "" when the import found none
		pane    string   // "" for a token
		why     string
		token   bool
	}
	var left []agent
	for _, it := range imp.Items {
		if it.Action == bindings.ActionSkipped {
			left = append(left, agent{names: []string{it.MailName, it.HerdrName}, project: it.ProjectKey, pane: it.PaneID,
				why: fmt.Sprintf("agent %s (pane %s) is not imported: %s", firstOf(it.MailName, it.HerdrName, it.PaneID), it.PaneID, it.Reason)})
		}
	}
	for _, t := range rep.Tokens {
		if t.Action == ActionACFSOnly {
			left = append(left, agent{names: []string{t.Name}, project: t.ProjectKey, token: true,
				why: fmt.Sprintf("the token of %s is not imported: %s", t.Name, t.Reason)})
		}
	}
	named := func(a agent, name string) bool {
		for _, n := range a.names {
			if n != "" && strings.EqualFold(n, name) {
				return true
			}
		}
		return false
	}
	// An exclusion names one agent: "/project:name" in that project, a
	// pane id with that pane's token, or a bare name that only one project
	// has (Agent Mail names are unique per project only).
	out := make([]bool, len(left))
	paired := map[int]bool{}
	var problems []string
	for _, x := range env.Exclude {
		var hit []int
		ambiguous := false
		switch {
		case strings.HasPrefix(x, "/") && strings.Contains(x, ":"):
			i := strings.LastIndex(x, ":")
			project, name := x[:i], x[i+1:]
			for j, a := range left {
				if a.project == project && named(a, name) {
					hit = append(hit, j)
				}
			}
		default:
			pane := -1
			for j, a := range left {
				if a.pane != "" && a.pane == x {
					pane = j
				}
			}
			if pane >= 0 {
				// The pane's agent and its token. A pane with no project
				// takes the token by name only while one project has it.
				p := left[pane]
				hit = append(hit, pane)
				projects := map[string]bool{}
				for j, a := range left {
					if a.token && (p.project == "" || a.project == p.project) && (named(a, p.names[0]) || named(a, p.names[1])) {
						hit = append(hit, j)
						projects[a.project] = true
					}
				}
				if len(projects) > 1 {
					// Not this pane's to choose: each token refuses on its
					// own unless /project:NAME excludes it.
					rep.Notes = append(rep.Notes, fmt.Sprintf("--exclude %s leaves its agent's tokens in place: they are in %d projects; exclude each by /project:NAME",
						x, len(projects)))
					hit = hit[:1]
				}
				break
			}
			projects := map[string]bool{}
			for j, a := range left {
				if named(a, x) {
					hit = append(hit, j)
					if a.project != "" {
						projects[a.project] = true
					}
				}
			}
			if len(projects) > 1 {
				list := make([]string, 0, len(projects))
				for p := range projects {
					list = append(list, p+":"+x)
				}
				sort.Strings(list)
				problems = append(problems, fmt.Sprintf("--exclude %s names agents in %d projects: give one of %s",
					x, len(projects), strings.Join(list, ", ")))
				hit, ambiguous = nil, true
			}
		}
		if len(hit) == 0 && !ambiguous {
			rep.Notes = append(rep.Notes, fmt.Sprintf("--exclude %s names no agent the takeover would leave behind", x))
		}
		// A pane and the tokens one exclusion takes with it are one agent.
		withPane := false
		for _, j := range hit {
			out[j] = true
			withPane = withPane || !left[j].token
		}
		for _, j := range hit {
			if withPane && left[j].token {
				paired[j] = true
			}
		}
	}
	var listed []agent
	for j, a := range left {
		if !out[j] {
			problems = append(problems, a.why)
			continue
		}
		// That agent is listed once, by its pane (panes come first).
		if paired[j] {
			continue
		}
		listed = append(listed, a)
	}
	// One name in two projects is listed as project:name, so the report
	// tells them apart.
	count := map[string]int{}
	for _, a := range listed {
		count[strings.ToLower(firstOf(a.names[0], a.names[len(a.names)-1], a.pane))]++
	}
	for _, a := range listed {
		name := firstOf(a.names[0], a.names[len(a.names)-1], a.pane)
		if count[strings.ToLower(name)] > 1 && a.project != "" {
			name = a.project + ":" + name
		}
		rep.Excluded = append(rep.Excluded, name)
		rep.Notes = append(rep.Notes, fmt.Sprintf("%s is left behind (--exclude): %s", name, a.why))
	}
	if len(problems) == 0 {
		return nil
	}
	return refused("%s; fix that first, or name each agent to leave behind with --exclude (NAME, /project:NAME or a pane id)",
		strings.Join(problems, "; "))
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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

func planToken(ctx context.Context, env Env, idx *index, t TokenRef, p *takeoverPlan, dry bool) (Item, error) {
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
	// The Bash keeps the token of the spawn's create_agent_identity. An
	// agent that registered itself again since then (register_agent,
	// macro_start_session) has a newer one, and Agent Mail refuses the
	// kept one (acfs-9hkh.92). The check changes nothing, so a dry run
	// makes it too.
	state, err := agentmail.CheckToken(ctx, env.Mail, project, t.Name, token)
	if err != nil {
		return it, fmt.Errorf("the token of %s cannot be checked with Agent Mail: %w", t.Name, err)
	}
	var note string
	switch state {
	case agentmail.TokenStale, agentmail.TokenNoAgent:
		return staleToken(ctx, env, it, b, state)
	case agentmail.TokenRetired:
		note = "not checked: Agent Mail has the identity retired"
	}
	if dry && b.ID == "" {
		it.Action, it.Reason = ActionWould, note
		return it, nil
	}
	old, err := store.ReadSecret(env.InstanceDir, b.ID)
	switch {
	case err == nil && old == token:
		it.Action, it.Reason = ActionSame, note
		return it, nil
	case err == nil:
		// Agent Mail accepts the Bash's token (or has the identity
		// retired), so nhm's is the older one.
		it.Action, it.Reason = ActionReplaced, "nhm kept another token for this binding; the Bash's replaces it"
	case errors.Is(err, store.ErrNoSecret):
		it.Action = ActionKept
	default:
		return it, fmt.Errorf("nhm's token for %s cannot be read: %w", t.Name, err)
	}
	if note != "" {
		it.Reason = strings.TrimPrefix(it.Reason+"; "+note, "; ")
	}
	if dry {
		it.Reason = strings.TrimSpace("would: " + it.Reason)
		return it, nil
	}
	p.secrets[b.ID] = token
	return it, nil
}

// staleToken is the item of a Bash token Agent Mail refuses (state is
// TokenStale or TokenNoAgent). nhm's own token for the binding, kept by
// its registration hook, may be the current one; then nothing changes.
// Otherwise the item is stale and no token is written: a refused token
// only makes retire fail later.
func staleToken(ctx context.Context, env Env, it Item, b bindings.Binding, state string) (Item, error) {
	why := "Agent Mail refuses it: the agent registered again after its spawn, which gives a new token"
	if state == agentmail.TokenNoAgent {
		why = "Agent Mail has no identity " + it.Name + " in this project"
	}
	if b.ID != "" {
		own, err := store.ReadSecret(env.InstanceDir, b.ID)
		switch {
		case err == nil && state == agentmail.TokenStale:
			ownState, err := agentmail.CheckToken(ctx, env.Mail, b.MailProjectKey, b.MailName, own)
			if err != nil {
				return it, fmt.Errorf("nhm's token for %s cannot be checked with Agent Mail: %w", it.Name, err)
			}
			if ownState == agentmail.TokenCurrent {
				it.Action, it.Reason = ActionSame, "the Bash's token is stale; nhm's own is current"
				return it, nil
			}
		case err != nil && !errors.Is(err, store.ErrNoSecret):
			return it, fmt.Errorf("nhm's token for %s cannot be read: %w", it.Name, err)
		}
	}
	it.Action, it.Reason = ActionStale, why+"; nhm keeps no token"
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
