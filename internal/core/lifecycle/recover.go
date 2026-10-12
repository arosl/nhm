package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
)

// RestoreItem is one binding after a restore.
type RestoreItem struct {
	MailName  string `json:"mail_name"`
	BindingID string `json:"binding_id"`
	PaneID    string `json:"pane_id,omitempty"`
	// Action: verified, displaced.
	Action string   `json:"action"`
	Reason string   `json:"reason,omitempty"`
	Redone []string `json:"reapplied,omitempty"`
}

// Restore is the plugin's startup hook, `nhm admin restore` (plan-v8
// 2.10): it verifies each active binding against the restored pane and
// native session, applies the herdr name, the sidebar tokens and Agent
// Mail's pane identity file again (herdr does not restore tokens), marks
// the rest displaced, and starts nothing.
func Restore(ctx context.Context, env Env) ([]RestoreItem, error) {
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("herdr: %w", err)
	}
	panes := map[string]bool{}
	for _, p := range snap.Panes {
		panes[p.PaneID] = true
	}
	agents := map[string]herdr.AgentInfo{}
	for _, a := range snap.Agents {
		agents[a.PaneID] = a
	}
	list, err := env.Bindings.List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return nil, err
	}
	var items []RestoreItem
	for _, b := range list {
		item := RestoreItem{MailName: b.MailName, BindingID: b.ID, PaneID: b.PaneID}
		a, hasAgent := agents[b.PaneID]
		switch {
		case b.PaneID == "" || !panes[b.PaneID]:
			item.Reason = "the pane did not come back"
		case !hasAgent:
			item.Reason = "the pane holds no agent"
		case a.Agent != "" && a.Agent != b.Kind:
			item.Reason = fmt.Sprintf("the pane holds a %s agent; the binding is %s", a.Agent, b.Kind)
		case b.NativeSession != nil && a.AgentSession != nil && a.AgentSession.Value != "" && a.AgentSession.Value != b.NativeSession.Value:
			item.Reason = "the pane's native session is another one"
		}
		if item.Reason != "" {
			item.Action = "displaced"
			if _, err := env.Bindings.Displace(ctx, b.ID, b.Generation, "restore: "+item.Reason); err != nil {
				item.Reason += "; " + err.Error()
			}
			items = append(items, item)
			continue
		}
		item.Action = "verified"
		alias := b.HerdrAlias
		if alias != "" && a.Name != alias {
			if _, err := env.Herdr.AgentRename(ctx, b.PaneID, alias); err == nil {
				item.Redone = append(item.Redone, "herdr name "+alias)
			} else {
				item.Reason = "herdr name: " + err.Error()
			}
		}
		tokens := map[string]string{"mail": b.MailName}
		if b.Model != "" {
			tokens["model"] = b.Model
		}
		if err := env.Herdr.PaneReportMetadata(ctx, herdr.ReportMetadataParams{PaneID: b.PaneID, Source: "nhm", Agent: b.Kind, Tokens: tokens}); err == nil {
			item.Redone = append(item.Redone, "sidebar tokens")
		}
		if env.MailConfigHome != "" {
			if _, err := agentmail.WriteIdentity(env.MailConfigHome, b.MailProjectKey, b.PaneID, b.MailName, env.now()); err == nil {
				item.Redone = append(item.Redone, "Agent Mail identity file")
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// Inspection is `nhm admin recover inspect`.
type Inspection struct {
	Live      []InspectBinding `json:"live"`
	Displaced []InspectBinding `json:"displaced"`
	Orphans   []InspectBinding `json:"orphans"`
	Pending   []PendingOp      `json:"pending_operations"`
	// Duplicates are native sessions that more than one active binding
	// claims.
	Duplicates map[string][]string `json:"duplicate_sessions"`
}

// InspectBinding is one binding in an inspection.
type InspectBinding struct {
	MailName  string           `json:"mail_name"`
	BindingID string           `json:"binding_id"`
	PaneID    string           `json:"pane_id,omitempty"`
	Kind      string           `json:"kind"`
	Session   string           `json:"native_session,omitempty"`
	Verdict   bindings.Verdict `json:"verdict"`
	Reason    string           `json:"reason,omitempty"`
}

// PendingOp is an operation with no final outcome.
type PendingOp struct {
	OperationID string    `json:"operation_id"`
	Verb        string    `json:"verb"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	// Unknown counts its receipts whose outcome is not known.
	Unknown int `json:"unknown_receipts"`
}

// Inspect lists live, displaced and orphan bindings, the operations with
// no final outcome, and the native sessions that two bindings claim.
func Inspect(ctx context.Context, env Env) (Inspection, error) {
	in := Inspection{Live: []InspectBinding{}, Displaced: []InspectBinding{}, Orphans: []InspectBinding{},
		Pending: []PendingOp{}, Duplicates: map[string][]string{}}
	list, err := env.Bindings.List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active, bindings.Displaced}})
	if err != nil {
		return in, err
	}
	sessions := map[string][]string{}
	for _, b := range list {
		ib := InspectBinding{MailName: b.MailName, BindingID: b.ID, PaneID: b.PaneID, Kind: b.Kind}
		if b.NativeSession != nil {
			ib.Session = b.NativeSession.Value
		}
		if b.Status == bindings.Displaced {
			ib.Verdict, ib.Reason = bindings.NotActive, b.StatusReason
			in.Displaced = append(in.Displaced, ib)
			continue
		}
		v, err := bindings.Verify(ctx, env.Herdr, b)
		if err != nil {
			return in, err
		}
		ib.Verdict = v.Verdict
		if ib.Session != "" {
			sessions[ib.Session] = append(sessions[ib.Session], b.MailName)
		}
		switch v.Verdict {
		case bindings.Verified, bindings.Unverified:
			in.Live = append(in.Live, ib)
		default:
			for _, c := range v.Checks {
				if c.Result == bindings.CheckFail {
					ib.Reason = c.Name + ": " + c.Detail
					break
				}
			}
			in.Orphans = append(in.Orphans, ib)
		}
	}
	for s, names := range sessions {
		if len(names) > 1 {
			in.Duplicates[s] = names
		}
	}
	rows, err := env.DB.SQL().QueryContext(ctx, `SELECT o.operation_id, o.verb, o.status, o.created_at,
		(SELECT COUNT(*) FROM receipts r WHERE r.operation_id = o.operation_id
			AND r.status IN ('pending', 'attempt_started', 'possibly_submitted'))
		FROM operations o WHERE o.completed_at IS NULL ORDER BY o.created_at`)
	if err != nil {
		return in, err
	}
	defer rows.Close()
	for rows.Next() {
		var p PendingOp
		var created string
		if err := rows.Scan(&p.OperationID, &p.Verb, &p.Status, &created, &p.Unknown); err != nil {
			return in, err
		}
		p.CreatedAt, _ = time.Parse(store.TimeFormat, created)
		in.Pending = append(in.Pending, p)
	}
	return in, rows.Err()
}

// ResumePlan is what `recover resume` would run.
type ResumePlan struct {
	MailName  string   `json:"mail_name"`
	BindingID string   `json:"binding_id"`
	PaneID    string   `json:"pane_id"`
	Argv      []string `json:"argv"`
	Ready     bool     `json:"pane_ready"`
	Reason    string   `json:"reason,omitempty"`
	Started   bool     `json:"started"`
}

// Resume validates a binding's resume argv and its pane, and with run
// starts it in the pane and bumps the generation (an operator's call;
// the CLI asks first). A dry run changes nothing.
func Resume(ctx context.Context, env Env, target string, run bool) (ResumePlan, error) {
	b, err := find(ctx, env, target, "")
	if err != nil {
		return ResumePlan{}, err
	}
	if !run {
		p, err := planResume(ctx, env, b)
		return p, err
	}
	// The binding's lock, as recycle holds it: a recycle or a send in
	// progress refuses, and nothing starts a second agent in the pane
	// between the check and the start.
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	unlock, err := send.LockTarget(lctx, env.InstanceDir, b.ID)
	cancel()
	switch {
	case errors.Is(err, send.ErrTargetBusy):
		return ResumePlan{MailName: b.MailName, BindingID: b.ID, PaneID: b.PaneID},
			fmt.Errorf("%s is being recycled or sent to; try again when that ends", b.MailName)
	case err != nil:
		return ResumePlan{MailName: b.MailName, BindingID: b.ID, PaneID: b.PaneID}, err
	}
	defer unlock()
	if b, err = env.Bindings.Get(ctx, b.ID); err != nil {
		return ResumePlan{}, err
	}
	p, err := planResume(ctx, env, b)
	if err != nil {
		return p, err
	}
	if !p.Ready {
		return p, errors.New(p.Reason)
	}
	a := kinds.Lookup(b.Kind)
	ms := readyWait.Milliseconds()
	alias := b.HerdrAlias
	if alias == "" {
		alias = strings.ToLower(b.MailName)
	}
	if _, err := env.Herdr.AgentStart(ctx, herdr.AgentStartParams{Name: alias, Kind: a.Name, PaneID: b.PaneID, Args: p.Argv[1:], TimeoutMS: &ms}); err != nil {
		return p, err
	}
	occ := bindings.Occupant{WorkspaceID: b.WorkspaceID, TabID: b.TabID, PaneID: b.PaneID, HerdrAlias: alias, Kind: b.Kind,
		Model: b.Model, Effort: b.Effort, LaunchID: b.LaunchID, NativeSession: b.NativeSession,
		Evidence: []bindings.Evidence{{Kind: "resume", Detail: "nhm admin recover resume --run"}}}
	if _, err := env.Bindings.Occupy(ctx, b.ID, b.Generation, occ); err != nil {
		return p, fmt.Errorf("binding: %w", err)
	}
	p.Started = true
	return p, nil
}

// planResume is a binding's resume argv and whether its pane can take it.
func planResume(ctx context.Context, env Env, b bindings.Binding) (ResumePlan, error) {
	p := ResumePlan{MailName: b.MailName, BindingID: b.ID, PaneID: b.PaneID}
	a := kinds.Lookup(b.Kind)
	session := ""
	if b.NativeSession != nil {
		session = b.NativeSession.Value
	}
	// The model, the effort and the guard hooks, as the launch had them.
	p.Argv = a.Resume(session, b.Model, b.Effort, env.LaunchSettings)
	switch {
	case b.Status == bindings.Retired:
		return p, fmt.Errorf("%s is retired", b.MailName)
	case p.Argv == nil:
		return p, fmt.Errorf("no resume for %s: the kind has no resume argv, or the session id %q is not a plain id", b.MailName, session)
	case p.Argv[0] != a.Name:
		return p, fmt.Errorf("the resume argv runs %s, not %s: herdr starts kinds only", p.Argv[0], a.Name)
	case b.PaneID == "":
		return p, fmt.Errorf("%s has no pane", b.MailName)
	}
	if _, err := env.Herdr.AgentGet(ctx, b.PaneID); err == nil {
		p.Reason = "pane " + b.PaneID + " already holds an agent"
	} else if info, err := env.Herdr.PaneProcessInfo(ctx, b.PaneID); err != nil {
		p.Reason = "pane " + b.PaneID + ": " + err.Error()
	} else if info.ShellPID == nil || info.ForegroundProcessGroupID == nil || *info.ShellPID != *info.ForegroundProcessGroupID {
		p.Reason = "pane " + b.PaneID + " is not at its shell prompt"
	} else {
		p.Ready = true
	}
	return p, nil
}

// Export is a host move's file (plan-v8 2.10): the bindings with their
// tokens, the transcripts and the keep list. Socket paths and pane ids
// never travel.
type Export struct {
	Version     int                `json:"version"`
	ExportedAt  time.Time          `json:"exported_at"`
	Project     string             `json:"project"`
	Bindings    []ExportedBinding  `json:"bindings"`
	Transcripts []ExportTranscript `json:"transcripts"`
	Keep        []string           `json:"keep"`
}

// ExportedBinding is one identity to carry over.
type ExportedBinding struct {
	BindingID     string                  `json:"binding_id"`
	MailName      string                  `json:"mail_name"`
	Kind          string                  `json:"kind"`
	Model         string                  `json:"model,omitempty"`
	Effort        string                  `json:"effort,omitempty"`
	ProjectID     string                  `json:"project_id"`
	NativeSession *bindings.NativeSession `json:"native_session,omitempty"`
	Token         string                  `json:"token,omitempty"`
}

// ExportTranscript is one preserved transcript, as a file in the export.
type ExportTranscript struct {
	BindingID string `json:"binding_id"`
	File      string `json:"file"`
	Reason    string `json:"reason"`
}

// ExportTo writes a project's export into dir (mode 0700): export.json
// (0600, it holds tokens) and transcripts/.
func ExportTo(ctx context.Context, env Env, project, dir string, keep []string) (Export, error) {
	x := Export{Version: 1, ExportedAt: env.now().UTC(), Project: project, Bindings: []ExportedBinding{},
		Transcripts: []ExportTranscript{}, Keep: keep}
	if x.Keep == nil {
		x.Keep = []string{}
	}
	list, err := env.Bindings.List(ctx, bindings.Filter{ProjectKey: project, Statuses: []bindings.Status{bindings.Active, bindings.Displaced}})
	if err != nil {
		return x, err
	}
	if err := exportDir(dir); err != nil {
		return x, err
	}
	if err := os.Mkdir(filepath.Join(dir, "transcripts"), 0o700); err != nil {
		return x, err
	}
	for _, b := range list {
		eb := ExportedBinding{BindingID: b.ID, MailName: b.MailName, Kind: b.Kind, Model: b.Model, Effort: b.Effort,
			ProjectID: b.ProjectID, NativeSession: b.NativeSession}
		eb.Token, _ = store.ReadSecret(env.InstanceDir, b.ID)
		x.Bindings = append(x.Bindings, eb)
		rows, err := env.DB.SQL().QueryContext(ctx, `SELECT path, reason FROM transcripts WHERE binding_id = ?`, b.ID)
		if err != nil {
			return x, err
		}
		for rows.Next() {
			var path, reason string
			if err := rows.Scan(&path, &reason); err != nil {
				_ = rows.Close()
				return x, err
			}
			name := b.ID + "-" + filepath.Base(path)
			if _, _, err := copyFile(path, filepath.Join(dir, "transcripts", name)); err == nil {
				x.Transcripts = append(x.Transcripts, ExportTranscript{BindingID: b.ID, File: name, Reason: reason})
			}
		}
		rerr := rows.Err()
		if err := rows.Close(); err != nil {
			return x, err
		}
		if rerr != nil {
			return x, rerr
		}
	}
	data, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return x, err
	}
	return x, store.WriteFileAtomic(filepath.Join(dir, "export.json"), append(data, '\n'), 0o600)
}

// ImportFrom reads an export into this instance: each identity becomes a
// displaced binding (its pane did not travel; adopt binds it), with its
// token kept; the transcripts are copied. An identity with an active
// binding here is skipped. It returns the keep list for the operator's
// config.
func ImportFrom(ctx context.Context, env Env, dir string) ([]string, []string, error) {
	// A regular file only: no link, and no FIFO or device that would
	// block or stream (O_NONBLOCK keeps the open itself from blocking).
	f, err := os.OpenFile(filepath.Join(dir, "export.json"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("export.json is not a regular file")
	}
	if fi, err := os.Lstat(filepath.Join(dir, "transcripts")); err == nil && !fi.IsDir() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("transcripts in the export is not a directory (a link is not followed)")
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<20))
	_ = f.Close()
	if err != nil {
		return nil, nil, err
	}
	var x Export
	if err := json.Unmarshal(data, &x); err != nil {
		return nil, nil, fmt.Errorf("export.json: %w", err)
	}
	if x.Version != 1 {
		return nil, nil, fmt.Errorf("export.json: version %d is not 1", x.Version)
	}
	// An export comes from another host: every field that reaches a path,
	// an argv or the store is checked, and one bad field refuses all.
	if err := validateExport(x); err != nil {
		return nil, nil, fmt.Errorf("export.json: %w; nothing imported", err)
	}
	var notes []string
	ids := map[string]string{}
	for _, eb := range x.Bindings {
		// An argv from another host is never kept: nhm builds the resume
		// argv from the kind and the validated session id.
		if eb.NativeSession != nil {
			ns := *eb.NativeSession
			ns.ResumeArgv = nil
			eb.NativeSession = &ns
		}
		b, err := env.Bindings.Create(ctx, bindings.New{MailName: eb.MailName, MailProjectKey: x.Project, ProjectID: eb.ProjectID,
			Provenance: bindings.Imported, Occupant: bindings.Occupant{Kind: eb.Kind, Model: eb.Model, Effort: eb.Effort,
				NativeSession: eb.NativeSession,
				Evidence:      []bindings.Evidence{{Kind: "import", Detail: "host move from binding " + eb.BindingID}}}})
		if err != nil {
			notes = append(notes, eb.MailName+": skipped: "+err.Error())
			continue
		}
		if _, err := env.Bindings.Displace(ctx, b.ID, b.Generation, "imported from another host: adopt it in its pane"); err != nil {
			notes = append(notes, eb.MailName+": "+err.Error())
		}
		if eb.Token != "" {
			if err := store.WriteSecret(env.InstanceDir, b.ID, eb.Token); err != nil {
				notes = append(notes, eb.MailName+": token not kept: "+err.Error())
			}
		}
		ids[eb.BindingID] = b.ID
		notes = append(notes, eb.MailName+": imported as "+b.ID+" (displaced; nhm adopt binds it)")
	}
	if len(x.Transcripts) > 0 {
		root, err := openTranscripts(dir)
		if err != nil {
			notes = append(notes, "transcripts: "+err.Error())
			return x.Keep, notes, nil
		}
		defer root.Close()
		for _, t := range x.Transcripts {
			id, ok := ids[t.BindingID]
			if !ok || filepath.Base(t.File) != t.File {
				continue
			}
			b, err := env.Bindings.Get(ctx, id)
			if err != nil {
				continue
			}
			if err := importTranscript(ctx, env, root, b, t.File); err != nil {
				notes = append(notes, "transcript "+t.File+": "+err.Error())
			}
		}
	}
	return x.Keep, notes, nil
}

// maxImportTranscript bounds one imported transcript.
const maxImportTranscript = 256 << 20

// openTranscripts opens the export's transcripts directory as a root, and
// checks that what it opened is the directory Lstat saw (not a link, not
// swapped between the two).
func openTranscripts(dir string) (*os.Root, error) {
	path := filepath.Join(dir, "transcripts")
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, errors.New("not a directory (a link is not followed)")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, errors.New("the directory changed while it was opened")
	}
	return root, nil
}

// importTranscript copies one transcript out of the export's root (never
// through a link out of it, a regular file only, at most
// maxImportTranscript bytes) into a staging dir in the instance, and
// keeps it from there.
func importTranscript(ctx context.Context, env Env, root *os.Root, b bindings.Binding, name string) error {
	in, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	switch {
	case err != nil:
		return err
	case !fi.Mode().IsRegular():
		return errors.New("not a regular file")
	case fi.Size() > maxImportTranscript:
		return fmt.Errorf("%d bytes, over the %d-byte limit", fi.Size(), maxImportTranscript)
	}
	stage, err := os.MkdirTemp(env.InstanceDir, ".import-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	staged := filepath.Join(stage, name)
	out, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, maxImportTranscript+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxImportTranscript {
		return errors.New("it grew past the size limit while it was copied")
	}
	_, err = Preserve(ctx, env.DB, env.InstanceDir, b, staged, "import", env.now())
	return err
}

// exportDir makes an export's directory, or accepts an existing one only
// when it is an empty directory of this user with mode 0700: an export
// holds tokens, and a shared or prepared directory could hold links that
// the writes would follow.
func exportDir(dir string) error {
	err := os.Mkdir(dir, 0o700)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir():
		return fmt.Errorf("%s exists and is not a directory", dir)
	case fi.Mode().Perm() != 0o700:
		return fmt.Errorf("%s exists with mode %o: an export needs a new directory, or an empty one of mode 0700", dir, fi.Mode().Perm())
	case !ok || int(st.Uid) != os.Getuid():
		return fmt.Errorf("%s is not yours", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty", dir)
	}
	return nil
}

var (
	mailNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	reasonPattern   = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)
	wordPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// validateExport checks every field of an export that reaches a path, an
// argv or the store.
func validateExport(x Export) error {
	if !filepath.IsAbs(x.Project) || filepath.Clean(x.Project) != x.Project {
		return fmt.Errorf("project %q is not a clean absolute path", x.Project)
	}
	ids := map[string]bool{}
	for i, eb := range x.Bindings {
		var bad []string
		if !idPattern.MatchString(eb.BindingID) {
			bad = append(bad, "binding_id")
		}
		if !mailNamePattern.MatchString(eb.MailName) {
			bad = append(bad, "mail_name")
		}
		if _, ok := kinds.Get(eb.Kind); !ok {
			bad = append(bad, "kind")
		}
		if eb.Model != "" && !spawn.ValidModel(eb.Model) {
			bad = append(bad, "model")
		}
		if eb.Effort != "" && !kinds.EffortPattern.MatchString(eb.Effort) {
			bad = append(bad, "effort")
		}
		if !idPattern.MatchString(eb.ProjectID) {
			bad = append(bad, "project_id")
		}
		if ns := eb.NativeSession; ns != nil {
			if !kinds.SessionPattern.MatchString(ns.Value) {
				bad = append(bad, "native_session")
			}
			for _, w := range []string{ns.Source, ns.Agent, ns.Kind} {
				if w != "" && !wordPattern.MatchString(w) {
					bad = append(bad, "native_session's source, agent or kind")
					break
				}
			}
		}
		if strings.ContainsAny(eb.Token, " \t\r\n") {
			bad = append(bad, "token")
		}
		if len(bad) > 0 {
			return fmt.Errorf("bindings[%d] (%q): bad %s", i, eb.MailName, strings.Join(bad, ", "))
		}
		ids[eb.BindingID] = true
	}
	for i, t := range x.Transcripts {
		if !ids[t.BindingID] || filepath.Base(t.File) != t.File || strings.HasPrefix(t.File, ".") || !reasonPattern.MatchString(t.Reason) {
			return fmt.Errorf("transcripts[%d] (%q): needs an exported binding, a plain file name and a reason", i, t.File)
		}
	}
	for i, k := range x.Keep {
		if !mailNamePattern.MatchString(k) {
			return fmt.Errorf("keep[%d] (%q) is not an Agent Mail name", i, k)
		}
	}
	return nil
}
