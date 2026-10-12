package snapshot

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/guards"
)

// Attention severities.
const (
	SevInfo     = "info"
	SevWarning  = "warning"
	SevCritical = "critical"
)

// Attention kinds: what needs a decision (plan-v8 2.5).
const (
	AttnBlocked           = "agent_blocked"
	AttnDone              = "agent_done"
	AttnUnread            = "mail_unread"
	AttnAckOverdue        = "mail_ack_overdue"
	AttnPossiblySubmitted = "send_possibly_submitted"
	AttnOpenOperation     = "operation_unresolved"
	AttnAbsent            = "binding_absent"
	AttnDisplaced         = "binding_displaced"
	AttnNameMismatch      = "name_mismatch"
	AttnUnbound           = "agent_unbound"
	AttnQuotaLimited      = "quota_limited"
	AttnQuotaStale        = "quota_stale"
	AttnWakePending       = "wake_pending"
	AttnSourceDegraded    = "source_degraded"
	AttnUnguarded         = "agent_unguarded"
	AttnAtLimit           = "agent_at_limit"
)

// openOperationGrace is how long an operation may run before attention
// names it.
const openOperationGrace = 5 * time.Minute

// Item is one thing that needs a decision.
type Item struct {
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	PaneID      string `json:"pane_id,omitempty"`
	Slot        string `json:"slot,omitempty"`
	MailName    string `json:"mail_name,omitempty"`
	BindingID   string `json:"binding_id,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	Detail      string `json:"detail"`
}

// Attention lists what needs a decision, most severe first.
func Attention(m *Model) []Item {
	items := []Item{}
	add := func(it Item) { items = append(items, it) }
	for _, name := range m.Degraded() {
		h := m.Sources[name]
		add(Item{Kind: AttnSourceDegraded, Severity: SevWarning, Detail: fmt.Sprintf("%s is %s: %s", name, h.Status, h.Error)})
	}
	for _, a := range m.Agents {
		base := Item{WorkspaceID: a.WorkspaceID, PaneID: a.PaneID, Slot: a.Slot}
		if a.Binding != nil {
			base.MailName, base.BindingID = a.Binding.MailName, a.Binding.ID
		}
		who := agentLabel(a)
		switch a.State {
		case herdr.StatusBlocked:
			it := base
			it.Kind, it.Severity, it.Detail = AttnBlocked, SevCritical, who+" waits for an answer (a prompt or a dialog)"
			add(it)
		case herdr.StatusDone:
			it := base
			it.Kind, it.Severity, it.Detail = AttnDone, SevInfo, who+" finished a turn that nobody has looked at"
			add(it)
		}
		if a.Binding == nil {
			it := base
			it.Kind, it.Severity, it.Detail = AttnUnbound, SevInfo, who+" has no binding; adopt it or import it"
			add(it)
		} else if !a.Binding.NameMatches {
			it := base
			it.Kind, it.Severity = AttnNameMismatch, SevWarning
			it.Detail = fmt.Sprintf("herdr shows %q, the binding has %q", a.HerdrName, a.Binding.Alias)
			add(it)
		}
		var unguarded []string
		for _, g := range guards.Guards {
			if st, ok := a.Guards[g]; ok && (st == guards.Missing || st == guards.Untrusted || st == guards.Stale) {
				unguarded = append(unguarded, g+" "+string(st))
			}
		}
		if len(unguarded) > 0 {
			it := base
			it.Kind, it.Severity = AttnUnguarded, SevWarning
			it.Detail = fmt.Sprintf("%s runs without its guards: %s (nhm doctor says how to fix)", who, strings.Join(unguarded, ", "))
			add(it)
		}
		if a.Limit != "" {
			it := base
			it.Kind, it.Severity = AttnAtLimit, SevWarning
			it.Detail = fmt.Sprintf("%s stopped at a limit: %s", who, a.Limit)
			add(it)
		}
		if a.Mail != nil && a.Mail.Status == Fresh {
			if a.Mail.OverdueAcks > 0 {
				it := base
				it.Kind, it.Severity, it.Detail = AttnAckOverdue, SevWarning, fmt.Sprintf("%s owes %d acks past 30 min", who, a.Mail.OverdueAcks)
				add(it)
			}
			if a.Mail.Unread > 0 {
				it := base
				sev := SevInfo
				if a.Mail.Urgent > 0 {
					sev = SevWarning
				}
				it.Kind, it.Severity = AttnUnread, sev
				it.Detail = fmt.Sprintf("%s has %d unread (%d urgent)", who, a.Mail.Unread, a.Mail.Urgent)
				add(it)
			}
		}
	}
	for _, r := range m.Uncertain {
		add(Item{Kind: AttnPossiblySubmitted, Severity: SevCritical, OperationID: r.OperationID, BindingID: r.BindingID,
			Detail: "a send may have reached its target; read the pane before you send again"})
	}
	for _, op := range m.Operations {
		if m.GeneratedAt.Sub(op.CreatedAt) > openOperationGrace {
			add(Item{Kind: AttnOpenOperation, Severity: SevWarning, OperationID: op.ID,
				Detail: fmt.Sprintf("%s has been %s since %s", op.Verb, op.Status, op.CreatedAt.UTC().Format(time.RFC3339))})
		}
	}
	for _, b := range m.Absent {
		add(Item{Kind: AttnAbsent, Severity: SevWarning, WorkspaceID: b.WorkspaceID, PaneID: b.PaneID, Slot: b.Slot,
			MailName: b.MailName, BindingID: b.ID, Detail: b.MailName + "'s pane holds no agent"})
	}
	for _, b := range m.Displaced {
		add(Item{Kind: AttnDisplaced, Severity: SevWarning, WorkspaceID: b.WorkspaceID, PaneID: b.PaneID, Slot: b.Slot,
			MailName: b.MailName, BindingID: b.ID, Detail: fmt.Sprintf("%s is displaced (%s); nhm adopt can bind it again", b.MailName, b.StatusReason)})
	}
	for _, q := range m.Quota {
		switch {
		case q.Limited:
			add(Item{Kind: AttnQuotaLimited, Severity: SevWarning, Detail: fmt.Sprintf("%s %s is at its limit until %s", q.PoolID, q.Window, q.ResetsAt)})
		case q.Stale:
			add(Item{Kind: AttnQuotaStale, Severity: SevInfo, Detail: fmt.Sprintf("%s %s was last seen %s", q.PoolID, q.Window, q.ObservedAt.UTC().Format(time.RFC3339))})
		}
	}
	for _, w := range m.Wakes {
		add(Item{Kind: AttnWakePending, Severity: SevInfo, BindingID: w.BindingID,
			Detail: fmt.Sprintf("message %s waits for a wake (after %s, %d attempts)", w.MessageID, w.WakeAfter, w.Attempts)})
	}
	rank := map[string]int{SevCritical: 0, SevWarning: 1, SevInfo: 2}
	sort.SliceStable(items, func(i, j int) bool { return rank[items[i].Severity] < rank[items[j].Severity] })
	return items
}

// CountBySeverity counts attention items per severity.
func CountBySeverity(items []Item) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		out[it.Severity]++
	}
	return out
}

func agentLabel(a Agent) string {
	switch {
	case a.Binding != nil:
		return a.Binding.MailName
	case a.HerdrName != "":
		return a.HerdrName
	default:
		return a.Kind + " in " + a.PaneID
	}
}
