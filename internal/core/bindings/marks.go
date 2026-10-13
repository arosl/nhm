package bindings

import (
	"context"

	"github.com/arosl/nhm/internal/herdr"
)

// Marker is what Mark and KeepMarks need from herdr; *herdr.Client has
// it.
type Marker interface {
	PaneRename(ctx context.Context, paneID, label string) (herdr.PaneInfo, error)
	PaneReportMetadata(ctx context.Context, p herdr.ReportMetadataParams) error
}

// A bound agent's pane carries two marks: the label "<Name> <model>"
// and the sidebar tokens mail and model, which nhm reports as source
// "nhm". herdr keeps both while the agent exits and another starts in
// the pane (nhm-hae, probed on herdr 0.9.3), and merges a source's
// tokens by name, so a report of mail and model keeps the group token
// that spawn adds.

// PaneLabel is the label of a binding's pane: "<Name> <model>", or the
// name alone when the binding has no model.
func PaneLabel(b Binding) string {
	if b.Model == "" {
		return b.MailName
	}
	return b.MailName + " " + b.Model
}

// MarkTokens are the sidebar tokens of a binding's pane: mail, and
// model when the binding has one.
func MarkTokens(b Binding) map[string]string {
	tokens := map[string]string{"mail": b.MailName}
	if b.Model != "" {
		tokens["model"] = b.Model
	}
	return tokens
}

// Mark gives a binding's pane both marks, as spawn does. Adopt and the
// hooks' bind call it, through Label. It returns the marks it applied
// ("pane label", "sidebar tokens") and what failed; a failure is a
// warning, since the binding holds either way and the reconcile tries
// again (KeepMarks).
func Mark(ctx context.Context, h Marker, b Binding) (done, warnings []string) {
	return applyMarks(ctx, h, b, true, true)
}

// KeepMarks gives a binding's pane the marks that p, the pane as herdr
// shows it, lacks: the label when the pane has none, and the tokens when
// one of them differs. A spawn that stopped at a dialog never set them,
// herdr drops the tokens when it restarts, and a user can clear them
// (nhm-hae). A label that someone else set stays, as keepTabLabel leaves
// a tab's. The hooks' reconcile, recycle and recover call it. It returns
// what Mark returns.
func KeepMarks(ctx context.Context, h Marker, b Binding, p herdr.PaneInfo) (done, warnings []string) {
	tokens := false
	for k, v := range MarkTokens(b) {
		if p.Tokens[k] != v {
			tokens = true
		}
	}
	return applyMarks(ctx, h, b, p.Label == "", tokens)
}

func applyMarks(ctx context.Context, h Marker, b Binding, label, tokens bool) (done, warnings []string) {
	if label {
		if _, err := h.PaneRename(ctx, b.PaneID, PaneLabel(b)); err != nil {
			warnings = append(warnings, "pane label: "+err.Error())
		} else {
			done = append(done, "pane label")
		}
	}
	if tokens {
		p := herdr.ReportMetadataParams{PaneID: b.PaneID, Source: "nhm", Agent: b.Kind, Tokens: MarkTokens(b)}
		if err := h.PaneReportMetadata(ctx, p); err != nil {
			warnings = append(warnings, "sidebar tokens: "+err.Error())
		} else {
			done = append(done, "sidebar tokens")
		}
	}
	return done, warnings
}
