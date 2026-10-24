// Package enrich puts CM's rules and CASS's hits before a prompt, checks
// CASS for duplicate work, and reports rule outcomes back to CM (plan-v8
// 2.13). herdr is not involved.
//
// One Retriever serves one batch (a send, or a spawn's kickoffs): it runs
// each unique query once, all under one total budget. Retrieved text is
// framed as reference material. Dedup is per context generation: a rule
// already given to the same context of an agent is suppressed, and a
// compaction, a clear or a resume starts a new context generation.
package enrich

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/arosl/nhm/internal/integrations/cass"
	"github.com/arosl/nhm/internal/integrations/cm"
)

// Mode is how a source takes part in a send.
type Mode string

// Modes. A best-effort failure is recorded and the send goes on; a
// required failure prevents the submission.
const (
	Off        Mode = "off"
	BestEffort Mode = "best-effort"
	Required   Mode = "required"
)

// ParseMode reads a mode; "" is off.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "", Off:
		return Off, nil
	case BestEffort, Required:
		return Mode(s), nil
	}
	return Off, fmt.Errorf("a mode is off, best-effort or required, not %q", s)
}

// Sources.
const (
	SourceCM   = "cm"
	SourceCASS = "cass"
)

// Item statuses and reasons.
const (
	Included   = "included"
	Omitted    = "omitted"
	Suppressed = "suppressed"

	ReasonTruncated      = "truncated_to_item_budget"
	ReasonBlockBudget    = "block_budget"
	ReasonAlreadyPresent = "already_present"
)

// CM is what enrich needs from cm; cm.Client has it.
type CM interface {
	Context(ctx context.Context, task, workspace string) (cm.Context, error)
	Mark(ctx context.Context, rule string, helpful bool, session string) error
	Outcome(ctx context.Context, status string, rules []string, session, text string) error
	OutcomeApply(ctx context.Context, session string) error
}

// CASS is what enrich needs from cass; cass.Client has it.
type CASS interface {
	Search(ctx context.Context, s cass.Search) ([]cass.Hit, error)
	IndexSession(ctx context.Context, path, workspace, key string) (string, error)
}

// Budgets bound a batch.
type Budgets struct {
	// Total is one budget for every query of the batch.
	Total      time.Duration
	BlockBytes int
	ItemBytes  int
	CASSLimit  int
}

// Item is one rule or hit.
type Item struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	// Version is the hash of the item's full text: the same id with new
	// text is a new version and is given again.
	Version string `json:"version"`
	// SHA256 and Bytes are of the text as framed.
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Status string `json:"status,omitempty"`
	Reason string `json:"reason,omitempty"`
	Label  string `json:"label,omitempty"`
	// Redacted counts the secrets replaced in the item.
	Redacted int    `json:"redacted,omitempty"`
	Text     string `json:"-"`
}

// Key is the item's dedup key.
func (i Item) Key() string { return i.Source + "\x00" + i.ID + "\x00" + i.Version }

// Report is what one source retrieved for a query.
type Report struct {
	Enabled       bool   `json:"enabled"`
	Mode          Mode   `json:"mode,omitempty"`
	Query         string `json:"query,omitempty"`
	Items         []Item `json:"items,omitempty"`
	Error         string `json:"error,omitempty"`
	SkippedReason string `json:"skipped_reason,omitempty"`
}

// Request is one retrieval.
type Request struct {
	// Query is the task's text. CM ranks it as it is; CASS gets its
	// keywords, any of them (cass.KeywordQuery).
	Query     string
	Workspace string
	CM        Mode
	CASS      Mode
}

// Retrieval is a request's answer.
type Retrieval struct {
	CM   Report
	CASS Report
}

// ErrRequired: a required source failed; nothing may be submitted.
var ErrRequired = errors.New("required enrichment failed")

// Err is ErrRequired with the failed sources, or nil.
func (r Retrieval) Err() error {
	var failed []string
	for _, rep := range []Report{r.CM, r.CASS} {
		if rep.Mode == Required && rep.Error != "" {
			failed = append(failed, rep.Error)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrRequired, strings.Join(failed, "; "))
}

// Retriever runs a batch's queries. Its zero value is not usable: use
// NewRetriever.
type Retriever struct {
	CM       CM
	CASS     CASS
	Budgets  Budgets
	deadline time.Time

	mu   sync.Mutex
	memo map[string]*memoEntry
}

type memoEntry struct {
	once sync.Once
	rep  Report
}

// NewRetriever starts a batch: its total budget starts now.
func NewRetriever(cmc CM, cs CASS, b Budgets, now time.Time) *Retriever {
	if b.Total <= 0 {
		b.Total = DefaultBudget
	}
	if b.BlockBytes <= 0 {
		b.BlockBytes = DefaultBlockBytes
	}
	if b.ItemBytes <= 0 {
		b.ItemBytes = DefaultItemBytes
	}
	if b.CASSLimit <= 0 {
		b.CASSLimit = DefaultCASSLimit
	}
	return &Retriever{CM: cmc, CASS: cs, Budgets: b, deadline: now.Add(b.Total), memo: map[string]*memoEntry{}}
}

// Retrieve runs the request's sources at once, each unique query once
// per batch, and never past the batch's budget.
func (r *Retriever) Retrieve(ctx context.Context, req Request) Retrieval {
	ctx, cancel := context.WithDeadline(ctx, r.deadline)
	defer cancel()
	var out Retrieval
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		out.CM = r.once(ctx, SourceCM, req.CM, req, r.cm)
	}()
	go func() {
		defer wg.Done()
		creq := req
		creq.Query = cass.KeywordQuery(req.Query)
		if creq.Query == "" && req.CASS != "" && req.CASS != Off {
			out.CASS = Report{Enabled: true, Mode: req.CASS, SkippedReason: "no keywords in the query"}
			return
		}
		out.CASS = r.once(ctx, SourceCASS, req.CASS, creq, r.cass)
	}()
	wg.Wait()
	return out
}

func (r *Retriever) once(ctx context.Context, source string, mode Mode, req Request,
	run func(context.Context, Request) ([]Item, error)) Report {
	if mode == "" || mode == Off {
		return Report{Enabled: false, Mode: Off, SkippedReason: "off"}
	}
	key := strings.Join([]string{source, req.Workspace, req.Query, fmt.Sprint(r.Budgets.ItemBytes, r.Budgets.CASSLimit)}, "\x00")
	r.mu.Lock()
	e, ok := r.memo[key]
	if !ok {
		e = &memoEntry{}
		r.memo[key] = e
	}
	r.mu.Unlock()
	e.once.Do(func() {
		e.rep = Report{Enabled: true, Query: req.Query}
		items, err := run(ctx, req)
		if err != nil {
			e.rep.Error = fmt.Sprintf("%s: %v", source, err)
			if ctx.Err() != nil {
				e.rep.Error = fmt.Sprintf("%s: over the %s budget", source, r.Budgets.Total)
			}
			return
		}
		e.rep.Items = items
	})
	rep := e.rep
	rep.Mode = mode
	return rep
}

func (r *Retriever) cm(ctx context.Context, req Request) ([]Item, error) {
	if r.CM == nil {
		return nil, cm.ErrNotInstalled
	}
	res, err := r.CM.Context(ctx, req.Query, req.Workspace)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, rule := range append(res.Rules, res.AntiPatterns...) {
		label := rule.Category
		if rule.Negative {
			label = strings.TrimSpace("avoid " + label)
		}
		items = append(items, r.item(SourceCM, rule.ID, label, rule.Content))
	}
	return items, nil
}

func (r *Retriever) cass(ctx context.Context, req Request) ([]Item, error) {
	if r.CASS == nil {
		return nil, cass.ErrNotInstalled
	}
	hits, err := r.CASS.Search(ctx, cass.Search{Query: req.Query, Workspace: req.Workspace, Limit: r.Budgets.CASSLimit,
		MaxContent: r.Budgets.ItemBytes})
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, h := range hits {
		text := h.Content
		if text == "" {
			text = h.Snippet
		}
		label := h.Agent
		if t := h.CreatedAt(); !t.IsZero() {
			label += ", " + t.Format("2006-01-02")
		}
		items = append(items, r.item(SourceCASS, h.ID(), label, text))
	}
	return items, nil
}

// item makes an item: its text, id and label cleaned, versioned by the
// full cleaned text, truncated to the item budget.
func (r *Retriever) item(source, id, label, text string) Item {
	text, redacted := clean(text)
	id, n := clean(id)
	label, m := clean(label)
	it := Item{Source: source, ID: id, Label: label, Version: digest(text)[:16], Redacted: redacted + n + m}
	if len(text) > r.Budgets.ItemBytes {
		text = truncate(text, r.Budgets.ItemBytes)
		it.Reason = ReasonTruncated
	}
	it.Text = text
	it.SHA256, it.Bytes = digest(text), len(text)
	return it
}

// clean makes text from CM or CASS safe to frame: secrets redacted (as
// ACFS's Bash --with-cass does), one line, and no "[nhm context" or "[end
// of nhm context" that could open or close the frame early. Both checks
// run on the folded text (fold.go), so neither a secret nor a marker
// hides behind an invisible code point or a look-alike, but the text the
// agent sees is the original, changed only where a check found something.
// It returns the text and the number of secrets replaced.
func clean(s string) (string, int) {
	s, n := redact(s)
	return neutralize(oneLine(s)), n
}

// safe is clean without the redaction, for nhm's own text (the query).
func safe(s string) string { return neutralize(oneLine(s)) }

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// truncate cuts s to at most n bytes on a rune boundary, with an
// ellipsis.
func truncate(s string, n int) string {
	const ell = "…"
	if n <= len(ell) {
		return ""
	}
	cut := n - len(ell)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ell
}

// oneLine puts s on one line: each run of white space and control
// characters becomes one space. CASS text can hold terminal text, and an
// ESC [201~ in the prompt would end herdr's paste (nhm-ubi).
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

// QueryOf is the query for a prompt: its first 500 bytes, on one line.
func QueryOf(prompt string) string {
	q := oneLine(prompt)
	if len(q) > 500 {
		q = truncate(q, 500)
		q = strings.TrimSuffix(q, "…")
	}
	return q
}
