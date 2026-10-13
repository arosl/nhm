package board

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arosl/nhm/internal/core/snapshot"
)

// Source names.
const (
	SourceState = "state"
	SourceMail  = "mail"
	SourceBeads = "beads"
)

// Schedules and deadlines (plan-v4 section 7): agent state every second,
// mail and beads every 15 seconds.
const (
	StateEvery    = time.Second
	MailEvery     = 15 * time.Second
	BeadsEvery    = 15 * time.Second
	StateDeadline = 3 * time.Second
	MailDeadline  = 6 * time.Second
	BeadsDeadline = 10 * time.Second
)

// ErrNoWorkspace means the board was given no workspace. It never falls
// back to the focused one.
var ErrNoWorkspace = errors.New("board: no workspace: the board acts on the workspace it was opened for, never on the focused one")

// Config makes a board.
type Config struct {
	// Deps are the snapshot service's sources. The state feed uses Herdr,
	// DB, Guards and LimitsFile; Mail and Beads each feed a source of
	// their own, which is left out when its dep is nil.
	Deps snapshot.Deps
	// WorkspaceID is the workspace the board shows (an id, never a label).
	WorkspaceID string
	// Reader reads the selected agent's pane for the preview; nil shows
	// no preview.
	Reader PaneReader
	// Now is time.Now in production.
	Now func() time.Time
	// Zero values take the package's schedules and deadlines.
	StateEvery, MailEvery, BeadsEvery          time.Duration
	StateDeadline, MailDeadline, BeadsDeadline time.Duration
}

// Board is the board's model: its sources, its rows and its selection.
// The feeds run on their own goroutines; everything else is for the
// bubbletea update loop alone.
type Board struct {
	State *Feed[*snapshot.Model]
	// Mail and Beads are nil when their source is not configured.
	Mail  *Feed[MailByKey]
	Beads *Feed[BeadsByKey]

	Sel     Selection
	Results Results

	preview   *previewSource
	workspace string
	changed   chan struct{}
}

// New makes a board; it reads nothing until Tick or Refresh.
func New(c Config) (*Board, error) {
	if c.WorkspaceID == "" {
		return nil, ErrNoWorkspace
	}
	if c.Deps.Herdr == nil {
		return nil, errors.New("board: no herdr client")
	}
	b := &Board{workspace: c.WorkspaceID, changed: make(chan struct{}, 1)}
	notify := func() {
		select {
		case b.changed <- struct{}{}:
		default:
		}
	}
	if c.Reader != nil {
		b.preview = newPreview(c.Reader, c.Now, notify)
	}
	opts := snapshot.Options{Workspace: c.WorkspaceID}
	stateDeps := c.Deps
	stateDeps.Mail, stateDeps.Beads = nil, nil
	b.State = NewFeed(FeedConfig[*snapshot.Model]{Name: SourceState, Now: c.Now, Changed: notify,
		Interval: or(c.StateEvery, StateEvery), Deadline: or(c.StateDeadline, StateDeadline),
		Read: func(ctx context.Context) (*snapshot.Model, error) {
			m := snapshot.Build(ctx, stateDeps, opts)
			return m, identitiesKnown(m, stateDeps.DB != nil)
		}})
	if c.Deps.Mail != nil {
		mailDeps := snapshot.Deps{Herdr: c.Deps.Herdr, DB: c.Deps.DB, Mail: c.Deps.Mail, Now: c.Deps.Now}
		b.Mail = NewFeed(FeedConfig[MailByKey]{Name: SourceMail, Now: c.Now, Changed: notify,
			Interval: or(c.MailEvery, MailEvery), Deadline: or(c.MailDeadline, MailDeadline),
			Read: func(ctx context.Context) (MailByKey, error) {
				m := snapshot.Build(ctx, mailDeps, opts)
				if err := identitiesKnown(m, mailDeps.DB != nil); err != nil {
					return nil, err
				}
				if err := sourceOK(m, snapshot.SourceMail); err != nil {
					return nil, err
				}
				// An inbox that failed this time keeps its last good
				// counts, or stays unknown: never zero.
				prev := b.Mail.State().Value
				out := MailByKey{}
				for _, a := range m.Agents {
					if a.Binding == nil || a.Mail == nil {
						continue
					}
					k := AgentKey(a).Key()
					if a.Mail.Status == snapshot.Fresh {
						out[k] = *a.Mail
					} else if last, ok := prev[k]; ok {
						out[k] = last
					}
				}
				return out, nil
			}})
	}
	if c.Deps.Beads != nil {
		beadsDeps := snapshot.Deps{Herdr: c.Deps.Herdr, DB: c.Deps.DB, Beads: c.Deps.Beads, Now: c.Deps.Now}
		beadsOpts := opts
		beadsOpts.LocalTimeout = or(c.BeadsDeadline, BeadsDeadline)
		b.Beads = NewFeed(FeedConfig[BeadsByKey]{Name: SourceBeads, Now: c.Now, Changed: notify,
			Interval: or(c.BeadsEvery, BeadsEvery), Deadline: or(c.BeadsDeadline, BeadsDeadline),
			Read: func(ctx context.Context) (BeadsByKey, error) {
				m := snapshot.Build(ctx, beadsDeps, beadsOpts)
				if err := identitiesKnown(m, beadsDeps.DB != nil); err != nil {
					return nil, err
				}
				// A project whose beads cannot be read leaves its agents'
				// beads unknown in the model, like those with none: keep
				// the last value rather than show "none".
				if err := sourceOK(m, snapshot.SourceBeads); err != nil {
					return nil, err
				}
				out := BeadsByKey{}
				for _, a := range m.Agents {
					if a.Binding != nil {
						out[AgentKey(a).Key()] = append([]string{}, a.Beads...)
					}
				}
				return out, nil
			}})
	}
	return b, nil
}

// identitiesKnown fails a build whose rows could not be keyed: without
// herdr's answer there are no live agents, and without the bindings
// every bound agent would look unbound (and every mark replaced).
func identitiesKnown(m *snapshot.Model, haveDB bool) error {
	if !m.HerdrOK() {
		return fmt.Errorf("herdr: %s", m.Sources[snapshot.SourceHerdr].Error)
	}
	if haveDB {
		return sourceOK(m, snapshot.SourceBindings)
	}
	return nil
}

func sourceOK(m *snapshot.Model, name string) error {
	h, ok := m.Sources[name]
	if !ok {
		return fmt.Errorf("%s: not read", name)
	}
	if h.Status == snapshot.Unavailable {
		return fmt.Errorf("%s: %s", name, h.Error)
	}
	return nil
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// WorkspaceID is the workspace the board shows.
func (b *Board) WorkspaceID() string { return b.workspace }

// Changed signals, coalesced, that a feed's state changed: the update
// loop then calls Sync and renders.
func (b *Board) Changed() <-chan struct{} { return b.changed }

// feed is a Feed of any value.
type feed interface {
	Tick(time.Time)
	Refresh()
	SetPaused(bool)
	Close()
	Health() Health
}

func (b *Board) feeds() []feed {
	out := []feed{b.State}
	if b.Mail != nil {
		out = append(out, b.Mail)
	}
	if b.Beads != nil {
		out = append(out, b.Beads)
	}
	if b.preview != nil {
		out = append(out, b.preview.feed)
	}
	return out
}

// Tick starts the reads that are due; it never blocks.
func (b *Board) Tick(now time.Time) {
	for _, f := range b.feeds() {
		f.Tick(now)
	}
}

// Refresh reads every source now (r).
func (b *Board) Refresh() {
	for _, f := range b.feeds() {
		f.Refresh()
	}
}

// RefreshMail reads the mail now (m).
func (b *Board) RefreshMail() {
	if b.Mail != nil {
		b.Mail.Refresh()
	}
}

// Invalidate is a herdr event: it only marks the agent state for a
// refresh, which the state feed coalesces.
func (b *Board) Invalidate() { b.State.Invalidate() }

// SetPaused pauses or resumes the automatic refresh (p).
func (b *Board) SetPaused(p bool) {
	for _, f := range b.feeds() {
		f.SetPaused(p)
	}
}

// Paused reports whether the automatic refresh is paused.
func (b *Board) Paused() bool { return b.State.State().Paused }

// Close stops the feeds.
func (b *Board) Close() {
	for _, f := range b.feeds() {
		f.Close()
	}
}

// Health lists each source's state, for the diagnostics view.
func (b *Board) Health() []Health {
	var out []Health
	for _, f := range b.feeds() {
		out = append(out, f.Health())
	}
	return out
}

// Model is the last agent state read, or nil.
func (b *Board) Model() *snapshot.Model {
	s := b.State.State()
	if !s.Has {
		return nil
	}
	return s.Value
}

// Rows joins the sources' last values.
func (b *Board) Rows() []Row {
	var mail State[MailByKey]
	var beads State[BeadsByKey]
	if b.Mail != nil {
		mail = b.Mail.State()
	}
	if b.Beads != nil {
		beads = b.Beads.State()
	}
	return BuildRows(b.Model(), mail, beads)
}

// Sync moves the selection onto the sources' last values; the update
// loop calls it on Changed.
func (b *Board) Sync() {
	b.Sel.Reconcile(b.Rows())
	b.FollowCursor()
}

// FollowCursor points the preview at the cursor's row; the update loop
// calls it after it moves the cursor. A new row is read at once.
func (b *Board) FollowCursor() {
	if b.preview != nil {
		b.preview.target(b.Sel.Cursor())
	}
}

// Preview is the preview of the cursor's row. ok is false while there is
// none for that row: no read yet, or the last read was for another row.
func (b *Board) Preview() (p Preview, ok bool) {
	if b.preview == nil {
		return Preview{}, false
	}
	s := b.preview.feed.State()
	r, has := b.Sel.Cursor()
	if !s.Has || !has || !same(s.Value.Key, r.Key) {
		return Preview{}, false
	}
	return s.Value, true
}

// Problems names the sources that are not fresh, in plain words, for the
// board's header: what failed, and how old the value shown is.
func (b *Board) Problems(now time.Time) []string {
	var out []string
	for _, h := range b.Health() {
		switch {
		case h.Err != "" && h.Has:
			out = append(out, fmt.Sprintf("%s: %s (showing %s old)", h.Source, h.Err, now.Sub(h.At).Round(time.Second)))
		case h.Err != "":
			out = append(out, fmt.Sprintf("%s: %s (no value yet)", h.Source, h.Err))
		}
	}
	return out
}
