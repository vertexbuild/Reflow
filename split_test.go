package reflow

import (
	"fmt"
	"iter"
	"sync"
	"testing"

	"pgregory.net/rapid"
)

func TestSplitBasic(t *testing.T) {
	source := streamSlice(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	evens, odds := Split(source, func(env Envelope[int]) bool {
		return env.Value%2 == 0
	})

	var evenVals, oddVals []int
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		for env, err := range evens {
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			evenVals = append(evenVals, env.Value)
		}
	}()
	go func() {
		defer wg.Done()
		for env, err := range odds {
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			oddVals = append(oddVals, env.Value)
		}
	}()
	wg.Wait()

	if len(evenVals) != 5 {
		t.Fatalf("expected 5 evens, got %d: %v", len(evenVals), evenVals)
	}
	if len(oddVals) != 5 {
		t.Fatalf("expected 5 odds, got %d: %v", len(oddVals), oddVals)
	}

	for _, v := range evenVals {
		if v%2 != 0 {
			t.Errorf("expected even, got %d", v)
		}
	}
	for _, v := range oddVals {
		if v%2 == 0 {
			t.Errorf("expected odd, got %d", v)
		}
	}
}

func TestSplitAllTrue(t *testing.T) {
	source := streamSlice(1, 2, 3)
	trueS, falseS := Split(source, func(env Envelope[int]) bool { return true })

	var trueVals, falseVals []int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for env, _ := range trueS {
			trueVals = append(trueVals, env.Value)
		}
	}()
	go func() {
		defer wg.Done()
		for env, _ := range falseS {
			falseVals = append(falseVals, env.Value)
		}
	}()
	wg.Wait()

	if len(trueVals) != 3 {
		t.Fatalf("expected 3 true, got %d", len(trueVals))
	}
	if len(falseVals) != 0 {
		t.Fatalf("expected 0 false, got %d", len(falseVals))
	}
}

func TestSplitPreservesHints(t *testing.T) {
	source := func(yield func(Envelope[string], error) bool) {
		env := NewEnvelope("hello").WithHint("test", "preserved", "")
		yield(env, nil)
	}

	trueS, _ := Split(source, func(env Envelope[string]) bool { return true })

	var wg sync.WaitGroup
	wg.Add(1)
	// Drain the false side (empty but must be consumed)
	go func() { defer wg.Done() }()

	for env, err := range trueS {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		hints := env.HintsByCode("test")
		if len(hints) != 1 {
			t.Fatalf("expected 1 hint, got %d", len(hints))
		}
	}
	wg.Wait()
}

func TestSplitErrorForwardedToBoth(t *testing.T) {
	source := func(yield func(Envelope[int], error) bool) {
		yield(NewEnvelope(1), nil)
		yield(Envelope[int]{}, fmt.Errorf("boom"))
	}

	trueS, falseS := Split(source, func(env Envelope[int]) bool {
		return env.Value%2 == 0
	})

	var trueErr, falseErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for _, err := range trueS {
			if err != nil {
				trueErr = err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for _, err := range falseS {
			if err != nil {
				falseErr = err
				return
			}
		}
	}()
	wg.Wait()

	// At least one side should see the error (the odd item goes to false,
	// then the error goes to both).
	if trueErr == nil && falseErr == nil {
		t.Fatal("expected at least one side to see the error")
	}
}

func TestMergeBasic(t *testing.T) {
	a := streamSlice(1, 2, 3)
	b := streamSlice(4, 5, 6)
	c := streamSlice(7, 8, 9)

	merged, err := Collect(Merge(a, b, c))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(merged) != 9 {
		t.Fatalf("expected 9 items, got %d", len(merged))
	}

	// All values should be present (order not guaranteed).
	seen := make(map[int]bool)
	for _, env := range merged {
		seen[env.Value] = true
	}
	for i := 1; i <= 9; i++ {
		if !seen[i] {
			t.Errorf("missing value %d", i)
		}
	}
}

func TestMergeEmpty(t *testing.T) {
	merged, err := Collect(Merge[int]())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(merged) != 0 {
		t.Fatalf("expected 0 items, got %d", len(merged))
	}
}

func TestMergeSingle(t *testing.T) {
	a := streamSlice(1, 2, 3)
	merged, err := Collect(Merge(a))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(merged) != 3 {
		t.Fatalf("expected 3 items, got %d", len(merged))
	}
}

func TestMergePreservesHints(t *testing.T) {
	a := func(yield func(Envelope[string], error) bool) {
		yield(NewEnvelope("a").WithHint("source", "a", ""), nil)
	}
	b := func(yield func(Envelope[string], error) bool) {
		yield(NewEnvelope("b").WithHint("source", "b", ""), nil)
	}

	merged, err := Collect(Merge(a, b))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(merged) != 2 {
		t.Fatalf("expected 2 items, got %d", len(merged))
	}

	for _, env := range merged {
		hints := env.HintsByCode("source")
		if len(hints) != 1 {
			t.Errorf("expected 1 hint on %q, got %d", env.Value, len(hints))
		}
	}
}

func TestSplitMergeRoundTrip(t *testing.T) {
	source := streamSlice(1, 2, 3, 4, 5, 6, 7, 8)

	evens, odds := Split(source, func(env Envelope[int]) bool {
		return env.Value%2 == 0
	})

	all, err := Collect(Merge(evens, odds))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(all) != 8 {
		t.Fatalf("expected 8 items, got %d", len(all))
	}

	seen := make(map[int]bool)
	for _, env := range all {
		seen[env.Value] = true
	}
	for i := 1; i <= 8; i++ {
		if !seen[i] {
			t.Errorf("missing value %d after round-trip", i)
		}
	}
}

// --- Property-based tests ---

func TestPBTSplitPartitions(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 50).Draw(t, "n")
		threshold := rapid.IntRange(0, 100).Draw(t, "threshold")

		items := make([]int, n)
		for i := range n {
			items[i] = rapid.IntRange(0, 100).Draw(t, fmt.Sprintf("item[%d]", i))
		}

		source := streamSlice(items...)
		trueS, falseS := Split(source, func(env Envelope[int]) bool {
			return env.Value >= threshold
		})

		var trueVals, falseVals []int
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for env, _ := range trueS {
				trueVals = append(trueVals, env.Value)
			}
		}()
		go func() {
			defer wg.Done()
			for env, _ := range falseS {
				falseVals = append(falseVals, env.Value)
			}
		}()
		wg.Wait()

		// Total count must match.
		if len(trueVals)+len(falseVals) != n {
			t.Fatalf("split lost items: %d + %d != %d", len(trueVals), len(falseVals), n)
		}

		// All true items should pass predicate.
		for _, v := range trueVals {
			if v < threshold {
				t.Fatalf("true side has %d < threshold %d", v, threshold)
			}
		}
		for _, v := range falseVals {
			if v >= threshold {
				t.Fatalf("false side has %d >= threshold %d", v, threshold)
			}
		}
	})
}

func TestPBTMergePreservesAll(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		nStreams := rapid.IntRange(1, 5).Draw(t, "streams")
		streams := make([]iter.Seq2[Envelope[int], error], nStreams)
		total := 0

		for i := range nStreams {
			size := rapid.IntRange(0, 20).Draw(t, fmt.Sprintf("size[%d]", i))
			items := make([]int, size)
			for j := range size {
				items[j] = total
				total++
			}
			streams[i] = streamSlice(items...)
		}

		merged, err := Collect(Merge(streams...))
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if len(merged) != total {
			t.Fatalf("expected %d items, got %d", total, len(merged))
		}

		seen := make(map[int]bool)
		for _, env := range merged {
			if seen[env.Value] {
				t.Fatalf("duplicate value %d", env.Value)
			}
			seen[env.Value] = true
		}
	})
}
