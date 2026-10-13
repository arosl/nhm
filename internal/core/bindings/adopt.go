package bindings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

// Env is what adopt and the import need besides their request.
type Env struct {
	DB       *store.DB
	Bindings *Store
	Herdr    EnvHerdr
	// ConfigHome holds Agent Mail's identity files; "" means
	// agentmail.ConfigHome().
	ConfigHome string
	Now        func() time.Time
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// ErrNoAgent means the pane holds no agent: adopt binds a live agent.
var ErrNoAgent = errors.New("bindings: the pane holds no agent")

var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Alias is the herdr name of a mail name: the name in lowercase. It fails
// for a name herdr would refuse.
func Alias(mailName string) (string, error) {
	alias := strings.ToLower(mailName)
	if !aliasPattern.MatchString(alias) {
		return "", fmt.Errorf("bindings: %q makes no valid herdr name ([a-z][a-z0-9_-]{0,31})", mailName)
	}
	return alias, nil
}

// AdoptRequest binds an existing identity to the agent in a pane, by hand
// (plan-v8 2.7): when the evidence for an automatic binding is missing,
// or for a converted session (casr) with a new kind and model.
type AdoptRequest struct {
	MailName string `json:"mail_name"`
	PaneID   string `json:"pane_id"`
	// HandoffFrom names the binding whose identity the pane's agent takes
	// over; the generation goes up. Without it the identity must have no
	// binding that is active or displaced.
	HandoffFrom string `json:"handoff_from,omitempty"`
	Kind        string `json:"kind,omitempty"`
	// ExpectedSession is the agent session a caller reviewed in the pane,
	// "kind:value" (herdr's agent_session, as ui.Target keys an unbound
	// agent). When set, adopt refuses before it binds anything if the
	// pane's agent has another session or none: another agent started
	// there since the review (nhm-qbi.9).
	ExpectedSession string `json:"expected_session,omitempty"`
	Model           string `json:"model,omitempty"`
	ProjectKey      string `json:"mail_project_key"`
	ProjectID       string `json:"project_id"`
	OperationID     string `json:"-"`
}

// ErrAgentChanged means the pane's agent is not the one the caller
// reviewed (AdoptRequest.ExpectedSession).
var ErrAgentChanged = errors.New("bindings: the agent in the pane is not the one reviewed")

// SessionKey is an agent session as AdoptRequest.ExpectedSession names
// it: "kind:value", or "" for none.
func SessionKey(s *herdr.AgentSession) string {
	if s == nil {
		return ""
	}
	return s.Kind + ":" + s.Value
}

// AdoptResult is what adopt did.
type AdoptResult struct {
	OperationID  string   `json:"operation_id"`
	Binding      Binding  `json:"binding"`
	Created      bool     `json:"created"`
	IdentityPath string   `json:"identity_path,omitempty"`
	Warnings     []string `json:"warnings"`
}

// Adopt runs adopt. The caller holds the owner lock and checked that nhm
// owns the instance.
func Adopt(ctx context.Context, env Env, req AdoptRequest) (AdoptResult, error) {
	if req.PaneID == "" {
		return AdoptResult{}, errors.New("bindings: adopt needs a pane (--pane, or run it in the agent's pane)")
	}
	if req.MailName == "" && req.HandoffFrom == "" {
		return AdoptResult{}, errors.New("bindings: adopt needs a mail name or --handoff-from")
	}
	var source Binding
	if req.HandoffFrom != "" {
		var err error
		if source, err = env.Bindings.Get(ctx, req.HandoffFrom); err != nil {
			return AdoptResult{}, err
		}
		if req.MailName != "" && !strings.EqualFold(req.MailName, source.MailName) {
			return AdoptResult{}, fmt.Errorf("bindings: %s holds %s, not %s", source.ID, source.MailName, req.MailName)
		}
		req.MailName, req.ProjectKey, req.ProjectID = source.MailName, source.MailProjectKey, source.ProjectID
	}
	alias, err := Alias(req.MailName)
	if err != nil {
		return AdoptResult{}, err
	}
	claim, err := env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: req.OperationID, Verb: "adopt", Scope: map[string]string{
		"mail_project_key": req.ProjectKey, "project_id": req.ProjectID}, Request: req}, env.now())
	if err != nil {
		return AdoptResult{}, err
	}
	opID := claim.Operation.ID
	if claim.Existing {
		return AdoptResult{}, fmt.Errorf("bindings: operation %s exists (status %s); nothing was done again", opID, claim.Operation.Status)
	}
	res, err := adopt(ctx, env, req, source, alias, opID)
	status := "done"
	switch {
	case errors.Is(err, ErrAgentChanged):
		// Not the reviewed agent: refused before anything was bound, as
		// nhm adopt's exit 2 says.
		status = "refused"
	case err != nil:
		status = "failed"
	}
	if serr := env.DB.SetOperationStatus(ctx, opID, status, true, env.now()); serr != nil && err == nil {
		res.Warnings = append(res.Warnings, serr.Error())
	}
	res.OperationID = opID
	return res, err
}

func adopt(ctx context.Context, env Env, req AdoptRequest, source Binding, alias, opID string) (AdoptResult, error) {
	res := AdoptResult{Warnings: []string{}}
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return res, fmt.Errorf("bindings: adopt: %w", err)
	}
	pane, ok := findPane(snap, req.PaneID)
	if !ok {
		return res, fmt.Errorf("bindings: pane %s is not in herdr's session", req.PaneID)
	}
	agent, ok := findAgent(snap, req.PaneID)
	if !ok || agent.Agent == "" {
		return res, fmt.Errorf("%w: %s", ErrNoAgent, req.PaneID)
	}
	// The session first: another agent in the pane since the review, of
	// any kind, is the reviewed agent changed (refused).
	if got := SessionKey(agent.AgentSession); req.ExpectedSession != "" && got != req.ExpectedSession {
		if got == "" {
			got = "none"
		}
		return res, fmt.Errorf("%w: the agent in %s has session %s, the review saw %s", ErrAgentChanged, req.PaneID, got, req.ExpectedSession)
	}
	kind := agent.Agent
	if req.Kind != "" && req.Kind != agent.Agent {
		return res, fmt.Errorf("bindings: herdr sees a %s agent in %s, not %s", agent.Agent, req.PaneID, req.Kind)
	}
	occ := occupantOf(ctx, env.Herdr, pane, agent, alias, req.Model)
	occ.Kind = kind
	occ.Evidence = append([]Evidence{{Kind: "adopt", Detail: "bound by hand, operation " + opID}}, occ.Evidence...)

	if source.ID != "" {
		// One owner of the identity at every moment (plan-v8 2.10): the old
		// occupant must be gone before another pane takes the identity.
		if source.Status == Active && source.PaneID != "" && source.PaneID != req.PaneID {
			if old, ok := findAgent(snap, source.PaneID); ok && old.Agent != "" {
				return res, fmt.Errorf("bindings: %s still has a %s agent in %s; stop it or mark the binding displaced first",
					source.MailName, old.Agent, source.PaneID)
			}
		}
		res.Binding, err = env.Bindings.Occupy(ctx, source.ID, source.Generation, occ)
		if err != nil {
			return res, err
		}
	} else {
		open, err := env.Bindings.List(ctx, Filter{ProjectKey: req.ProjectKey, MailName: req.MailName, Statuses: []Status{Active, Displaced}})
		if err != nil {
			return res, err
		}
		if len(open) > 0 {
			return res, fmt.Errorf("%w: %s is %s in %s as %s; hand it over with --handoff-from %s",
				ErrIdentityBusy, req.MailName, open[0].Status, orNone(open[0].PaneID), open[0].ID, open[0].ID)
		}
		res.Binding, err = env.Bindings.Create(ctx, New{MailName: req.MailName, MailProjectKey: req.ProjectKey,
			ProjectID: req.ProjectID, Provenance: Adopted, CreatedByOp: opID, Occupant: occ})
		if err != nil {
			return res, err
		}
		res.Created = true
	}
	res.Warnings = append(res.Warnings, decorate(ctx, env, res.Binding, agent)...)
	res.Warnings = append(res.Warnings, Label(ctx, env.Herdr, res.Binding)...)
	path, warn := writeIdentity(env, res.Binding)
	res.IdentityPath = path
	res.Warnings = appendIf(res.Warnings, warn)
	if source.PaneID != "" && source.PaneID != res.Binding.PaneID {
		if _, err := agentmail.RemoveIdentity(env.ConfigHome, source.MailProjectKey, source.PaneID, source.MailName); err != nil {
			res.Warnings = append(res.Warnings, "old identity file: "+err.Error())
		}
	}
	return res, nil
}

// occupantOf collects the facts of the agent in a pane.
func occupantOf(ctx context.Context, h Herdr, pane herdr.PaneInfo, agent herdr.AgentInfo, alias, model string) Occupant {
	occ := Occupant{WorkspaceID: pane.WorkspaceID, TabID: pane.TabID, PaneID: pane.PaneID, HerdrAlias: alias,
		Kind: agent.Agent, Model: model}
	occ.Evidence = []Evidence{{Kind: "herdr_agent", Detail: fmt.Sprintf("%s agent in %s", agent.Agent, pane.PaneID)}}
	if s := agent.AgentSession; s != nil {
		occ.NativeSession = &NativeSession{Source: s.Source, Agent: s.Agent, Kind: s.Kind, Value: s.Value}
		occ.Evidence = append(occ.Evidence, Evidence{Kind: "native_session", Detail: s.Source + ": " + s.Value})
	}
	occ.ForegroundPID, occ.ForegroundStart = processFacts(ctx, h, pane.PaneID)
	return occ
}

// decorate gives the pane's agent its herdr alias; a failure is a
// warning, since the binding holds either way and the hooks apply the
// name again.
func decorate(ctx context.Context, env Env, b Binding, agent herdr.AgentInfo) []string {
	if b.HerdrAlias == "" || agent.Name == b.HerdrAlias {
		return nil
	}
	if _, err := env.Herdr.AgentRename(ctx, b.PaneID, b.HerdrAlias); err != nil {
		return []string{fmt.Sprintf("herdr name %s not applied to %s: %v", b.HerdrAlias, b.PaneID, err)}
	}
	return nil
}

// Label names a new binding's pane "<Name> <model>", and its tab
// "<Name>" when the tab holds only that pane, as spawn does. Adopt and
// the hooks' bind call it. Failures come back as warnings: the binding
// holds either way.
func Label(ctx context.Context, h Labeler, b Binding) []string {
	var warnings []string
	label := b.MailName
	if b.Model != "" {
		label += " " + b.Model
	}
	if _, err := h.PaneRename(ctx, b.PaneID, label); err != nil {
		warnings = append(warnings, "pane label: "+err.Error())
	}
	if b.TabID == "" {
		return warnings
	}
	tab, err := h.TabGet(ctx, b.TabID)
	if err != nil {
		return append(warnings, "tab label: "+err.Error())
	}
	if tab.PaneCount != 1 || tab.Label == b.MailName {
		return warnings
	}
	if _, err := h.TabRename(ctx, tab.TabID, b.MailName); err != nil {
		warnings = append(warnings, "tab label: "+err.Error())
	}
	return warnings
}

func writeIdentity(env Env, b Binding) (string, string) {
	path, err := agentmail.WriteIdentity(env.ConfigHome, b.MailProjectKey, b.PaneID, b.MailName, env.now())
	if err != nil {
		return "", "Agent Mail identity file: " + err.Error()
	}
	return path, ""
}

func appendIf(list []string, s string) []string {
	if s == "" {
		return list
	}
	return append(list, s)
}

func orNone(s string) string {
	if s == "" {
		return "no pane"
	}
	return s
}

// BashTokenFile is where ACFS's Bash `acfs agents spawn` keeps an
// identity's registration token: $ACFS_HOME/state/tokens/<the first 16
// hex digits of the SHA-256 of the project's physical path>/<Name>.
func BashTokenFile(acfsHome, projectRoot, name string) string {
	sum := sha256.Sum256([]byte(projectRoot))
	return filepath.Join(acfsHome, "state", "tokens", hex.EncodeToString(sum[:])[:16], name)
}

// DefaultAcfsHome is $ACFS_HOME, or ~/.acfs.
func DefaultAcfsHome() string {
	if h := os.Getenv("ACFS_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".acfs")
}
