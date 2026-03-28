package reflow

import "context"

// Node is the core interface. Each node has three phases:
//
//   - Resolve: inspect the envelope, read hints, derive local context.
//   - Act: do the work — transform data, call tools, run inference.
//   - Settle: turn the outcome into a stable handoff for the next node.
//
// Implement this interface as a named type for reusable, testable nodes.
// Use Func for quick inline definitions.
type Node[I, O any] interface {
	Resolve(context.Context, Envelope[I]) (Envelope[I], error)
	Act(context.Context, Envelope[I]) (Envelope[O], error)
	Settle(ctx context.Context, in Envelope[I], out Envelope[O], actErr error) (Envelope[O], bool, error)
}

// Func is a convenience adapter for building nodes from closures.
// Nil fields get sensible defaults: nil Resolve passes through,
// nil Settle accepts if Act succeeded.
//
//	double := &reflow.Func[int, int]{
//	    ActFn: reflow.Pass(func(n int) int { return n * 2 }),
//	}
type Func[I, O any] struct {
	ResolveFn func(context.Context, Envelope[I]) (Envelope[I], error)
	ActFn     func(context.Context, Envelope[I]) (Envelope[O], error)
	SettleFn  func(context.Context, Envelope[I], Envelope[O], error) (Envelope[O], bool, error)
}

func (f *Func[I, O]) Resolve(ctx context.Context, in Envelope[I]) (Envelope[I], error) {
	if f.ResolveFn == nil {
		return in, nil
	}
	return f.ResolveFn(ctx, in)
}

func (f *Func[I, O]) Act(ctx context.Context, in Envelope[I]) (Envelope[O], error) {
	if f.ActFn != nil {
		return f.ActFn(ctx, in)
	}
	var zero O
	return Envelope[O]{Value: zero, Meta: in.Meta}, nil
}

func (f *Func[I, O]) Settle(ctx context.Context, in Envelope[I], out Envelope[O], actErr error) (Envelope[O], bool, error) {
	if f.SettleFn != nil {
		return f.SettleFn(ctx, in, out, actErr)
	}
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}
