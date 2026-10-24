package send

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/templates"
	"github.com/arosl/nhm/internal/robot"
)

// enrichTargets runs the send's retrieval and duplicate check under one
// budget, then frames each target's payload with the items its context
// does not have yet. A required source that failed refuses the send
// before the claim; a best-effort one is a warning. It returns one
// exposure per target, to record once the operation is claimed.
func enrichTargets(ctx context.Context, env Env, req Request, tmpl *templates.Template, shared string,
	targets []Target, payloads []string, res *Result) ([]enrich.Exposure, error) {
	cmMode, cassMode, err := req.Enrich.resolve(tmpl)
	if err != nil {
		return nil, err
	}
	on := cmMode != enrich.Off || cassMode != enrich.Off
	if req.Prepared != nil {
		if on || req.Enrich.Check {
			return nil, errors.New("a send with a prepared enrichment runs no enrichment of its own")
		}
		return preparedExposure(*req.Prepared, targets, res)
	}
	check := req.Enrich.Check && !req.Force
	if !on && !check {
		return nil, nil
	}
	query := req.Enrich.Query
	if query == "" {
		query = enrich.QueryOf(shared)
	}
	days := req.Enrich.CheckDays
	if days <= 0 {
		days = enrich.DefaultCheckDays
	}
	var retr enrich.Retrieval
	var dups []enrich.Similar
	var dupErr error
	var wg sync.WaitGroup
	if on {
		r := enrich.NewRetriever(env.CM, env.CASS, env.Budgets, env.now())
		wg.Add(1)
		go func() {
			defer wg.Done()
			retr = r.Retrieve(ctx, enrich.Request{Query: query, Workspace: req.ProjectRoot, CM: cmMode, CASS: cassMode,
				CASSQuery: req.Enrich.CASSQuery, CASSDays: req.Enrich.CASSDays, CASSLimit: req.Enrich.CASSLimit})
		}()
	}
	if check {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dups, dupErr = enrich.CheckDuplicates(ctx, env.CASS, query, req.ProjectRoot, days, env.Budgets.Total)
		}()
	}
	wg.Wait()

	switch {
	case dupErr != nil:
		res.Warnings = append(res.Warnings, "cass-check skipped: "+dupErr.Error())
	case len(dups) > 0:
		res.Duplicates = dups
		res.Warnings = append(res.Warnings, enrich.DuplicateWarning(dups, days))
	}
	if !on {
		return nil, nil
	}
	if retr.CM.Enabled {
		res.MemoryInjection = retr.CM
	}
	if retr.CASS.Enabled {
		res.CASSInjection = retr.CASS
	}
	if err := retr.Err(); err != nil {
		return nil, err
	}
	for _, rep := range []enrich.Report{retr.CM, retr.CASS} {
		if rep.Error != "" {
			res.Warnings = append(res.Warnings, "best-effort enrichment failed: "+rep.Error)
		}
	}
	block := env.Budgets.BlockBytes
	ex := make([]enrich.Exposure, 0, len(targets))
	for i, t := range targets {
		cg := enrich.ContextGeneration(t.Binding)
		given, err := enrich.Given(ctx, env.DB, t.Binding.ID, cg)
		if err != nil {
			return nil, err
		}
		plan := enrich.PlanFor(retr, given, block)
		payloads[i] = enrich.Frame(plan.Block, payloads[i])
		ex = append(ex, enrich.Exposure{TargetIndex: i, BindingID: t.Binding.ID, Generation: t.Binding.Generation,
			ContextGeneration: cg, BeadID: req.BeadID, Plan: plan, Retrieval: retr})
		if retr.CM.Enabled {
			res.Injections = append(res.Injections, enrich.TargetInjection{TargetIndex: i, Source: enrich.SourceCM, Items: plan.CM})
		}
		if retr.CASS.Enabled {
			res.Injections = append(res.Injections, enrich.TargetInjection{TargetIndex: i, Source: enrich.SourceCASS, Items: plan.CASS})
		}
	}
	return ex, nil
}

// preparedExposure checks a prepared enrichment against the send's one
// target and reports it as the send's injection. The context generation is
// the target's now: what the receipt says the agent got is in the context
// it holds at delivery.
func preparedExposure(p enrich.Exposure, targets []Target, res *Result) ([]enrich.Exposure, error) {
	if len(targets) != 1 {
		return nil, fmt.Errorf("a prepared enrichment is for one target, not %d", len(targets))
	}
	b := targets[0].Binding
	if b.ID != p.BindingID || b.Generation != p.Generation {
		return nil, fmt.Errorf("the enrichment was prepared for %s@%d, not %s@%d", p.BindingID, p.Generation, b.ID, b.Generation)
	}
	p.TargetIndex, p.ContextGeneration = 0, enrich.ContextGeneration(b)
	if p.Retrieval.CM.Enabled {
		res.MemoryInjection = p.Retrieval.CM
		res.Injections = append(res.Injections, enrich.TargetInjection{TargetIndex: 0, Source: enrich.SourceCM, Items: p.Plan.CM})
	}
	if p.Retrieval.CASS.Enabled {
		res.CASSInjection = p.Retrieval.CASS
		res.Injections = append(res.Injections, enrich.TargetInjection{TargetIndex: 0, Source: enrich.SourceCASS, Items: p.Plan.CASS})
	}
	return []enrich.Exposure{p}, nil
}

// skipReason is a report's reason for ntm's skipped_reason.
func skipReason(r Injection) string {
	if r.Error != "" {
		return r.Error
	}
	return r.SkippedReason
}

// injected sums one source's included items over the targets: the
// distinct ids and the bytes.
func injected(res Result, source string) (ids []string, bytes int) {
	seen := map[string]bool{}
	for _, ti := range res.Injections {
		if ti.Source != source {
			continue
		}
		for _, it := range ti.Items {
			if it.Status != enrich.Included {
				continue
			}
			bytes += it.Bytes
			if !seen[it.ID] {
				seen[it.ID] = true
				ids = append(ids, it.ID)
			}
		}
	}
	return ids, bytes
}

// memoryInfo and cassInfo project the injection onto ntm's robot types;
// tokens are bytes/4, as ntm estimates them.
func memoryInfo(res Result) *robot.CMInjectionInfo {
	ids, bytes := injected(res, enrich.SourceCM)
	return &robot.CMInjectionInfo{Enabled: res.MemoryInjection.Enabled, RulesInjected: ids, TokensAdded: bytes / 4,
		SkippedReason: skipReason(res.MemoryInjection)}
}

func cassInfo(res Result) *robot.CASSInjectionInfo {
	ids, bytes := injected(res, enrich.SourceCASS)
	return &robot.CASSInjectionInfo{Enabled: res.CASSInjection.Enabled, ItemsFound: len(res.CASSInjection.Items),
		ItemsInjected: len(ids), TokensAdded: bytes / 4, SkippedReason: skipReason(res.CASSInjection)}
}

// redactionInfo is ntm's redaction summary: nhm redacts what it puts in
// from CM and CASS, never the operator's own text. "off" when neither
// source ran.
func redactionInfo(res Result) robot.RedactionSummary {
	if !res.MemoryInjection.Enabled && !res.CASSInjection.Enabled {
		return robot.RedactionSummary{Mode: "off"}
	}
	n := 0
	for _, it := range append(append([]enrich.Item{}, res.MemoryInjection.Items...), res.CASSInjection.Items...) {
		n += it.Redacted
	}
	return robot.RedactionSummary{Mode: "redact", Findings: n}
}

// reportOf is the report kept with the operation, or nil for a source
// that is off.
func reportOf(r Injection) *Injection {
	if !r.Enabled {
		return nil
	}
	return &r
}
