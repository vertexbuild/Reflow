package reflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// --- Basic ---

func TestChainComposition(t *testing.T) {
	double := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 2 })}
	addOne := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 1 })}

	chain := Chain[int, int, int](double, addOne)
	out, err := Run(context.Background(), chain, NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 5*2 + 1 = 11
	if out.Value != 11 {
		t.Fatalf("expected 11, got %d", out.Value)
	}
}

func TestChainTypeConversion(t *testing.T) {
	toStr := &Func[int, string]{
		ActFn: Lift(func(n int) (string, error) {
			return fmt.Sprintf("%d", n), nil
		}),
	}
	annotate := &Func[string, string]{
		ActFn: Pass(func(s string) string { return "value=" + s }),
	}

	chain := Chain[int, string, string](toStr, annotate)
	out, err := Run(context.Background(), chain, NewEnvelope(42))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "value=42" {
		t.Fatalf("expected 'value=42', got %q", out.Value)
	}
}

func TestChainNamedTypes(t *testing.T) {
	chain := Chain[int, int, int](doubler{}, doubler{})
	out, err := Run(context.Background(), chain, NewEnvelope(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 3*2*2 = 12
	if out.Value != 12 {
		t.Fatalf("expected 12, got %d", out.Value)
	}
}

func TestChainThreeDeep(t *testing.T) {
	a := &Func[int, int]{ActFn: Pass(func(n int) int { return n + 1 })}
	b := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 10 })}
	c := &Func[int, int]{ActFn: Pass(func(n int) int { return n - 5 })}

	chain := Chain[int, int, int](a, Chain[int, int, int](b, c))
	out, err := Run(context.Background(), chain, NewEnvelope(2))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// (2+1)*10 - 5 = 25
	if out.Value != 25 {
		t.Fatalf("expected 25, got %d", out.Value)
	}
}

// --- Error propagation ---

func TestChainPropagatesFirstError(t *testing.T) {
	fail := &Func[int, int]{
		ActFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("boom")
		},
	}
	noop := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}

	chain := Chain[int, int, int](fail, noop)
	_, err := Run(context.Background(), chain, NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected underlying error, got: %v", err)
	}
}

func TestChainPropagatesSecondError(t *testing.T) {
	noop := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}
	fail := &Func[int, int]{
		ActFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("second failed")
		},
	}

	chain := Chain[int, int, int](noop, fail)
	_, err := Run(context.Background(), chain, NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "second failed") {
		t.Fatalf("expected second error, got: %v", err)
	}
}

func TestChainFirstErrorSkipsSecond(t *testing.T) {
	secondRan := false
	fail := &Func[int, int]{
		ActFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("first broke")
		},
	}
	tracker := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			secondRan = true
			return in, nil
		},
	}

	chain := Chain[int, int, int](fail, tracker)
	_, _ = Run(context.Background(), chain, NewEnvelope(1))
	if secondRan {
		t.Fatal("second node should not run after first error")
	}
}

// --- Metadata flow ---

func TestChainHintPropagation(t *testing.T) {
	hinter := &Func[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			return in.WithHint("step1.done", "processed", ""), nil
		},
	}
	reader := &Func[string, string]{
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			hints := in.HintsByCode("step1.done")
			if len(hints) == 0 {
				return in, errors.New("expected hint from upstream")
			}
			return in.CarryMeta("got hint: " + hints[0].Message), nil
		},
	}

	chain := Chain[string, string, string](hinter, reader)
	out, err := Run(context.Background(), chain, NewEnvelope("data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "got hint: processed" {
		t.Fatalf("expected 'got hint: processed', got %q", out.Value)
	}
}

func TestChainTagPropagation(t *testing.T) {
	tagger := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			return in.WithTag("stage", "first"), nil
		},
	}
	reader := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			if in.Meta.Tags["stage"] != "first" {
				return in, errors.New("missing upstream tag")
			}
			return in.WithTag("stage", "second"), nil
		},
	}

	chain := Chain[int, int, int](tagger, reader)
	out, err := Run(context.Background(), chain, NewEnvelope(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Meta.Tags["stage"] != "second" {
		t.Fatalf("expected tag 'second', got %q", out.Meta.Tags["stage"])
	}
}

func TestChainTraceAccumulates(t *testing.T) {
	a := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}
	b := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}

	chain := Chain[int, int, int](a, b)
	out, err := Run(context.Background(), chain, NewEnvelope(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Each node produces resolve + settle = 2 trace steps. Two nodes = 4 minimum.
	if len(out.Meta.Trace) < 4 {
		t.Fatalf("expected at least 4 trace steps, got %d", len(out.Meta.Trace))
	}
}

func TestChainInputTagsReachBothNodes(t *testing.T) {
	var firstSaw, secondSaw string
	a := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			firstSaw = in.Meta.Tags["env"]
			return in, nil
		},
	}
	b := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			secondSaw = in.Meta.Tags["env"]
			return in, nil
		},
	}

	chain := Chain[int, int, int](a, b)
	in := NewEnvelope(1).WithTag("env", "test")
	_, err := Run(context.Background(), chain, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if firstSaw != "test" || secondSaw != "test" {
		t.Fatalf("expected both nodes to see env=test, got first=%q second=%q", firstSaw, secondSaw)
	}
}

// --- Context ---

func TestChainContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	a := &Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			cancel() // cancel after first node
			return in, nil
		},
	}
	b := &Func[int, int]{
		ResolveFn: func(ctx context.Context, in Envelope[int]) (Envelope[int], error) {
			if err := ctx.Err(); err != nil {
				return Envelope[int]{}, err
			}
			return in, nil
		},
		ActFn: Pass(func(n int) int { return n }),
	}

	chain := Chain[int, int, int](a, b)
	_, err := Run(ctx, chain, NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

// --- Property-based ---

func TestPBTChainAssociativity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		input := rapid.IntRange(-100, 100).Draw(t, "input")
		addend := rapid.IntRange(1, 50).Draw(t, "addend")
		multiplier := rapid.IntRange(1, 10).Draw(t, "multiplier")

		a := &Func[int, int]{ActFn: Pass(func(n int) int { return n + addend })}
		b := &Func[int, int]{ActFn: Pass(func(n int) int { return n * multiplier })}
		c := &Func[int, int]{ActFn: Pass(func(n int) int { return n - 1 })}

		leftAssoc := Chain[int, int, int](Chain[int, int, int](a, b), c)
		rightAssoc := Chain[int, int, int](a, Chain[int, int, int](b, c))

		env := NewEnvelope(input)
		outL, errL := Run(context.Background(), leftAssoc, env)
		outR, errR := Run(context.Background(), rightAssoc, env)

		if errL != nil || errR != nil {
			t.Fatalf("errors: left=%v right=%v", errL, errR)
		}
		if outL.Value != outR.Value {
			t.Fatalf("left=%d right=%d", outL.Value, outR.Value)
		}
	})
}

func TestPBTChainPreservesTags(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "key")
		val := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "val")

		a := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}
		b := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}

		chain := Chain[int, int, int](a, b)
		in := NewEnvelope(0).WithTag(key, val)
		out, err := Run(context.Background(), chain, in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Meta.Tags[key] != val {
			t.Fatalf("tag %q lost: expected %q, got %q", key, val, out.Meta.Tags[key])
		}
	})
}
