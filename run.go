package reflow

import (
	"context"
	"fmt"
)

// Runner is an optional interface a Node can implement to control its
// own execution. WithRetry returns a Runner. If a Node is also a Runner,
// Run delegates to it.
type Runner[I, O any] interface {
	Node[I, O]
	Run(context.Context, Envelope[I]) (Envelope[O], error)
}

// Run executes a node. If the node implements Runner (e.g. from WithRetry),
// it delegates to the node's Run method. Otherwise it does a single pass
// of resolve → act → settle.
func Run[I, O any](ctx context.Context, n Node[I, O], in Envelope[I]) (Envelope[O], error) {
	if r, ok := n.(Runner[I, O]); ok {
		return r.Run(ctx, in)
	}
	return runOnce(ctx, n, in)
}

func runOnce[I, O any](ctx context.Context, n Node[I, O], in Envelope[I]) (Envelope[O], error) {
	resolved, err := n.Resolve(ctx, in)
	if err != nil {
		return Envelope[O]{}, fmt.Errorf("reflow: resolve: %w", err)
	}
	resolved.Meta.Trace = append(cloneTrace(resolved.Meta.Trace), Step{Phase: "resolve", Status: "ok"})

	out, actErr := n.Act(ctx, resolved)

	settled, done, err := n.Settle(ctx, resolved, out, actErr)
	if err != nil {
		return Envelope[O]{}, fmt.Errorf("reflow: settle: %w", err)
	}
	if !done {
		if actErr != nil {
			return Envelope[O]{}, fmt.Errorf("reflow: act: %w", actErr)
		}
		return Envelope[O]{}, fmt.Errorf("reflow: did not settle")
	}
	settled.Meta.Trace = append(cloneTrace(settled.Meta.Trace), Step{Phase: "settle", Status: "ok"})
	return settled, nil
}

// WithRetry wraps a node with a settle loop. On each iteration, if Settle
// returns done=false, the output envelope's hints are merged back into the
// input and the loop retries from Resolve.
//
//	retrying := reflow.WithRetry(classifier, 5)
func WithRetry[I, O any](n Node[I, O], maxIter int) Runner[I, O] {
	if maxIter <= 0 {
		maxIter = 1
	}
	return &retryNode[I, O]{inner: n, maxIter: maxIter}
}

type retryNode[I, O any] struct {
	inner   Node[I, O]
	maxIter int
}

func (r *retryNode[I, O]) Resolve(ctx context.Context, in Envelope[I]) (Envelope[I], error) {
	return r.inner.Resolve(ctx, in)
}

func (r *retryNode[I, O]) Act(ctx context.Context, in Envelope[I]) (Envelope[O], error) {
	return r.inner.Act(ctx, in)
}

func (r *retryNode[I, O]) Settle(ctx context.Context, in Envelope[I], out Envelope[O], actErr error) (Envelope[O], bool, error) {
	return r.inner.Settle(ctx, in, out, actErr)
}

func (r *retryNode[I, O]) Run(ctx context.Context, in Envelope[I]) (Envelope[O], error) {
	// Accumulate trace across iterations so the final output has the full history.
	var trace []Step

	for i := range r.maxIter {
		priorHints := len(in.Meta.Hints)

		resolved, err := r.inner.Resolve(ctx, in)
		if err != nil {
			return Envelope[O]{}, fmt.Errorf("reflow: resolve (iter %d): %w", i, err)
		}
		trace = append(trace, Step{Phase: "resolve", Status: "ok"})

		out, actErr := r.inner.Act(ctx, resolved)

		settled, done, err := r.inner.Settle(ctx, resolved, out, actErr)
		if err != nil {
			return Envelope[O]{}, fmt.Errorf("reflow: settle (iter %d): %w", i, err)
		}
		trace = append(trace, Step{Phase: "settle", Status: settleStatus(done)})

		if done {
			settled.Meta.Trace = append(cloneTrace(in.Meta.Trace), trace...)
			return settled, nil
		}

		// Only feed back hints added during this iteration, not inherited ones.
		// Hints flow through Resolve → Act → Settle via meta propagation,
		// so settled.Meta.Hints[:priorHints] are the input hints carried forward.
		// Everything beyond that was added by this iteration's phases.
		if len(settled.Meta.Hints) > priorHints {
			in.Meta.Hints = append(in.Meta.Hints, settled.Meta.Hints[priorHints:]...)
		}
	}
	return Envelope[O]{}, fmt.Errorf("reflow: did not settle after %d iterations", r.maxIter)
}

func settleStatus(done bool) string {
	if done {
		return "ok"
	}
	return "retry"
}
