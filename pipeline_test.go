package reflow

import (
	"context"
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

func TestPipelineLinear(t *testing.T) {
	addOne := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 1 })}
	double := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 2 })}
	addTen := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 10 })}

	// (5 + 1) * 2 + 10 = 22
	pipe := Pipeline[int]("math", addOne, double, addTen)
	out, err := Run(context.Background(), pipe, NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 22 {
		t.Fatalf("expected 22, got %d", out.Value)
	}
}

func TestPipelineSingleNode(t *testing.T) {
	double := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 2 })}

	pipe := Pipeline[int]("single", double)
	out, err := Run(context.Background(), pipe, NewEnvelope(7))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 14 {
		t.Fatalf("expected 14, got %d", out.Value)
	}
}

func TestPipelineEmpty(t *testing.T) {
	pipe := Pipeline[int]("empty")
	out, err := Run(context.Background(), pipe, NewEnvelope(42))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 42 {
		t.Fatalf("expected 42 (passthrough), got %d", out.Value)
	}
}

func TestPipelinePreservesHints(t *testing.T) {
	annotate := &Func[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			return in.WithHint("step.a", "first", ""), nil
		},
	}
	append_ := &Func[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			out := Envelope[string]{Value: in.Value + "!", Meta: in.Meta}
			return out.WithHint("step.b", "second", ""), nil
		},
	}

	pipe := Pipeline[string]("hints", annotate, append_)
	out, err := Run(context.Background(), pipe, NewEnvelope("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "hello!" {
		t.Fatalf("expected 'hello!', got %q", out.Value)
	}

	a := out.HintsByCode("step.a")
	b := out.HintsByCode("step.b")
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("expected 1 hint each, got a=%d b=%d", len(a), len(b))
	}
}

func TestPipelineErrorStopsExecution(t *testing.T) {
	called := []string{}
	step1 := &Func[int, int]{ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
		called = append(called, "step1")
		return in, nil
	}}
	failing := &Func[int, int]{ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
		called = append(called, "failing")
		return Envelope[int]{}, fmt.Errorf("boom")
	}}
	step3 := &Func[int, int]{ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
		called = append(called, "step3")
		return in, nil
	}}

	pipe := Pipeline[int]("fails", step1, failing, step3)
	_, err := Run(context.Background(), pipe, NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(called) != 2 {
		t.Fatalf("expected 2 steps called, got %d: %v", len(called), called)
	}
}

func TestPipelineComposesWithAgent(t *testing.T) {
	addOne := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 1 })}
	double := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 2 })}

	skill := Pipeline[int]("prep", addOne, double)

	agent := Compose[int, int]("outer", func(ctx context.Context, s *Steps, in Envelope[int]) Envelope[int] {
		return Do(s, ctx, skill, in)
	})

	// (3 + 1) * 2 = 8
	out, err := Run(context.Background(), agent, NewEnvelope(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 8 {
		t.Fatalf("expected 8, got %d", out.Value)
	}
}

func TestPipelineComposesWithWithRetry(t *testing.T) {
	attempts := 0
	flaky := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			attempts++
			if attempts < 2 {
				return Envelope[int]{}, fmt.Errorf("flaky")
			}
			return Envelope[int]{Value: in.Value * 10, Meta: in.Meta}, nil
		},
	}
	addOne := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 1 })}

	pipe := Pipeline[int]("retryable", flaky, addOne)
	retrying := WithRetry(pipe, 3)

	out, err := Run(context.Background(), retrying, NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Second attempt: (5 * 10) + 1 = 51
	if out.Value != 51 {
		t.Fatalf("expected 51, got %d", out.Value)
	}
}

// --- Property-based tests ---

func TestPBTPipelineEquivalentToSequentialRun(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		input := rapid.IntRange(-100, 100).Draw(t, "input")
		a := rapid.IntRange(1, 10).Draw(t, "a")
		b := rapid.IntRange(1, 10).Draw(t, "b")
		c := rapid.IntRange(1, 10).Draw(t, "c")

		nodeA := &Func[int, int]{ActFn: Pass(func(n int) int { return n + a })}
		nodeB := &Func[int, int]{ActFn: Pass(func(n int) int { return n * b })}
		nodeC := &Func[int, int]{ActFn: Pass(func(n int) int { return n + c })}

		// Pipeline
		pipe := Pipeline[int]("test", nodeA, nodeB, nodeC)
		pipeOut, err := Run(context.Background(), pipe, NewEnvelope(input))
		if err != nil {
			t.Fatalf("pipeline error: %v", err)
		}

		// Manual sequential
		expected := (input + a) * b + c
		if pipeOut.Value != expected {
			t.Fatalf("pipeline gave %d, expected %d", pipeOut.Value, expected)
		}
	})
}

func TestPBTPipelineAccumulatesHints(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 10).Draw(t, "steps")

		nodes := make([]Node[int, int], n)
		for i := range n {
			i := i
			nodes[i] = &Func[int, int]{
				ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
					return in.WithHint(fmt.Sprintf("step.%d", i), "done", ""), nil
				},
			}
		}

		pipe := Pipeline[int]("hints", nodes...)
		out, err := Run(context.Background(), pipe, NewEnvelope(0))
		if err != nil {
			t.Fatalf("error: %v", err)
		}

		if out.Meta.Hints.Len() != n {
			t.Fatalf("expected %d hints, got %d", n, out.Meta.Hints.Len())
		}
	})
}
