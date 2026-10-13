package palette

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/store/history"
)

// Target is a frozen identity (plan-v4 section 4): the binding and the
// generation the palette saw, and what it shows of them.
type Target struct {
	BindingID  string `json:"binding_id"`
	Generation int64  `json:"generation"`
	PaneID     string `json:"pane_id"`
	MailName   string `json:"mail_name"`
	Kind       string `json:"kind"`
}

// Targets is a frozen target set, in the order it was picked.
type Targets []Target

// Key identifies the set by its bindings and generations, in any order.
func (ts Targets) Key() string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = t.BindingID + "@" + strconv.FormatInt(t.Generation, 10)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Expect is the send's expected generation per binding.
func (ts Targets) Expect() map[string]int64 {
	out := make(map[string]int64, len(ts))
	for _, t := range ts {
		out[t.BindingID] = t.Generation
	}
	return out
}

// PaneIDs are the targets' panes, for the send's selector.
func (ts Targets) PaneIDs() []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		if !slices.Contains(out, t.PaneID) {
			out = append(out, t.PaneID)
		}
	}
	return out
}

// ReviewKey identifies what a review was made for: the payload's
// revision and variables, and the target set. A preview whose key is
// not the current one is stale.
func ReviewKey(p Payload, ts Targets) string {
	h := sha256.New()
	fmt.Fprintf(h, "rev %d\n", p.Rev)
	keys := slices.Sorted(maps.Keys(p.Vars))
	for _, k := range keys {
		fmt.Fprintf(h, "var %q=%q\n", k, p.Vars[k])
	}
	fmt.Fprintf(h, "targets %s\n", ts.Key())
	return hex.EncodeToString(h.Sum(nil))
}

// Errors of a submission.
var (
	// ErrNoTargets: an explicitly empty target set is refused; the
	// palette never sends to "all" by default.
	ErrNoTargets = errors.New("no agent picked: pick at least one")
	// ErrInFlight: one submission runs at a time.
	ErrInFlight = errors.New("a send is in flight; wait for its result")
)

// Bodies writes the drafts sent as templates, one file per revision:
// Dir/<rev>/<key>.md, so the core's messages name the prompt. A file
// holds the draft after a newline: the core reads it as a template
// body, never as front matter, even when the draft starts with "---".
// The caller removes Dir when the palette exits.
type Bodies struct {
	Dir string
}

// Path writes p's text and returns the file's path.
func (b Bodies) Path(p Payload) (string, error) {
	if b.Dir == "" {
		return "", errors.New("palette: no directory for template drafts")
	}
	name := p.Key
	if !stateKey.MatchString(name) || strings.HasPrefix(name, ".") {
		name = "draft"
	}
	dir := filepath.Join(b.Dir, strconv.FormatUint(p.Rev, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte("\n"+p.Text+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// BuildRequest builds the send request of a payload to a target set, on
// base: the session, workspace, project, scope and enrichment that the
// caller resolved. The selector names the targets' panes and Expect
// freezes their bindings and generations, so the core refuses a target
// that changed since it was picked, under the target's lock and before
// any prompt; it never swaps in the agent now in that pane. An empty
// target set is refused. bodyPath is the file that holds a template
// payload's text (Bodies.Path).
func BuildRequest(base send.Request, p Payload, ts Targets, bodyPath string) (send.Request, error) {
	if len(ts) == 0 {
		return send.Request{}, ErrNoTargets
	}
	if strings.TrimSpace(p.Text) == "" {
		return send.Request{}, ErrEmptyDraft
	}
	req := base
	req.Selector = send.Selector{Panes: ts.PaneIDs()}
	req.Expect = ts.Expect()
	req.Source = history.SourcePalette
	req.Message, req.Template, req.Vars, req.FileContent, req.BeadID = "", "", nil, "", ""
	req.WhenAny, req.Force, req.DryRun, req.OperationID = false, false, false, ""
	if p.Template {
		if bodyPath == "" {
			return send.Request{}, errors.New("palette: a template payload needs its file")
		}
		req.Template, req.Vars = bodyPath, maps.Clone(p.Vars)
	} else {
		req.Message = p.Text
	}
	return req, nil
}

// Sender is nhm's send operation as the caller runs it (the CLI: under
// the owner lock, with its send Env). The palette has no other way to
// send.
type Sender func(ctx context.Context, req send.Request) (send.Result, error)

// Reader reads what the core recorded for an operation (send.Stored).
type Reader func(ctx context.Context, opID string) (send.Result, error)

// Deadlines of the async calls. A submission is not cancelled when the
// popup closes; its deadline only bounds a hung call.
const (
	PreviewTimeout = 30 * time.Second
	SubmitTimeout  = 2 * time.Minute
	ReadTimeout    = 10 * time.Second
)

// PreviewMsg is a dry run's answer. Seq is the request's number; Key is
// the ReviewKey it was made for.
type PreviewMsg struct {
	Seq    uint64
	Key    string
	Result send.Result
	Err    error
}

// SubmitMsg is a submission's answer.
type SubmitMsg struct {
	Seq         uint64
	OperationID string
	Result      send.Result
	Err         error
	// Panicked: the send stopped in a panic; what it did is unknown.
	Panicked bool
}

// ReconcileMsg is what the core recorded for an operation whose answer
// was lost.
type ReconcileMsg struct {
	OperationID string
	Result      send.Result
	Err         error
}

// Outcome is how a submission ended.
type Outcome int

const (
	// Answered: the core answered; each target has its receipt.
	Answered Outcome = iota
	// Refused: the core refused the request before it claimed the
	// operation, so nothing was sent.
	Refused
	// Unknown: the operation may exist, but its answer is lost. The
	// palette never sends it again on its own; Reconcile reads what the
	// core recorded.
	Unknown
)

// Outcome classifies the answer. The core claims an operation before
// any prompt and returns its id from then on, so an error without an id
// is a refusal.
func (m SubmitMsg) Outcome() Outcome {
	switch {
	case m.Panicked:
		return Unknown
	case m.Err == nil:
		return Answered
	case m.Result.OperationID == "":
		return Refused
	}
	return Unknown
}

// Submitter runs dry runs and submissions off bubbletea's update loop.
// Each answer carries the number of its request, so the model drops the
// stale ones. One submission runs at a time, and Wait lets the process
// finish it before it exits: closing the popup does not cancel a send.
type Submitter struct {
	send Sender
	read Reader
	// NewOperationID makes the id a submission is claimed under, so a
	// lost answer can be read back by id.
	newOpID func() (string, error)

	mu       sync.Mutex
	seq      uint64
	inFlight bool
	wg       sync.WaitGroup
}

// NewSubmitter returns a Submitter over the caller's send and read.
func NewSubmitter(s Sender, r Reader, newOpID func() (string, error)) *Submitter {
	return &Submitter{send: s, read: r, newOpID: newOpID}
}

func (s *Submitter) next() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// Preview starts a dry run of req: the same request with nothing
// claimed or sent. key is the ReviewKey it is made for.
func (s *Submitter) Preview(req send.Request, key string) (uint64, tea.Cmd) {
	seq := s.next()
	req.DryRun, req.OperationID = true, ""
	return seq, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), PreviewTimeout)
		defer cancel()
		res, _, err := call(ctx, s.send, req)
		return PreviewMsg{Seq: seq, Key: key, Result: res, Err: err}
	}
}

// Submit starts the send of req under a new operation id, at once, and
// returns the id and the command that waits for its answer. It returns
// ErrInFlight while another submission runs.
func (s *Submitter) Submit(req send.Request) (uint64, string, tea.Cmd, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight {
		return 0, "", nil, ErrInFlight
	}
	opID, err := s.newOpID()
	if err != nil {
		return 0, "", nil, err
	}
	s.seq++
	seq := s.seq
	s.inFlight = true
	s.wg.Add(1)
	req.DryRun, req.OperationID = false, opID
	ch := make(chan SubmitMsg, 1)
	// The send starts here, not when bubbletea runs the command: it must
	// run even if the program quits first.
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), SubmitTimeout)
		defer cancel()
		res, panicked, err := call(ctx, s.send, req)
		s.mu.Lock()
		s.inFlight = false
		s.mu.Unlock()
		ch <- SubmitMsg{Seq: seq, OperationID: opID, Result: res, Err: err, Panicked: panicked}
	}()
	return seq, opID, func() tea.Msg { return <-ch }, nil
}

// Reconcile reads what the core recorded for opID.
func (s *Submitter) Reconcile(opID string) tea.Cmd {
	return func() tea.Msg {
		if s.read == nil {
			return ReconcileMsg{OperationID: opID, Err: errors.New("palette: no way to read operations")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), ReadTimeout)
		defer cancel()
		res, err := s.read(ctx, opID)
		return ReconcileMsg{OperationID: opID, Result: res, Err: err}
	}
}

// InFlight reports whether a submission runs.
func (s *Submitter) InFlight() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight
}

// Wait returns when no submission runs.
func (s *Submitter) Wait() { s.wg.Wait() }

// call runs the send; a panic is an error, and panicked says so.
func call(ctx context.Context, f Sender, req send.Request) (res send.Result, panicked bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicked, err = true, fmt.Errorf("palette: the send stopped: %v", r)
		}
	}()
	res, err = f(ctx, req)
	return res, false, err
}
