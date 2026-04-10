package reflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// --- merge helpers ---

func sumMerge(results []Envelope[int]) Envelope[int] {
	sum := 0
	var meta Meta
	meta.Tags = make(map[string]string)
	var allHints []Hint
	var allTrace []Step
	for _, r := range results {
		sum += r.Value
		allHints = append(allHints, r.Meta.Hints.Slice()...)
		allTrace = append(allTrace, r.Meta.Trace.Slice()...)
		for k, v := range r.Meta.Tags {
			meta.Tags[k] = v
		}
	}
	meta.Hints = LogFrom(allHints)
	meta.Trace = LogFrom(allTrace)
	return Envelope[int]{Value: sum, Meta: meta}
}

func concatMerge(results []Envelope[string]) Envelope[string] {
	var parts []string
	var meta Meta
	meta.Tags = make(map[string]string)
	var allHints []Hint
	var allTrace []Step
	for _, r := range results {
		parts = append(parts, r.Value)
		allHints = append(allHints, r.Meta.Hints.Slice()...)
		allTrace = append(allTrace, r.Meta.Trace.Slice()...)
		for k, v := range r.Meta.Tags {
			meta.Tags[k] = v
		}
	}
	meta.Hints = LogFrom(allHints)
	meta.Trace = LogFrom(allTrace)
	return Envelope[string]{Value: strings.Join(parts, ","), Meta: meta}
}

// --- Basic behavior ---

func TestForkJoinBasic(t *testing.T) {
	a := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 10 })}
	b := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 20 })}

	fj := ForkJoin(sumMerge, a, b)
	out, err := Run(context.Background(), fj, NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// (5+10) + (5+20) = 40
	if out.Value != 40 {
		t.Fatalf("expected 40, got %d", out.Value)
	}
}

func TestForkJoinSingleNode(t *testing.T) {
	node := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 3 })}

	fj := ForkJoin(sumMerge, node)
	out, err := Run(context.Background(), fj, NewEnvelope(7))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 21 {
		t.Fatalf("expected 21, got %d", out.Value)
	}
}

func TestForkJoinManyNodes(t *testing.T) {
	var nodes []Node[int, int]
	for i := range 10 {
		offset := i
		nodes = append(nodes, &Func[int, int]{
			ActFn: Pass(func(n int) int { return n + offset }),
		})
	}

	fj := ForkJoin(sumMerge, nodes...)
	out, err := Run(context.Background(), fj, NewEnvelope(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 10*1 + (0+1+2+...+9) = 10 + 45 = 55
	if out.Value != 55 {
		t.Fatalf("expected 55, got %d", out.Value)
	}
}

// --- Error handling ---

func TestForkJoinFirstError(t *testing.T) {
	ok := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}
	fail := &Func[int, int]{
		ActFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("branch failed")
		},
	}

	fj := ForkJoin(sumMerge, ok, fail)
	_, err := Run(context.Background(), fj, NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "branch failed") {
		t.Fatalf("expected underlying error, got: %v", err)
	}
}

func TestForkJoinMultipleErrors(t *testing.T) {
	fail1 := &Func[int, int]{
		ActFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("fail-1")
		},
	}
	fail2 := &Func[int, int]{
		ActFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("fail-2")
		},
	}

	fj := ForkJoin(sumMerge, fail1, fail2)
	_, err := Run(context.Background(), fj, NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}
	// errgroup returns the first error; either is fine
	if !strings.Contains(err.Error(), "fail-") {
		t.Fatalf("expected a failure error, got: %v", err)
	}
}

func TestForkJoinPartialFailureCancelsOthers(t *testing.T) {
	var slowRan atomic.Bool

	fast := &Func[int, int]{
		ActFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("fast fail")
		},
	}
	slow := &Func[int, int]{
		ActFn: func(ctx context.Context, in Envelope[int]) (Envelope[int], error) {
			select {
			case <-time.After(2 * time.Second):
				slowRan.Store(true)
				return in, nil
			case <-ctx.Done():
				return Envelope[int]{}, ctx.Err()
			}
		},
	}

	fj := ForkJoin(sumMerge, fast, slow)
	start := time.Now()
	_, err := Run(context.Background(), fj, NewEnvelope(1))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("expected fast cancellation, took %v", elapsed)
	}
	if slowRan.Load() {
		t.Fatal("slow branch should have been cancelled")
	}
}

// --- Concurrency ---

func TestForkJoinRunsConcurrently(t *testing.T) {
	var running atomic.Int32
	var maxConcurrent atomic.Int32

	makeNode := func() Node[int, int] {
		return &Func[int, int]{
			ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
				cur := running.Add(1)
				for {
					old := maxConcurrent.Load()
					if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
				running.Add(-1)
				return in, nil
			},
		}
	}

	fj := ForkJoin(sumMerge, makeNode(), makeNode(), makeNode(), makeNode())
	_, err := Run(context.Background(), fj, NewEnvelope(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if maxConcurrent.Load() < 2 {
		t.Fatalf("expected concurrent execution, max concurrent was %d", maxConcurrent.Load())
	}
}

// --- Context ---

func TestForkJoinRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	node := &Func[int, int]{
		ActFn: func(ctx context.Context, in Envelope[int]) (Envelope[int], error) {
			if err := ctx.Err(); err != nil {
				return Envelope[int]{}, err
			}
			return in, nil
		},
	}

	fj := ForkJoin(sumMerge, node, node)
	_, err := Run(ctx, fj, NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestForkJoinTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	slow := &Func[int, int]{
		ActFn: func(ctx context.Context, in Envelope[int]) (Envelope[int], error) {
			select {
			case <-time.After(5 * time.Second):
				return in, nil
			case <-ctx.Done():
				return Envelope[int]{}, ctx.Err()
			}
		},
	}

	fj := ForkJoin(sumMerge, slow, slow)
	_, err := Run(ctx, fj, NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got: %v", err)
	}
}

// --- Metadata propagation ---

func TestForkJoinMergesHints(t *testing.T) {
	a := &Func[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			return in.WithHint("source.a", "found-a", ""), nil
		},
	}
	b := &Func[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			return in.WithHint("source.b", "found-b", ""), nil
		},
	}

	fj := ForkJoin(concatMerge, a, b)
	out, err := Run(context.Background(), fj, NewEnvelope("input"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Meta.Hints.Len() < 2 {
		t.Fatalf("expected at least 2 hints, got %d", out.Meta.Hints.Len())
	}
	aHints := out.HintsByCode("source.a")
	bHints := out.HintsByCode("source.b")
	if len(aHints) != 1 || len(bHints) != 1 {
		t.Fatalf("expected one hint from each source, got a=%d b=%d", len(aHints), len(bHints))
	}
}

func TestForkJoinMergesTags(t *testing.T) {
	a := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{Value: in.Value, Meta: in.Meta}.WithTag("from", "a"), nil
		},
	}
	b := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{Value: in.Value, Meta: in.Meta}.WithTag("checked", "true"), nil
		},
	}

	fj := ForkJoin(sumMerge, a, b)
	out, err := Run(context.Background(), fj, NewEnvelope(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Meta.Tags["from"] != "a" {
		t.Fatalf("expected tag 'from'='a', got %q", out.Meta.Tags["from"])
	}
	if out.Meta.Tags["checked"] != "true" {
		t.Fatalf("expected tag 'checked'='true', got %q", out.Meta.Tags["checked"])
	}
}

func TestForkJoinMergesTrace(t *testing.T) {
	a := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}
	b := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}

	fj := ForkJoin(sumMerge, a, b)
	out, err := Run(context.Background(), fj, NewEnvelope(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Each branch produces resolve+settle trace steps; merge collects all
	if out.Meta.Trace.Len() < 4 {
		t.Fatalf("expected at least 4 trace steps (2 per branch), got %d", out.Meta.Trace.Len())
	}
}

func TestForkJoinInputTagsReachBranches(t *testing.T) {
	reader := &Func[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			if in.Meta.Tags["env"] != "test" {
				return in, errors.New("missing input tag")
			}
			return in, nil
		},
	}

	fj := ForkJoin(concatMerge, reader, reader)
	in := NewEnvelope("data").WithTag("env", "test")
	_, err := Run(context.Background(), fj, in)
	if err != nil {
		t.Fatalf("input tags should propagate to branches: %v", err)
	}
}

// --- Trace safety ---

func TestForkJoinTraceNoRace(t *testing.T) {
	// Seed the input with trace that has extra capacity to expose shared backing array.
	in := NewEnvelope(1)
	in.Meta.Trace = LogFrom([]Step{{Phase: "setup", Status: "ok"}})

	a := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 1 })}
	b := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 2 })}
	c := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 3 })}
	d := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 4 })}

	fj := ForkJoin(sumMerge, a, b, c, d)
	// Run multiple times to increase chance of race detection
	for range 50 {
		_, err := Run(context.Background(), fj, in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	// Original input trace must be untouched
	if in.Meta.Trace.Len() != 1 {
		t.Fatalf("input trace was corrupted: expected 1 step, got %d", in.Meta.Trace.Len())
	}
}

// --- Composition ---

func TestForkJoinInChain(t *testing.T) {
	prepare := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 10 })}

	a := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 1 })}
	b := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 2 })}
	fj := ForkJoin(sumMerge, a, b)

	pipeline := Chain[int, int, int](prepare, fj)
	out, err := Run(context.Background(), pipeline, NewEnvelope(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 3*10=30, then (30+1)+(30+2)=63
	if out.Value != 63 {
		t.Fatalf("expected 63, got %d", out.Value)
	}
}

func TestForkJoinWithNamedTypes(t *testing.T) {
	fj := ForkJoin(sumMerge, doubler{}, doubler{})
	out, err := Run(context.Background(), fj, NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 5*2 + 5*2 = 20
	if out.Value != 20 {
		t.Fatalf("expected 20, got %d", out.Value)
	}
}

func TestForkJoinWithRetryBranches(t *testing.T) {
	attempts := atomic.Int32{}
	retrying := WithRetry(&Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			attempts.Add(1)
			return in.CarryMeta(in.Value * 2), nil
		},
		SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
			// Settle after 3 attempts
			if attempts.Load() >= 3 {
				return out, true, nil
			}
			out = out.WithHint("retry", "not yet", "")
			return out, false, nil
		},
	}, 5)

	simple := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 100 })}

	fj := ForkJoin(sumMerge, retrying, simple)
	out, err := Run(context.Background(), fj, NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// retrying: always doubles input 5→10 (settles on 3rd), simple: 5+100=105, total=115
	if out.Value != 115 {
		t.Fatalf("expected 115, got %d", out.Value)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 retry attempts, got %d", attempts.Load())
	}
}

// --- Property-based tests ---

func TestPBTForkJoinCommutative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		input := rapid.IntRange(-100, 100).Draw(t, "input")
		offsetA := rapid.IntRange(0, 50).Draw(t, "offsetA")
		offsetB := rapid.IntRange(0, 50).Draw(t, "offsetB")

		a := &Func[int, int]{ActFn: Pass(func(n int) int { return n + offsetA })}
		b := &Func[int, int]{ActFn: Pass(func(n int) int { return n + offsetB })}

		abFJ := ForkJoin(sumMerge, a, b)
		baFJ := ForkJoin(sumMerge, b, a)

		env := NewEnvelope(input)
		outAB, errAB := Run(context.Background(), abFJ, env)
		outBA, errBA := Run(context.Background(), baFJ, env)

		if errAB != nil || errBA != nil {
			t.Fatalf("errors: ab=%v ba=%v", errAB, errBA)
		}
		if outAB.Value != outBA.Value {
			t.Fatalf("sum should be commutative: ab=%d ba=%d", outAB.Value, outBA.Value)
		}
	})
}

func TestPBTForkJoinAllBranchesRun(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "branches")

		var counts []atomic.Int32
		counts = make([]atomic.Int32, n)
		var nodes []Node[int, int]
		for i := range n {
			idx := i
			nodes = append(nodes, &Func[int, int]{
				ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
					counts[idx].Add(1)
					return in, nil
				},
			})
		}

		fj := ForkJoin(sumMerge, nodes...)
		_, err := Run(context.Background(), fj, NewEnvelope(1))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for i := range counts {
			if counts[i].Load() != 1 {
				t.Fatalf("branch %d ran %d times, expected 1", i, counts[i].Load())
			}
		}
	})
}

func TestPBTForkJoinPreservesInputValue(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		input := rapid.IntRange(-1000, 1000).Draw(t, "input")
		n := rapid.IntRange(1, 5).Draw(t, "branches")

		var nodes []Node[int, int]
		for range n {
			nodes = append(nodes, &Func[int, int]{
				ActFn: Pass(func(v int) int { return v }),
			})
		}

		fj := ForkJoin(sumMerge, nodes...)
		out, err := Run(context.Background(), fj, NewEnvelope(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := input * n
		if out.Value != expected {
			t.Fatalf("expected %d (input*branches), got %d", expected, out.Value)
		}
	})
}

func TestPBTForkJoinHintCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(t, "branches")

		var nodes []Node[string, string]
		for i := range n {
			code := fmt.Sprintf("branch.%d", i)
			nodes = append(nodes, &Func[string, string]{
				ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
					return in.WithHint(code, "info", ""), nil
				},
			})
		}

		fj := ForkJoin(concatMerge, nodes...)
		out, err := Run(context.Background(), fj, NewEnvelope("x"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Each branch adds 1 hint, merge collects all
		// (trace-generated hints may also exist, so check >= n)
		if out.Meta.Hints.Len() < n {
			t.Fatalf("expected at least %d hints, got %d", n, out.Meta.Hints.Len())
		}
	})
}
