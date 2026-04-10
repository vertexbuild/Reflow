package reflow

import (
	"context"
	"fmt"
	"iter"
)

// StreamNode is a node whose Act phase yields a sequence of envelopes
// instead of a single result. Each yielded item is settled individually
// before being emitted downstream.
//
// This enables pull-based streaming: downstream controls the pace,
// and backpressure is automatic — if the caller stops ranging,
// the producer stops producing.
//
// The three phases work the same as Node:
//
//   - Resolve: inspect the input, read hints, prepare context.
//   - Act: yield envelopes one at a time via iter.Seq2.
//   - Settle: validate/annotate each yielded item. Items where
//     done=true are emitted; done=false items are dropped.
type StreamNode[I, O any] interface {
	Resolve(context.Context, Envelope[I]) (Envelope[I], error)
	Act(context.Context, Envelope[I]) iter.Seq2[Envelope[O], error]
	Settle(ctx context.Context, in Envelope[I], out Envelope[O], actErr error) (Envelope[O], bool, error)
}

// Stream executes a StreamNode and returns a pull-based iterator.
// Each yielded envelope has been through Resolve and Settle.
//
// The caller controls the pace:
//
//	for out, err := range reflow.Stream(ctx, node, in) {
//	    if err != nil { ... }
//	    // process out
//	}
//
// Stopping the range loop signals the producer to stop via backpressure.
func Stream[I, O any](ctx context.Context, n StreamNode[I, O], in Envelope[I]) iter.Seq2[Envelope[O], error] {
	return func(yield func(Envelope[O], error) bool) {
		resolved, err := n.Resolve(ctx, in)
		if err != nil {
			yield(Envelope[O]{}, fmt.Errorf("reflow: resolve: %w", err))
			return
		}
		resolved.Meta.Trace = resolved.Meta.Trace.fork().append(Step{Phase: "resolve", Status: "ok"})

		for out, actErr := range n.Act(ctx, resolved) {
			settled, done, err := n.Settle(ctx, resolved, out, actErr)
			if err != nil {
				if !yield(Envelope[O]{}, fmt.Errorf("reflow: settle: %w", err)) {
					return
				}
				continue
			}

			settled.Meta.Trace = settled.Meta.Trace.fork().append(Step{Phase: "settle", Status: settleStatus(done)})

			if done {
				if !yield(settled, nil) {
					return
				}
			}
			// done=false: item dropped (filtered by settle)
		}
	}
}

// Collect drains a stream into a slice of envelopes.
// Stops on the first error and returns what was collected so far.
func Collect[T any](seq iter.Seq2[Envelope[T], error]) ([]Envelope[T], error) {
	var out []Envelope[T]
	for env, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, env)
	}
	return out, nil
}

// StreamFunc is the closure adapter for StreamNode, mirroring Func for Node.
// Nil Resolve passes through. Nil Settle accepts all items.
type StreamFunc[I, O any] struct {
	ResolveFn func(context.Context, Envelope[I]) (Envelope[I], error)
	ActFn     func(context.Context, Envelope[I]) iter.Seq2[Envelope[O], error]
	SettleFn  func(context.Context, Envelope[I], Envelope[O], error) (Envelope[O], bool, error)
}

func (f *StreamFunc[I, O]) Resolve(ctx context.Context, in Envelope[I]) (Envelope[I], error) {
	if f.ResolveFn == nil {
		return in, nil
	}
	return f.ResolveFn(ctx, in)
}

func (f *StreamFunc[I, O]) Act(ctx context.Context, in Envelope[I]) iter.Seq2[Envelope[O], error] {
	if f.ActFn != nil {
		return f.ActFn(ctx, in)
	}
	return func(yield func(Envelope[O], error) bool) {}
}

func (f *StreamFunc[I, O]) Settle(ctx context.Context, in Envelope[I], out Envelope[O], actErr error) (Envelope[O], bool, error) {
	if f.SettleFn != nil {
		return f.SettleFn(ctx, in, out, actErr)
	}
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}
