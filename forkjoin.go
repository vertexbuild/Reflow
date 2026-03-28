package reflow

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// ForkJoin runs multiple nodes concurrently on the same input and merges
// the results. All nodes must share the same input and output type.
//
// The merge function combines the collected outputs into a single envelope.
// If any node returns an error, ForkJoin returns that error.
func ForkJoin[T any](merge func([]Envelope[T]) Envelope[T], nodes ...Node[T, T]) Runner[T, T] {
	return &forkJoinNode[T]{merge: merge, nodes: nodes}
}

type forkJoinNode[T any] struct {
	merge func([]Envelope[T]) Envelope[T]
	nodes []Node[T, T]
}

func (f *forkJoinNode[T]) Resolve(_ context.Context, in Envelope[T]) (Envelope[T], error) {
	return in, nil
}

func (f *forkJoinNode[T]) Act(ctx context.Context, in Envelope[T]) (Envelope[T], error) {
	return f.run(ctx, in)
}

func (f *forkJoinNode[T]) Settle(_ context.Context, _ Envelope[T], out Envelope[T], actErr error) (Envelope[T], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

func (f *forkJoinNode[T]) Run(ctx context.Context, in Envelope[T]) (Envelope[T], error) {
	return f.run(ctx, in)
}

func (f *forkJoinNode[T]) run(ctx context.Context, in Envelope[T]) (Envelope[T], error) {
	results := make([]Envelope[T], len(f.nodes))

	g, ctx := errgroup.WithContext(ctx)
	for i, n := range f.nodes {
		g.Go(func() error {
			var err error
			results[i], err = Run(ctx, n, in)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return Envelope[T]{}, err
	}

	return f.merge(results), nil
}
