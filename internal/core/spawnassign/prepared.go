// retrievalQuery is derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/prompt_context.go (AssignmentPrompt.retrievalQuery) at
// commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// change: nhm's redaction. The rest of this file is nhm's.

package spawnassign

// The work prompt's CASS and CM context (--with-cass, --with-memory), as
// ntm's enrichSpawnWorkPrompt prepares it: once, for a fresh intent, before
// the ledger records the prompt, so the ledger keeps exactly what the agent
// gets and a replay or a later attempt never queries again. nhm prepares
// it with its own enrichment (internal/core/enrich) for the occupant's
// binding and context generation. The exposure is kept beside the ledger,
// keyed by the assignment's idempotency key, until the send that delivers
// the prompt records it under its own operation (send.Request.Prepared):
// that is what nhm's feedback and its "already given" rule read (nhm-i85
// step 3b, ACFS acfs-f2mc.2).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/redaction"
	"github.com/arosl/nhm/internal/robot"
)

// PromptContextOptions is ntm's switch for the CASS and CM context of a
// fresh work prompt; both sources are best-effort, as in ntm.
type PromptContextOptions struct {
	WithCASS, WithMemory bool
}

// Enabled reports whether any enrichment was requested.
func (o PromptContextOptions) Enabled() bool { return o.WithCASS || o.WithMemory }

// PreparedPrompt is a work prompt with its context, and what was put in.
type PreparedPrompt struct {
	Prompt string
	CASS   *robot.CASSInjectionInfo
	Memory *robot.CMInjectionInfo
}

// EnrichmentPort prepares a fresh work prompt's context for one occupant
// and keeps its exposure for the delivery of the assignment key. nhm's is
// SendPort.
type EnrichmentPort interface {
	Prepare(ctx context.Context, o Occupant, key, beadID, query, prompt string, opts PromptContextOptions) (PreparedPrompt, error)
}

// retrievalQuery is ntm's AssignmentPrompt.retrievalQuery: the bead's
// title, labels and description, in that order, redacted.
func retrievalQuery(title string, labels []string, description string) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{title, strings.Join(labels, " "), description} {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	query, _ := redaction.Redact(strings.Join(parts, "\n"))
	return query
}

// Prepare retrieves the context for the occupant's binding, plans it
// against what its context generation already has, frames it above the
// prompt and keeps the exposure for key. The binding must still be the
// occupant's.
func (p SendPort) Prepare(ctx context.Context, o Occupant, key, beadID, query, prompt string, opts PromptContextOptions) (PreparedPrompt, error) {
	if p.Env.DB == nil || p.Env.Bindings == nil || p.PreparedDir == "" {
		return PreparedPrompt{}, errors.New("no store for the work prompt's context")
	}
	b, err := p.Env.Bindings.Get(ctx, o.BindingID)
	if err != nil {
		return PreparedPrompt{}, err
	}
	if b.Status != bindings.Active || b.Generation != o.Generation {
		return PreparedPrompt{}, fmt.Errorf("binding %s changed before its work prompt was prepared", o.BindingID)
	}
	now := time.Now()
	if p.Env.Now != nil {
		now = p.Env.Now()
	}
	mode := func(on bool) enrich.Mode {
		if on {
			return enrich.BestEffort
		}
		return enrich.Off
	}
	retr := enrich.NewRetriever(p.Env.CM, p.Env.CASS, p.Env.Budgets, now).Retrieve(ctx, enrich.Request{Query: query,
		Workspace: p.ProjectRoot, CM: mode(opts.WithMemory), CASS: mode(opts.WithCASS)})
	cg := enrich.ContextGeneration(b)
	given, err := enrich.Given(ctx, p.Env.DB, b.ID, cg)
	if err != nil {
		return PreparedPrompt{}, err
	}
	plan := enrich.PlanFor(retr, given, p.Env.Budgets.BlockBytes)
	ex := enrich.Exposure{BindingID: b.ID, Generation: b.Generation, ContextGeneration: cg, BeadID: beadID, Plan: plan, Retrieval: retr}
	if err := writePrepared(p.PreparedDir, key, ex); err != nil {
		return PreparedPrompt{}, err
	}
	out := PreparedPrompt{Prompt: enrich.Frame(plan.Block, prompt)}
	if retr.CM.Enabled {
		ids, bytes := includedItems(plan.CM)
		out.Memory = &robot.CMInjectionInfo{Enabled: true, RulesInjected: ids, TokensAdded: bytes / 4, SkippedReason: skipReason(retr.CM)}
	}
	if retr.CASS.Enabled {
		ids, bytes := includedItems(plan.CASS)
		out.CASS = &robot.CASSInjectionInfo{Enabled: true, ItemsFound: len(retr.CASS.Items), ItemsInjected: len(ids),
			TokensAdded: bytes / 4, SkippedReason: skipReason(retr.CASS)}
	}
	return out, nil
}

// includedItems are a plan's included ids and their bytes (tokens are
// bytes/4, as ntm and nhm's send estimate them).
func includedItems(items []enrich.Item) (ids []string, bytes int) {
	for _, it := range items {
		if it.Status == enrich.Included {
			ids = append(ids, it.ID)
			bytes += it.Bytes
		}
	}
	return ids, bytes
}

func skipReason(r enrich.Report) string {
	if r.Error != "" {
		return r.Error
	}
	return r.SkippedReason
}

// preparedKeyPattern is an idempotency key that can name a file.
var preparedKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func preparedPath(dir, key string) (string, error) {
	if !preparedKeyPattern.MatchString(key) {
		return "", fmt.Errorf("assignment key %q cannot name a prepared context", key)
	}
	return filepath.Join(dir, key+".json"), nil
}

// writePrepared keeps an exposure for key: written whole (a temporary file
// renamed into place), readable by the user only. The block's text is not
// kept: it is in the recorded prompt, and the record needs only the items.
func writePrepared(dir, key string, ex enrich.Exposure) error {
	path, err := preparedPath(dir, key)
	if err != nil {
		return err
	}
	ex.Plan.Block = ""
	data, err := json.Marshal(ex)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".prepared-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// removePrepared drops key's kept exposure once its delivery recorded it.
// A file that stays only takes room.
func removePrepared(dir, key string) {
	if path, err := preparedPath(dir, key); err == nil && dir != "" {
		_ = os.Remove(path)
	}
}

// readPrepared is the exposure kept for key, or nil when none was.
func readPrepared(dir, key string) (*enrich.Exposure, error) {
	if dir == "" {
		return nil, nil
	}
	path, err := preparedPath(dir, key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ex enrich.Exposure
	if err := json.Unmarshal(data, &ex); err != nil {
		return nil, fmt.Errorf("prepared context %s: %w", path, err)
	}
	return &ex, nil
}
