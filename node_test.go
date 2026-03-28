package reflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// --- doubler: shared named type for tests ---

type doubler struct{}

func (doubler) Resolve(_ context.Context, in Envelope[int]) (Envelope[int], error) {
	return in, nil
}
func (doubler) Act(_ context.Context, in Envelope[int]) (Envelope[int], error) {
	return Envelope[int]{Value: in.Value * 2, Meta: in.Meta}, nil
}
func (doubler) Settle(_ context.Context, _ Envelope[int], out Envelope[int], actErr error) (Envelope[int], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// --- Named type (interface style) ---

func TestNamedTypeNode(t *testing.T) {
	out, err := Run(context.Background(), doubler{}, NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 10 {
		t.Fatalf("expected 10, got %d", out.Value)
	}
}

func TestNamedTypePreservesMeta(t *testing.T) {
	in := NewEnvelope(3).WithTag("env", "test").WithHint("src", "upstream", "")
	out, err := Run(context.Background(), doubler{}, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 6 {
		t.Fatalf("expected 6, got %d", out.Value)
	}
	if out.Meta.Tags["env"] != "test" {
		t.Fatal("expected tag preserved")
	}
	if len(out.HintsByCode("src")) != 1 {
		t.Fatal("expected hint preserved")
	}
}

// --- Func adapter ---

func TestFuncSinglePass(t *testing.T) {
	node := &Func[int, int]{
		ActFn: Pass(func(n int) int { return n * 2 }),
	}
	out, err := Run(context.Background(), node, NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 10 {
		t.Fatalf("expected 10, got %d", out.Value)
	}
}

func TestFuncNilResolvePassthrough(t *testing.T) {
	node := &Func[string, string]{
		ActFn: Pass(func(s string) string { return strings.ToUpper(s) }),
	}
	in := NewEnvelope("hello").WithTag("source", "test")
	out, err := Run(context.Background(), node, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "HELLO" {
		t.Fatalf("expected HELLO, got %q", out.Value)
	}
	if out.Meta.Tags["source"] != "test" {
		t.Fatal("expected tag to propagate")
	}
}

func TestFuncNilSettleAcceptsOnSuccess(t *testing.T) {
	node := &Func[int, int]{
		ActFn: Pass(func(n int) int { return n + 1 }),
	}
	out, err := Run(context.Background(), node, NewEnvelope(0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 1 {
		t.Fatalf("expected 1, got %d", out.Value)
	}
}

func TestFuncNilSettleRejectsOnActError(t *testing.T) {
	node := &Func[int, int]{
		ActFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("broken")
		},
	}
	_, err := Run(context.Background(), node, NewEnvelope(0))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("expected underlying error, got: %v", err)
	}
}

func TestFuncNilActReturnsZeroButPreservesMeta(t *testing.T) {
	node := &Func[int, int]{}
	in := NewEnvelope(5).WithTag("env", "test").WithHint("src", "upstream", "")
	out, err := Run(context.Background(), node, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 0 {
		t.Fatalf("expected zero value, got %d", out.Value)
	}
	if out.Meta.Tags["env"] != "test" {
		t.Fatal("expected nil ActFn to preserve input tags")
	}
	if len(out.HintsByCode("src")) != 1 {
		t.Fatal("expected nil ActFn to preserve input hints")
	}
}

func TestFuncAllPhases(t *testing.T) {
	var phases []string
	node := &Func[int, int]{
		ResolveFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			phases = append(phases, "resolve")
			return in, nil
		},
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			phases = append(phases, "act")
			return in.CarryMeta(in.Value + 1), nil
		},
		SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
			phases = append(phases, "settle")
			return out, true, nil
		},
	}

	out, err := Run(context.Background(), node, NewEnvelope(0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 1 {
		t.Fatalf("expected 1, got %d", out.Value)
	}
	if len(phases) != 3 || phases[0] != "resolve" || phases[1] != "act" || phases[2] != "settle" {
		t.Fatalf("expected [resolve, act, settle], got %v", phases)
	}
}

// --- Property-based ---

func TestPBTSinglePassEquivalence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		input := rapid.Int().Draw(t, "input")
		multiplier := rapid.IntRange(1, 100).Draw(t, "multiplier")

		node := &Func[int, int]{
			ActFn: Pass(func(n int) int { return n * multiplier }),
		}
		out, err := Run(context.Background(), node, NewEnvelope(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Value != input*multiplier {
			t.Fatalf("expected %d, got %d", input*multiplier, out.Value)
		}
	})
}
