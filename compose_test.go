package reflow

import (
	"context"
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

func TestComposeLinearPipeline(t *testing.T) {
	double := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 2 })}
	addOne := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 1 })}
	square := &Func[int, int]{ActFn: Pass(func(n int) int { return n * n })}

	// (1 * 2 + 1)^2 = 9
	agent := Compose[int, int]("math", func(ctx context.Context, s *Steps, in Envelope[int]) Envelope[int] {
		a := Do(s, ctx, double, in)
		b := Do(s, ctx, addOne, a)
		return Do(s, ctx, square, b)
	})

	out, err := Run(context.Background(), agent, NewEnvelope(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 9 {
		t.Fatalf("expected 9, got %d", out.Value)
	}
}

func TestComposeErrorShortCircuits(t *testing.T) {
	called := make([]string, 0)

	step1 := &Func[int, int]{ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
		called = append(called, "step1")
		return in, nil
	}}
	failing := &Func[int, int]{ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
		called = append(called, "failing")
		return Envelope[int]{}, fmt.Errorf("step failed")
	}}
	step3 := &Func[int, int]{ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
		called = append(called, "step3")
		return in, nil
	}}

	agent := Compose[int, int]("fails", func(ctx context.Context, s *Steps, in Envelope[int]) Envelope[int] {
		a := Do(s, ctx, step1, in)
		b := Do(s, ctx, failing, a)
		return Do(s, ctx, step3, b)
	})

	_, err := Run(context.Background(), agent, NewEnvelope(42))
	if err == nil {
		t.Fatal("expected error")
	}

	if len(called) != 2 {
		t.Fatalf("expected 2 steps called, got %d: %v", len(called), called)
	}
	if called[0] != "step1" || called[1] != "failing" {
		t.Fatalf("unexpected call order: %v", called)
	}
}

func TestComposePreservesHints(t *testing.T) {
	annotate := &Func[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			return in.WithHint("step.done", in.Value, ""), nil
		},
	}
	upper := &Func[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			out := Envelope[string]{Value: in.Value + "!", Meta: in.Meta}
			return out.WithHint("upper.done", "added bang", ""), nil
		},
	}

	agent := Compose[string, string]("hints", func(ctx context.Context, s *Steps, in Envelope[string]) Envelope[string] {
		a := Do(s, ctx, annotate, in)
		return Do(s, ctx, upper, a)
	})

	out, err := Run(context.Background(), agent, NewEnvelope("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "hello!" {
		t.Fatalf("expected 'hello!', got %q", out.Value)
	}

	stepHints := out.HintsByCode("step.done")
	upperHints := out.HintsByCode("upper.done")
	if len(stepHints) != 1 || len(upperHints) != 1 {
		t.Fatalf("expected 1 hint each, got step=%d upper=%d", len(stepHints), len(upperHints))
	}
}

func TestComposeBranching(t *testing.T) {
	classify := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}
	doubleIt := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 2 })}
	tripleIt := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 3 })}

	agent := Compose[int, int]("branching", func(ctx context.Context, s *Steps, in Envelope[int]) Envelope[int] {
		classified := Do(s, ctx, classify, in)
		if classified.Value > 5 {
			return Do(s, ctx, tripleIt, classified)
		}
		return Do(s, ctx, doubleIt, classified)
	})

	// Input 3 → double → 6
	out, err := Run(context.Background(), agent, NewEnvelope(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 6 {
		t.Fatalf("expected 6, got %d", out.Value)
	}

	// Input 10 → triple → 30
	out, err = Run(context.Background(), agent, NewEnvelope(10))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 30 {
		t.Fatalf("expected 30, got %d", out.Value)
	}
}

func TestComposeComposesWithWithRetry(t *testing.T) {
	attempts := 0
	unreliable := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			attempts++
			if attempts < 3 {
				return Envelope[int]{}, fmt.Errorf("not yet")
			}
			return Envelope[int]{Value: in.Value * 10, Meta: in.Meta}, nil
		},
	}

	agent := Compose[int, int]("retryable", func(ctx context.Context, s *Steps, in Envelope[int]) Envelope[int] {
		return Do(s, ctx, unreliable, in)
	})

	retrying := WithRetry(agent, 5)
	out, err := Run(context.Background(), retrying, NewEnvelope(7))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 70 {
		t.Fatalf("expected 70, got %d", out.Value)
	}
}

func TestComposeNested(t *testing.T) {
	double := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 2 })}
	addOne := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 1 })}

	inner := Compose[int, int]("inner", func(ctx context.Context, s *Steps, in Envelope[int]) Envelope[int] {
		return Do(s, ctx, double, in)
	})

	outer := Compose[int, int]("outer", func(ctx context.Context, s *Steps, in Envelope[int]) Envelope[int] {
		a := Do(s, ctx, inner, in)   // agent inside agent
		return Do(s, ctx, addOne, a)
	})

	// (5 * 2) + 1 = 11
	out, err := Run(context.Background(), outer, NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 11 {
		t.Fatalf("expected 11, got %d", out.Value)
	}
}

func TestDoStandaloneWithoutCompose(t *testing.T) {
	double := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 2 })}
	addOne := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 1 })}

	var s Steps
	ctx := context.Background()
	in := NewEnvelope(5)

	a := Do(&s, ctx, double, in)
	b := Do(&s, ctx, addOne, a)

	if s.Err() != nil {
		t.Fatalf("unexpected error: %v", s.Err())
	}
	if b.Value != 11 {
		t.Fatalf("expected 11, got %d", b.Value)
	}
}

// --- Property-based tests ---

func TestPBTComposeMatchesManualChain(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		input := rapid.IntRange(-100, 100).Draw(t, "input")
		addend := rapid.IntRange(1, 10).Draw(t, "addend")
		factor := rapid.IntRange(1, 5).Draw(t, "factor")

		add := &Func[int, int]{ActFn: Pass(func(n int) int { return n + addend })}
		mul := &Func[int, int]{ActFn: Pass(func(n int) int { return n * factor })}

		// Compose approach
		agent := Compose[int, int]("test", func(ctx context.Context, s *Steps, in Envelope[int]) Envelope[int] {
			a := Do(s, ctx, add, in)
			return Do(s, ctx, mul, a)
		})

		agentOut, err := Run(context.Background(), agent, NewEnvelope(input))
		if err != nil {
			t.Fatalf("agent error: %v", err)
		}

		// Manual approach
		expected := (input + addend) * factor
		if agentOut.Value != expected {
			t.Fatalf("agent gave %d, expected %d", agentOut.Value, expected)
		}
	})
}

func TestPBTDoShortCircuitsOnError(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		failAt := rapid.IntRange(0, 4).Draw(t, "failAt")
		totalSteps := 5

		callCount := 0
		nodes := make([]Node[int, int], totalSteps)
		for i := range totalSteps {
			i := i
			nodes[i] = &Func[int, int]{
				ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
					callCount++
					if i == failAt {
						return Envelope[int]{}, fmt.Errorf("fail at %d", i)
					}
					return Envelope[int]{Value: in.Value + 1, Meta: in.Meta}, nil
				},
			}
		}

		var s Steps
		ctx := context.Background()
		env := NewEnvelope(0)
		for _, n := range nodes {
			env = Do(&s, ctx, n, env)
		}

		if s.Err() == nil {
			t.Fatal("expected error")
		}
		// Should have called failAt+1 nodes (0..failAt inclusive)
		if callCount != failAt+1 {
			t.Fatalf("expected %d calls, got %d", failAt+1, callCount)
		}
	})
}
