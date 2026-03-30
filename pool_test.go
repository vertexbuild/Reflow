package reflow

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolBasic(t *testing.T) {
	node := &Func[int, int]{
		ActFn: Pass(func(n int) int { return n * 2 }),
	}

	source := streamSlice(1, 2, 3, 4, 5)
	results, err := Collect(Pool(context.Background(), node, source, 3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}

	// Verify order is preserved.
	for i, r := range results {
		expected := (i + 1) * 2
		if r.Value != expected {
			t.Errorf("result[%d] = %d, want %d", i, r.Value, expected)
		}
	}
}

func TestPoolPreservesOrder(t *testing.T) {
	// Each item takes a different amount of time, but results should
	// still come out in input order.
	node := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			// Higher values finish faster to test ordering.
			delay := time.Duration(50-in.Value*5) * time.Millisecond
			if delay > 0 {
				time.Sleep(delay)
			}
			return Envelope[int]{Value: in.Value * 10, Meta: in.Meta}, nil
		},
	}

	source := streamSlice(1, 2, 3, 4, 5, 6, 7, 8)
	results, err := Collect(Pool(context.Background(), node, source, 4))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, r := range results {
		expected := (i + 1) * 10
		if r.Value != expected {
			t.Errorf("result[%d] = %d, want %d", i, r.Value, expected)
		}
	}
}

func TestPoolBoundsConcurrency(t *testing.T) {
	var active atomic.Int32
	var maxSeen atomic.Int32

	node := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			cur := active.Add(1)
			// Track peak concurrency.
			for {
				old := maxSeen.Load()
				if cur <= old || maxSeen.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			active.Add(-1)
			return Envelope[int]{Value: in.Value, Meta: in.Meta}, nil
		},
	}

	source := streamSlice(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	_, err := Collect(Pool(context.Background(), node, source, 3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	peak := maxSeen.Load()
	if peak > 3 {
		t.Fatalf("peak concurrency %d exceeded limit 3", peak)
	}
	if peak < 2 {
		t.Fatalf("peak concurrency %d — pool doesn't seem to parallelize", peak)
	}
}

func TestPoolContextCancellation(t *testing.T) {
	node := &Func[int, int]{
		ActFn: func(ctx context.Context, in Envelope[int]) (Envelope[int], error) {
			select {
			case <-time.After(5 * time.Second):
				return Envelope[int]{Value: in.Value, Meta: in.Meta}, nil
			case <-ctx.Done():
				return Envelope[int]{}, ctx.Err()
			}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	source := streamSlice(1, 2, 3, 4, 5)
	_, err := Collect(Pool(ctx, node, source, 3))
	if err == nil {
		t.Fatal("expected error from cancellation")
	}
}

func TestPoolSingleConcurrency(t *testing.T) {
	// concurrency=1 should process sequentially.
	var order []int
	node := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			order = append(order, in.Value)
			return Envelope[int]{Value: in.Value, Meta: in.Meta}, nil
		},
	}

	source := streamSlice(1, 2, 3)
	_, err := Collect(Pool(context.Background(), node, source, 1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, v := range order {
		if v != i+1 {
			t.Fatalf("out of order: got %v", order)
		}
	}
}

func TestPoolPropagatesError(t *testing.T) {
	node := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			if in.Value == 3 {
				return Envelope[int]{}, fmt.Errorf("bad value: %d", in.Value)
			}
			return Envelope[int]{Value: in.Value, Meta: in.Meta}, nil
		},
	}

	source := streamSlice(1, 2, 3, 4, 5)
	results, err := Collect(Pool(context.Background(), node, source, 2))
	if err == nil {
		t.Fatal("expected error")
	}
	// Should have collected items before the error.
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results before error, got %d", len(results))
	}
}

func TestPoolHintPropagation(t *testing.T) {
	node := &Func[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			out := Envelope[string]{Value: in.Value + "!", Meta: in.Meta}
			return out.WithHint("pool.processed", in.Value, ""), nil
		},
	}

	source := streamSlice("a", "b", "c")
	results, err := Collect(Pool(context.Background(), node, source, 2))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range results {
		hints := r.HintsByCode("pool.processed")
		if len(hints) != 1 {
			t.Errorf("expected 1 hint, got %d", len(hints))
		}
	}
}

func TestPoolEmptySource(t *testing.T) {
	node := &Func[int, int]{
		ActFn: Pass(func(n int) int { return n }),
	}

	source := streamSlice[int]()
	results, err := Collect(Pool(context.Background(), node, source, 5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

// streamSlice creates an iter.Seq2 from a slice of values.
func streamSlice[T any](items ...T) func(func(Envelope[T], error) bool) {
	return func(yield func(Envelope[T], error) bool) {
		for _, item := range items {
			if !yield(NewEnvelope(item), nil) {
				return
			}
		}
	}
}
