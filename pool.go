package reflow

import (
	"context"
	"iter"
	"sync"
)

// Pool processes a stream of envelopes through a Node with bounded concurrency.
// It preserves emission order: items come out in the same sequence they went in,
// regardless of which worker finishes first.
//
//	source := reflow.Stream(ctx, splitter, input)
//	for out, err := range reflow.Pool(ctx, classifier, source, 10) { ... }
//
// Or collect:
//
//	results, err := reflow.Collect(reflow.Pool(ctx, classifier, source, 10))
//
// Backpressure works naturally: if the consumer stops ranging, in-flight
// items finish but no new items are dispatched.
func Pool[I, O any](ctx context.Context, node Node[I, O], source iter.Seq2[Envelope[I], error], concurrency int) iter.Seq2[Envelope[O], error] {
	if concurrency <= 0 {
		concurrency = 1
	}

	return func(yield func(Envelope[O], error) bool) {
		type result struct {
			env Envelope[O]
			err error
		}

		sem := make(chan struct{}, concurrency)
		var mu sync.Mutex
		var slots []chan result

		// Dispatch: each upstream item gets a slot channel that preserves order.
		for item, err := range source {
			if err != nil {
				yield(Envelope[O]{}, err)
				return
			}

			slot := make(chan result, 1)
			mu.Lock()
			slots = append(slots, slot)
			mu.Unlock()

			// Acquire semaphore — bounds active goroutines.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				yield(Envelope[O]{}, ctx.Err())
				return
			}

			go func() {
				defer func() { <-sem }()
				out, runErr := Run(ctx, node, item)
				slot <- result{out, runErr}
			}()
		}

		// Drain in order.
		mu.Lock()
		ordered := slots
		mu.Unlock()

		for _, slot := range ordered {
			r := <-slot
			if !yield(r.env, r.err) {
				return
			}
		}
	}
}
