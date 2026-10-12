package send

import (
	"context"
	"time"

	"github.com/arosl/nhm/internal/core/wait"
)

// WaitTargets turns an operation's receipts into wait targets: the
// targets that a prompt may have reached, with their baselines.
func WaitTargets(ctx context.Context, env Env, receipts []Receipt) ([]wait.Target, []int, error) {
	var targets []wait.Target
	var index []int
	for _, rc := range receipts {
		if rc.BindingID == "" || (rc.Status != StatusSubmitted && !Unknown(rc.Status)) || rc.Baseline == nil {
			continue
		}
		b, err := env.Bindings.Get(ctx, rc.BindingID)
		if err != nil {
			return nil, nil, err
		}
		targets = append(targets, wait.Target{Binding: b, Baseline: rc.Baseline, Submission: rc.Status})
		index = append(index, rc.TargetIndex)
	}
	return targets, index, nil
}

// Observe runs 2.6's wait for an operation's targets and records each
// completion observation in its receipt. ctx's deadline is the timeout.
func Observe(ctx context.Context, env Env, wenv wait.Env, opID string, until wait.Until, poll time.Duration) ([]wait.Result, error) {
	receipts, err := Receipts(ctx, env.DB, opID)
	if err != nil {
		return nil, err
	}
	targets, index, err := WaitTargets(ctx, env, receipts)
	if err != nil || len(targets) == 0 {
		return nil, err
	}
	results := wait.All(ctx, wenv, targets, wait.Request{Until: until, AfterOp: true, Poll: poll}, 0)
	// The receipts are written after the wait, so the deadline that ended
	// the wait does not cancel the write.
	wctx := context.WithoutCancel(ctx)
	for i, r := range results {
		if err := RecordCompletion(wctx, env.DB, opID, index[i], Observation(r), env.now()); err != nil {
			return results, err
		}
	}
	return results, nil
}
