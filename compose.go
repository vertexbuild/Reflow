package reflow

import "context"

// Steps tracks errors across a sequence of Do calls.
// If any step fails, subsequent Do calls are no-ops.
// Check the result with Err() after the sequence completes.
//
// This follows the same pattern as bufio.Scanner — accumulate
// and check once at the end.
type Steps struct {
	err error
}

// Err returns the first error encountered during the sequence, or nil.
func (s *Steps) Err() error { return s.err }

// Do executes a node within a Steps sequence. If a previous step already
// failed, Do is a no-op and returns a zero envelope.
//
//	var s reflow.Steps
//	a := reflow.Do(&s, ctx, classify, in)
//	b := reflow.Do(&s, ctx, extract, a)
//	c := reflow.Do(&s, ctx, validate, b)
//	return c, s.Err()
func Do[I, O any](s *Steps, ctx context.Context, n Node[I, O], in Envelope[I]) Envelope[O] {
	if s.err != nil {
		return Envelope[O]{}
	}
	out, err := Run(ctx, n, in)
	if err != nil {
		s.err = err
		return Envelope[O]{}
	}
	return out
}

// Compose wraps a composition function as a Node[I, O].
// The function body declares the execution graph — each Do call is a step.
// The resulting Node composes with Chain, ForkJoin, WithRetry, and Pool
// like any other node.
//
//	support := reflow.Compose[Request, Response]("support",
//	    func(ctx context.Context, s *reflow.Steps, in reflow.Envelope[Request]) reflow.Envelope[Response] {
//	        a := reflow.Do(s, ctx, classify, in)
//	        b := reflow.Do(s, ctx, extract, a)
//	        c := reflow.Do(s, ctx, validate, b)
//	        return reflow.Do(s, ctx, respond, c)
//	    },
//	)
//
//	out, err := reflow.Run(ctx, support, envelope)
func Compose[I, O any](name string, fn func(context.Context, *Steps, Envelope[I]) Envelope[O]) Node[I, O] {
	return &composeNode[I, O]{name: name, fn: fn}
}

type composeNode[I, O any] struct {
	name string
	fn   func(context.Context, *Steps, Envelope[I]) Envelope[O]
}

func (c *composeNode[I, O]) Resolve(_ context.Context, in Envelope[I]) (Envelope[I], error) {
	return in, nil
}

func (c *composeNode[I, O]) Act(ctx context.Context, in Envelope[I]) (Envelope[O], error) {
	var s Steps
	out := c.fn(ctx, &s, in)
	if s.Err() != nil {
		return out, s.Err()
	}
	return out, nil
}

func (c *composeNode[I, O]) Settle(_ context.Context, _ Envelope[I], out Envelope[O], actErr error) (Envelope[O], bool, error) {
	if actErr != nil {
		// Return done=false with nil error so WithRetry can retry.
		// The actErr is settled as a hint for the next iteration.
		out = out.WithHint("compose.error", actErr.Error(), "")
		return out, false, nil
	}
	return out, true, nil
}
