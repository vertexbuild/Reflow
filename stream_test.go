package reflow

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// --- Basic streaming ---

func TestStreamBasic(t *testing.T) {
	node := &StreamFunc[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) iter.Seq2[Envelope[string], error] {
			return func(yield func(Envelope[string], error) bool) {
				for _, word := range strings.Fields(in.Value) {
					if !yield(Envelope[string]{Value: word, Meta: in.Meta}, nil) {
						return
					}
				}
			}
		},
	}

	results, err := Collect(Stream(context.Background(), node, NewEnvelope("hello world foo")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 items, got %d", len(results))
	}
	if results[0].Value != "hello" || results[1].Value != "world" || results[2].Value != "foo" {
		t.Fatalf("unexpected values: %v", results)
	}
}

func TestStreamEmpty(t *testing.T) {
	node := &StreamFunc[int, int]{} // nil ActFn yields nothing

	results, err := Collect(Stream(context.Background(), node, NewEnvelope(1)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 items, got %d", len(results))
	}
}

func TestStreamSingleItem(t *testing.T) {
	node := &StreamFunc[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				yield(in.CarryMeta(in.Value*2), nil)
			}
		},
	}

	results, err := Collect(Stream(context.Background(), node, NewEnvelope(5)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Value != 10 {
		t.Fatalf("expected [10], got %v", results)
	}
}

// --- Settle filtering ---

func TestStreamSettleFilters(t *testing.T) {
	// Yield numbers 1-5, settle rejects evens
	node := &StreamFunc[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				for i := 1; i <= 5; i++ {
					if !yield(Envelope[int]{Value: i, Meta: in.Meta}, nil) {
						return
					}
				}
			}
		},
		SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
			if out.Value%2 == 0 {
				return out, false, nil // reject evens
			}
			return out, true, nil
		},
	}

	results, err := Collect(Stream(context.Background(), node, NewEnvelope(0)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 odd numbers, got %d: %v", len(results), results)
	}
	for _, r := range results {
		if r.Value%2 == 0 {
			t.Fatalf("expected only odds, got %d", r.Value)
		}
	}
}

func TestStreamSettleAnnotates(t *testing.T) {
	node := &StreamFunc[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) iter.Seq2[Envelope[string], error] {
			return func(yield func(Envelope[string], error) bool) {
				for _, w := range strings.Fields(in.Value) {
					yield(Envelope[string]{Value: w, Meta: in.Meta}, nil)
				}
			}
		},
		SettleFn: func(_ context.Context, _ Envelope[string], out Envelope[string], _ error) (Envelope[string], bool, error) {
			if len(out.Value) <= 3 {
				out = out.WithHint("short", "word is short", "")
			}
			return out, true, nil
		},
	}

	results, err := Collect(Stream(context.Background(), node, NewEnvelope("hi there yo")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "hi" and "yo" are short, "there" is not
	shortCount := 0
	for _, r := range results {
		if len(r.HintsByCode("short")) > 0 {
			shortCount++
		}
	}
	if shortCount != 2 {
		t.Fatalf("expected 2 short hints, got %d", shortCount)
	}
}

// --- Error handling ---

func TestStreamResolveError(t *testing.T) {
	node := &StreamFunc[int, int]{
		ResolveFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("resolve failed")
		},
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {}
		},
	}

	results, err := Collect(Stream(context.Background(), node, NewEnvelope(0)))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolve") {
		t.Fatalf("expected resolve error, got: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results before error, got %d", len(results))
	}
}

func TestStreamActError(t *testing.T) {
	// Act yields some good items then an error
	node := &StreamFunc[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				yield(Envelope[int]{Value: 1, Meta: in.Meta}, nil)
				yield(Envelope[int]{Value: 2, Meta: in.Meta}, nil)
				yield(Envelope[int]{}, errors.New("act failed mid-stream"))
			}
		},
	}

	results, err := Collect(Stream(context.Background(), node, NewEnvelope(0)))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "act failed") {
		t.Fatalf("expected act error, got: %v", err)
	}
	// Should have collected the 2 good items before the error
	if len(results) != 2 {
		t.Fatalf("expected 2 items before error, got %d", len(results))
	}
}

func TestStreamSettleError(t *testing.T) {
	node := &StreamFunc[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				yield(Envelope[int]{Value: 1, Meta: in.Meta}, nil)
			}
		},
		SettleFn: func(_ context.Context, _ Envelope[int], _ Envelope[int], _ error) (Envelope[int], bool, error) {
			return Envelope[int]{}, false, errors.New("settle exploded")
		},
	}

	results, err := Collect(Stream(context.Background(), node, NewEnvelope(0)))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "settle") {
		t.Fatalf("expected settle error, got: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}
}

// --- Backpressure ---

func TestStreamBackpressure(t *testing.T) {
	produced := 0
	node := &StreamFunc[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				for i := range 1000 {
					produced++
					if !yield(Envelope[int]{Value: i, Meta: in.Meta}, nil) {
						return
					}
				}
			}
		},
	}

	// Only take the first 3
	count := 0
	for _, err := range Stream(context.Background(), node, NewEnvelope(0)) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		count++
		if count >= 3 {
			break
		}
	}

	if count != 3 {
		t.Fatalf("expected 3 items, got %d", count)
	}
	// Producer should have stopped well before 1000
	if produced > 10 {
		t.Fatalf("expected backpressure to stop producer early, produced %d", produced)
	}
}

// --- Metadata ---

func TestStreamCarriesMeta(t *testing.T) {
	node := &StreamFunc[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				yield(Envelope[int]{Value: in.Value, Meta: in.Meta}, nil)
			}
		},
	}

	in := NewEnvelope(42).WithTag("env", "test").WithHint("src", "upstream", "")
	results, err := Collect(Stream(context.Background(), node, in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Meta.Tags["env"] != "test" {
		t.Fatal("expected tag preserved")
	}
	if len(results[0].HintsByCode("src")) != 1 {
		t.Fatal("expected hint preserved")
	}
}

func TestStreamTrace(t *testing.T) {
	node := &StreamFunc[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				yield(Envelope[int]{Value: 1, Meta: in.Meta}, nil)
				yield(Envelope[int]{Value: 2, Meta: in.Meta}, nil)
			}
		},
	}

	results, err := Collect(Stream(context.Background(), node, NewEnvelope(0)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Each item should have resolve + settle trace
	for i, r := range results {
		hasResolve, hasSettle := false, false
		for _, s := range r.Meta.Trace {
			if s.Phase == "resolve" {
				hasResolve = true
			}
			if s.Phase == "settle" {
				hasSettle = true
			}
		}
		if !hasResolve || !hasSettle {
			t.Fatalf("item %d missing trace phases: %+v", i, r.Meta.Trace)
		}
	}
}

// --- Context ---

func TestStreamContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	node := &StreamFunc[int, int]{
		ActFn: func(ctx context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				for i := range 100 {
					if ctx.Err() != nil {
						yield(Envelope[int]{}, ctx.Err())
						return
					}
					if !yield(Envelope[int]{Value: i, Meta: in.Meta}, nil) {
						return
					}
					if i == 2 {
						cancel()
					}
				}
			}
		},
	}

	var collected []Envelope[int]
	var streamErr error
	for env, err := range Stream(ctx, node, NewEnvelope(0)) {
		if err != nil {
			streamErr = err
			break
		}
		collected = append(collected, env)
	}

	if streamErr == nil {
		t.Fatal("expected error from cancellation")
	}
	if !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", streamErr)
	}
}

// --- Named type ---

type csvSplitter struct{}

func (csvSplitter) Resolve(_ context.Context, in Envelope[string]) (Envelope[string], error) {
	return in, nil
}

func (csvSplitter) Act(_ context.Context, in Envelope[string]) iter.Seq2[Envelope[string], error] {
	return func(yield func(Envelope[string], error) bool) {
		for _, line := range strings.Split(in.Value, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !yield(Envelope[string]{Value: line, Meta: in.Meta}, nil) {
				return
			}
		}
	}
}

func (csvSplitter) Settle(_ context.Context, _ Envelope[string], out Envelope[string], actErr error) (Envelope[string], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	fields := strings.Split(out.Value, ",")
	if len(fields) < 2 {
		return out, false, nil // drop malformed rows
	}
	return out, true, nil
}

func TestStreamNamedType(t *testing.T) {
	input := "Alice,30\nBob\nCharlie,25\n\nDave,40"

	results, err := Collect(Stream(context.Background(), csvSplitter{}, NewEnvelope(input)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "Bob" has only 1 field, should be dropped. Empty line dropped.
	if len(results) != 3 {
		t.Fatalf("expected 3 valid rows, got %d", len(results))
	}
	if results[0].Value != "Alice,30" {
		t.Fatalf("expected 'Alice,30', got %q", results[0].Value)
	}
}

// --- Collect ---

func TestCollectEmpty(t *testing.T) {
	seq := func(yield func(Envelope[int], error) bool) {}
	results, err := Collect[int](seq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 items, got %d", len(results))
	}
}

func TestCollectStopsOnError(t *testing.T) {
	produced := 0
	seq := func(yield func(Envelope[int], error) bool) {
		produced++
		if !yield(NewEnvelope(1), nil) {
			return
		}
		produced++
		if !yield(NewEnvelope(2), nil) {
			return
		}
		produced++
		if !yield(Envelope[int]{}, errors.New("boom")) {
			return
		}
		produced++
		// This should never be reached — Collect stops on error
		yield(NewEnvelope(4), nil)
	}

	results, err := Collect[int](seq)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 items before error, got %d", len(results))
	}
	if produced != 3 {
		t.Fatalf("expected producer to stop after error yield, produced %d", produced)
	}
}

// --- Property-based ---

func TestPBTStreamYieldsAll(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 50).Draw(t, "items")

		node := &StreamFunc[int, int]{
			ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
				return func(yield func(Envelope[int], error) bool) {
					for i := range n {
						if !yield(Envelope[int]{Value: i, Meta: in.Meta}, nil) {
							return
						}
					}
				}
			},
		}

		results, err := Collect(Stream(context.Background(), node, NewEnvelope(0)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != n {
			t.Fatalf("expected %d items, got %d", n, len(results))
		}
	})
}

func TestPBTStreamSettleFilterCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 30).Draw(t, "items")
		threshold := rapid.IntRange(0, n).Draw(t, "threshold")

		node := &StreamFunc[int, int]{
			ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
				return func(yield func(Envelope[int], error) bool) {
					for i := range n {
						if !yield(Envelope[int]{Value: i, Meta: in.Meta}, nil) {
							return
						}
					}
				}
			},
			SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
				return out, out.Value < threshold, nil
			},
		}

		results, err := Collect(Stream(context.Background(), node, NewEnvelope(0)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != threshold {
			t.Fatalf("expected %d items (values < %d), got %d", threshold, threshold, len(results))
		}
	})
}

func TestPBTStreamPreservesTags(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "key")
		val := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "val")

		node := &StreamFunc[int, int]{
			ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
				return func(yield func(Envelope[int], error) bool) {
					yield(Envelope[int]{Value: in.Value, Meta: in.Meta}, nil)
				}
			},
		}

		in := NewEnvelope(0).WithTag(key, val)
		results, err := Collect(Stream(context.Background(), node, in))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Meta.Tags[key] != val {
			t.Fatalf("tag %q lost: expected %q, got %q", key, val, results[0].Meta.Tags[key])
		}
	})
}

// --- Integration: Stream feeding into batch Node ---

func TestStreamToBatchViaColl(t *testing.T) {
	// Stream splits words, collect gathers them, batch node joins them
	splitter := &StreamFunc[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) iter.Seq2[Envelope[string], error] {
			return func(yield func(Envelope[string], error) bool) {
				for _, w := range strings.Fields(in.Value) {
					if !yield(Envelope[string]{Value: strings.ToUpper(w), Meta: in.Meta}, nil) {
						return
					}
				}
			}
		},
	}

	// Stream phase
	items, err := Collect(Stream(context.Background(), splitter, NewEnvelope("hello world")))
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	// Gather values for batch phase
	var words []string
	for _, item := range items {
		words = append(words, item.Value)
	}

	// Batch phase
	joiner := &Func[[]string, string]{
		ActFn: Pass(func(ws []string) string { return strings.Join(ws, "-") }),
	}
	out, err := Run(context.Background(), joiner, NewEnvelope(words))
	if err != nil {
		t.Fatalf("batch error: %v", err)
	}
	if out.Value != "HELLO-WORLD" {
		t.Fatalf("expected 'HELLO-WORLD', got %q", out.Value)
	}
}

// --- Nil defaults ---

func TestStreamFuncNilResolve(t *testing.T) {
	node := &StreamFunc[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				yield(in, nil)
			}
		},
	}

	in := NewEnvelope(5).WithTag("k", "v")
	results, err := Collect(Stream(context.Background(), node, in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0].Meta.Tags["k"] != "v" {
		t.Fatal("nil resolve should pass through with meta")
	}
}

func TestStreamFuncNilSettle(t *testing.T) {
	node := &StreamFunc[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				yield(in.CarryMeta(1), nil)
				yield(in.CarryMeta(2), nil)
			}
		},
	}

	results, err := Collect(Stream(context.Background(), node, NewEnvelope(0)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Nil settle accepts all
	if len(results) != 2 {
		t.Fatalf("expected 2 items, got %d", len(results))
	}
}

func TestStreamFuncNilSettleRejectsActError(t *testing.T) {
	node := &StreamFunc[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
			return func(yield func(Envelope[int], error) bool) {
				if !yield(in, nil) {
					return
				}
				if !yield(Envelope[int]{}, fmt.Errorf("bad item")) {
					return
				}
				yield(in.CarryMeta(99), nil)
			}
		},
	}

	// Nil settle rejects items with act errors (returns err from settle)
	results, err := Collect(Stream(context.Background(), node, NewEnvelope(0)))
	if err == nil {
		t.Fatal("expected error from nil settle on act error")
	}
	// First item should have been collected before the error
	if len(results) != 1 {
		t.Fatalf("expected 1 item before error, got %d", len(results))
	}
}
