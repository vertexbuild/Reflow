package reflow

import "context"

// Chain composes two nodes sequentially: ab runs first, then bc receives
// ab's output. The result is a node that goes from A → C.
func Chain[A, B, C any](ab Node[A, B], bc Node[B, C]) Runner[A, C] {
	return &chainNode[A, B, C]{ab: ab, bc: bc}
}

type chainNode[A, B, C any] struct {
	ab Node[A, B]
	bc Node[B, C]
}

func (c *chainNode[A, B, C]) Resolve(ctx context.Context, in Envelope[A]) (Envelope[A], error) {
	return in, nil
}

func (c *chainNode[A, B, C]) Act(ctx context.Context, in Envelope[A]) (Envelope[C], error) {
	mid, err := Run(ctx, c.ab, in)
	if err != nil {
		return Envelope[C]{}, err
	}
	return Run(ctx, c.bc, mid)
}

func (c *chainNode[A, B, C]) Settle(_ context.Context, _ Envelope[A], out Envelope[C], actErr error) (Envelope[C], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

func (c *chainNode[A, B, C]) Run(ctx context.Context, in Envelope[A]) (Envelope[C], error) {
	mid, err := Run(ctx, c.ab, in)
	if err != nil {
		return Envelope[C]{}, err
	}
	return Run(ctx, c.bc, mid)
}
