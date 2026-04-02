package reflow

import (
	"iter"
	"sync"
)

// Split divides a stream into two based on a predicate. Items where the
// predicate returns true go to the first stream, false to the second.
//
//	urgent, standard := reflow.Split(triaged, func(env reflow.Envelope[Ticket]) bool {
//	    return env.Value.Priority == "urgent"
//	})
//
//	escalated := reflow.Pool(ctx, escalateAgent, urgent, 3)
//	handled   := reflow.Pool(ctx, standardAgent, standard, 10)
//
// Both output streams must be consumed concurrently — Split uses a
// goroutine to read the source and dispatch items to buffered channels.
// If one side isn't consumed, the buffer fills and the source blocks.
//
// Buffer size defaults to 64 items per side.
func Split[T any](source iter.Seq2[Envelope[T], error], pred func(Envelope[T]) bool) (trueStream, falseStream iter.Seq2[Envelope[T], error]) {
	return SplitBuf(source, pred, 64)
}

// SplitBuf is like Split but with a configurable buffer size per side.
func SplitBuf[T any](source iter.Seq2[Envelope[T], error], pred func(Envelope[T]) bool, bufSize int) (trueStream, falseStream iter.Seq2[Envelope[T], error]) {
	if bufSize <= 0 {
		bufSize = 64
	}

	type item struct {
		env Envelope[T]
		err error
	}

	trueCh := make(chan item, bufSize)
	falseCh := make(chan item, bufSize)

	// Dispatch goroutine reads source and routes items.
	var once sync.Once
	start := func() {
		once.Do(func() {
			go func() {
				defer close(trueCh)
				defer close(falseCh)
				for env, err := range source {
					if err != nil {
						// Send error to both sides.
						trueCh <- item{err: err}
						falseCh <- item{err: err}
						return
					}
					if pred(env) {
						trueCh <- item{env: env}
					} else {
						falseCh <- item{env: env}
					}
				}
			}()
		})
	}

	trueStream = func(yield func(Envelope[T], error) bool) {
		start()
		for it := range trueCh {
			if !yield(it.env, it.err) {
				return
			}
		}
	}

	falseStream = func(yield func(Envelope[T], error) bool) {
		start()
		for it := range falseCh {
			if !yield(it.env, it.err) {
				return
			}
		}
	}

	return trueStream, falseStream
}

// Merge interleaves items from multiple streams into a single stream.
// Items are emitted in the order they arrive — no ordering guarantee
// across sources. Each source is consumed in its own goroutine.
//
//	escalated := reflow.Pool(ctx, escalateAgent, urgent, 3)
//	handled   := reflow.Pool(ctx, standardAgent, standard, 10)
//	all       := reflow.Merge(escalated, handled)
//
// Merge completes when all sources are exhausted. If any source yields
// an error, it is forwarded and that source stops contributing.
func Merge[T any](streams ...iter.Seq2[Envelope[T], error]) iter.Seq2[Envelope[T], error] {
	return func(yield func(Envelope[T], error) bool) {
		if len(streams) == 0 {
			return
		}

		type item struct {
			env Envelope[T]
			err error
		}

		out := make(chan item, len(streams)*8)
		var wg sync.WaitGroup

		for _, s := range streams {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for env, err := range s {
					out <- item{env, err}
				}
			}()
		}

		// Close output channel when all sources are done.
		go func() {
			wg.Wait()
			close(out)
		}()

		for it := range out {
			if !yield(it.env, it.err) {
				return
			}
		}
	}
}
