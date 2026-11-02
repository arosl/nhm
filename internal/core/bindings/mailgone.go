package bindings

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

// A binding is the pane of a live Agent Mail identity. When the identity
// is retired (retire_agent, am agents reap) or deregistered
// (deregister_agent), it no longer sends or receives mail, and nhm must
// not send to its pane either: a person's own session that deregistered
// would keep getting nudges (nhm-h85k). nhm maintain checks the active
// and displaced bindings against Agent Mail and retires the binding of
// an identity that is retired or deregistered. The agent in the pane
// runs on; nhm
// removes the pane's identity file, which names the gone identity, and
// its own copy of the registration token. If the agent registers again,
// the registration hook binds it again.
//
// Agent Mail's list_agents lists the active identities of a project, the
// most recently active first; whois confirms each bound name that the
// list leaves out. A name that whois cannot find, or finds active, stays
// bound.

// IdentityGoneGrace is how long an identity has been retired or
// deregistered before nhm maintain retires its binding. nhm retire
// retires the identity and then the binding, in milliseconds; the grace
// keeps the two from racing.
const IdentityGoneGrace = time.Minute

// listAgentsLimit is list_agents' own cap.
const listAgentsLimit = 250

// GoneIdentity is a binding that nhm maintain retired because its Agent
// Mail identity is gone.
type GoneIdentity struct {
	BindingID  string    `json:"binding_id"`
	MailName   string    `json:"mail_name"`
	ProjectKey string    `json:"mail_project_key"`
	PaneID     string    `json:"pane_id,omitempty"`
	State      string    `json:"state"` // retired or deregistered
	Since      time.Time `json:"since,omitempty"`
}

// Agent Mail identity states that retire a binding.
const (
	IdentityRetired      = "retired"
	IdentityDeregistered = "deregistered"
)

// ReleaseGoneIdentities retires each active or displaced binding of open
// whose identity Agent Mail reports retired or deregistered for
// IdentityGoneGrace or longer. What it cannot read or change is a
// warning; the next pass tries again. The caller holds the owner lock.
func ReleaseGoneIdentities(ctx context.Context, env Env, mail agentmail.Caller, open []Binding) ([]GoneIdentity, []string) {
	gone, warnings := []GoneIdentity{}, []string{}
	byProject := map[string][]Binding{}
	for _, b := range open {
		if b.Status == Active || b.Status == Displaced {
			byProject[b.MailProjectKey] = append(byProject[b.MailProjectKey], b)
		}
	}
	projects := make([]string, 0, len(byProject))
	for p := range byProject {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	for _, project := range projects {
		var listed []struct {
			Name string `json:"name"`
		}
		if err := mail.Call(ctx, "list_agents", map[string]any{"project_key": project, "limit": listAgentsLimit}, &listed); err != nil {
			warnings = append(warnings, fmt.Sprintf("Agent Mail identities of %s: %v", project, err))
			continue
		}
		live := map[string]bool{}
		for _, a := range listed {
			live[strings.ToLower(a.Name)] = true
		}
		for _, b := range byProject[project] {
			if live[strings.ToLower(b.MailName)] {
				continue
			}
			state, since, err := identityState(ctx, mail, project, b.MailName)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("Agent Mail identity %s: %v", b.MailName, err))
				continue
			}
			if state == "" || (!since.IsZero() && env.now().Sub(since) < IdentityGoneGrace) {
				continue
			}
			if _, err := env.Bindings.Retire(ctx, b.ID, b.Generation, "the Agent Mail identity was "+state); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: its Agent Mail identity was %s; the binding stays: %v", b.MailName, state, err))
				continue
			}
			if b.PaneID != "" {
				if _, err := agentmail.RemoveIdentity(env.ConfigHome, b.MailProjectKey, b.PaneID, b.MailName); err != nil {
					warnings = append(warnings, fmt.Sprintf("%s: Agent Mail identity file: %v", b.MailName, err))
				}
			}
			_ = store.DeleteSecret(env.InstanceDir, b.ID)
			gone = append(gone, GoneIdentity{BindingID: b.ID, MailName: b.MailName, ProjectKey: b.MailProjectKey,
				PaneID: b.PaneID, State: state, Since: since})
		}
	}
	return gone, warnings
}

// identityState is whois's word on an identity: IdentityDeregistered or
// IdentityRetired with the time, or "" for active. A time Agent Mail
// gives in a form nhm cannot read is the zero time: the state counts at
// once.
func identityState(ctx context.Context, mail agentmail.Caller, project, name string) (string, time.Time, error) {
	var who struct {
		RetiredAt      *string `json:"retired_at"`
		DeregisteredAt *string `json:"deregistered_at"`
	}
	if err := mail.Call(ctx, "whois", map[string]any{"project_key": project, "agent_name": name,
		"include_recent_commits": false}, &who); err != nil {
		return "", time.Time{}, err
	}
	at := func(s *string) (bool, time.Time) {
		if s == nil || strings.TrimSpace(*s) == "" {
			return false, time.Time{}
		}
		t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*s))
		if err != nil {
			return true, time.Time{}
		}
		return true, t
	}
	if ok, t := at(who.DeregisteredAt); ok {
		return IdentityDeregistered, t, nil
	}
	if ok, t := at(who.RetiredAt); ok {
		return IdentityRetired, t, nil
	}
	return "", time.Time{}, nil
}
