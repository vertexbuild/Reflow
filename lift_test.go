package reflow

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"pgregory.net/rapid"
)

// --- Lift ---

func TestLiftBasic(t *testing.T) {
	node := &Func[string, int]{
		ActFn: Lift(func(s string) (int, error) {
			return strconv.Atoi(s)
		}),
	}
	out, err := Run(context.Background(), node, NewEnvelope("42"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 42 {
		t.Fatalf("expected 42, got %d", out.Value)
	}
}

func TestLiftPropagaresError(t *testing.T) {
	node := &Func[string, int]{
		ActFn: Lift(func(s string) (int, error) {
			return 0, errors.New("parse failed")
		}),
	}
	_, err := Run(context.Background(), node, NewEnvelope("bad"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLiftCarriesMeta(t *testing.T) {
	node := &Func[int, string]{
		ActFn: Lift(func(n int) (string, error) {
			return fmt.Sprintf("val=%d", n), nil
		}),
	}
	in := NewEnvelope(5).WithTag("env", "test").WithHint("src", "upstream", "")
	out, err := Run(context.Background(), node, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "val=5" {
		t.Fatalf("expected 'val=5', got %q", out.Value)
	}
	if out.Meta.Tags["env"] != "test" {
		t.Fatal("expected tag preserved")
	}
	if len(out.HintsByCode("src")) != 1 {
		t.Fatal("expected hint preserved")
	}
}

// --- LiftCtx ---

func TestLiftCtxBasic(t *testing.T) {
	node := &Func[int, string]{
		ActFn: LiftCtx(func(ctx context.Context, n int) (string, error) {
			if ctx == nil {
				return "", errors.New("nil context")
			}
			return fmt.Sprintf("%d", n), nil
		}),
	}
	out, err := Run(context.Background(), node, NewEnvelope(7))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "7" {
		t.Fatalf("expected '7', got %q", out.Value)
	}
}

func TestLiftCtxReceivesContext(t *testing.T) {
	type ctxKey string
	ctx := context.WithValue(context.Background(), ctxKey("k"), "hello")

	node := &Func[int, string]{
		ActFn: LiftCtx(func(ctx context.Context, n int) (string, error) {
			v, ok := ctx.Value(ctxKey("k")).(string)
			if !ok {
				return "", errors.New("missing context value")
			}
			return v, nil
		}),
	}
	out, err := Run(ctx, node, NewEnvelope(0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "hello" {
		t.Fatalf("expected 'hello', got %q", out.Value)
	}
}

func TestLiftCtxCarriesMeta(t *testing.T) {
	node := &Func[int, int]{
		ActFn: LiftCtx(func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		}),
	}
	in := NewEnvelope(3).WithTag("k", "v")
	out, err := Run(context.Background(), node, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 6 {
		t.Fatalf("expected 6, got %d", out.Value)
	}
	if out.Meta.Tags["k"] != "v" {
		t.Fatal("expected tag preserved")
	}
}

// --- Pass ---

func TestPassBasic(t *testing.T) {
	node := &Func[int, int]{
		ActFn: Pass(func(n int) int { return n * 3 }),
	}
	out, err := Run(context.Background(), node, NewEnvelope(4))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 12 {
		t.Fatalf("expected 12, got %d", out.Value)
	}
}

func TestPassTypeConversion(t *testing.T) {
	node := &Func[int, string]{
		ActFn: Pass(func(n int) string { return fmt.Sprintf("n=%d", n) }),
	}
	out, err := Run(context.Background(), node, NewEnvelope(99))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "n=99" {
		t.Fatalf("expected 'n=99', got %q", out.Value)
	}
}

func TestPassCarriesMeta(t *testing.T) {
	node := &Func[string, string]{
		ActFn: Pass(func(s string) string { return s }),
	}
	in := NewEnvelope("x").WithTag("a", "1").WithHint("h", "msg", "span")
	out, err := Run(context.Background(), node, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Meta.Tags["a"] != "1" {
		t.Fatal("expected tag preserved")
	}
	if out.Meta.Hints.Len() != 1 || out.Meta.Hints.Slice()[0].Code != "h" {
		t.Fatal("expected hint preserved")
	}
}

func TestPassNeverErrors(t *testing.T) {
	// Pass wraps a function that doesn't return error.
	// Verify Act never returns an error.
	node := &Func[int, int]{
		ActFn: Pass(func(n int) int { return n }),
	}
	for i := range 100 {
		_, err := Run(context.Background(), node, NewEnvelope(i))
		if err != nil {
			t.Fatalf("unexpected error on i=%d: %v", i, err)
		}
	}
}

// --- Property-based ---

func TestPBTLiftRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.Int().Draw(t, "n")

		toStr := &Func[int, string]{
			ActFn: Lift(func(n int) (string, error) {
				return strconv.Itoa(n), nil
			}),
		}
		toInt := &Func[string, int]{
			ActFn: Lift(func(s string) (int, error) {
				return strconv.Atoi(s)
			}),
		}

		chain := Chain[int, string, int](toStr, toInt)
		out, err := Run(context.Background(), chain, NewEnvelope(n))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Value != n {
			t.Fatalf("round trip: expected %d, got %d", n, out.Value)
		}
	})
}

func TestPBTPassPreservesMeta(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "key")
		val := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "val")
		code := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "code")

		node := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}
		in := NewEnvelope(0).WithTag(key, val).WithHint(code, "msg", "")
		out, err := Run(context.Background(), node, in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Meta.Tags[key] != val {
			t.Fatalf("tag %q lost", key)
		}
		if len(out.HintsByCode(code)) != 1 {
			t.Fatalf("hint %q lost", code)
		}
	})
}
