package reflow

import (
	"context"
	"sync"
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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error

	for i, n := range f.nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := Run(ctx, n, in)
			if err != nil {
				once.Do(func() { firstErr = err; cancel() })
				return
			}
			results[i] = out
		}()
	}

	wg.Wait()
	if firstErr != nil {
		return Envelope[T]{}, firstErr
	}

	return f.merge(results), nil
}
