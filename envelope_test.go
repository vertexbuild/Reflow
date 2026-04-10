package reflow

import (
	"testing"

	"pgregory.net/rapid"
)

// --- Construction ---

func TestNewEnvelope(t *testing.T) {
	e := NewEnvelope("hello")
	if e.Value != "hello" {
		t.Fatalf("expected 'hello', got %q", e.Value)
	}
	if e.Meta.Tags == nil {
		t.Fatal("expected non-nil tags map")
	}
	if e.Meta.Hints.Len() != 0 {
		t.Fatal("expected no hints")
	}
	if e.Meta.Trace.Len() != 0 {
		t.Fatal("expected no trace")
	}
}

func TestNewEnvelopeZeroValue(t *testing.T) {
	e := NewEnvelope(0)
	if e.Value != 0 {
		t.Fatalf("expected 0, got %d", e.Value)
	}
}

func TestNewEnvelopeStruct(t *testing.T) {
	type record struct{ Name string }
	e := NewEnvelope(record{Name: "Alice"})
	if e.Value.Name != "Alice" {
		t.Fatalf("expected Alice, got %q", e.Value.Name)
	}
}

// --- Hints ---

func TestEnvelopeWithHint(t *testing.T) {
	e := NewEnvelope("x").
		WithHint("a", "msg-a", "span-a").
		WithHint("b", "msg-b", "")
	if e.Meta.Hints.Len() != 2 {
		t.Fatalf("expected 2 hints, got %d", e.Meta.Hints.Len())
	}
	if e.Meta.Hints.Slice()[0].Code != "a" {
		t.Fatalf("expected hint code 'a', got %q", e.Meta.Hints.Slice()[0].Code)
	}
	if e.Meta.Hints.Slice()[0].Span != "span-a" {
		t.Fatalf("expected span 'span-a', got %q", e.Meta.Hints.Slice()[0].Span)
	}
}

func TestEnvelopeHintsByCode(t *testing.T) {
	e := NewEnvelope("x").
		WithHint("a", "first", "").
		WithHint("b", "second", "").
		WithHint("a", "third", "")
	hints := e.HintsByCode("a")
	if len(hints) != 2 {
		t.Fatalf("expected 2 hints with code 'a', got %d", len(hints))
	}
	if hints[0].Message != "first" || hints[1].Message != "third" {
		t.Fatalf("unexpected hint messages: %v", hints)
	}
}

func TestEnvelopeHintsByCodeEmpty(t *testing.T) {
	e := NewEnvelope("x").WithHint("a", "msg", "")
	hints := e.HintsByCode("nonexistent")
	if len(hints) != 0 {
		t.Fatalf("expected 0 hints, got %d", len(hints))
	}
}

func TestWithHintImmutable(t *testing.T) {
	original := NewEnvelope("x").WithHint("a", "one", "")
	_ = original.WithHint("b", "two", "")

	if original.Meta.Hints.Len() != 1 {
		t.Fatalf("original should still have 1 hint, got %d", original.Meta.Hints.Len())
	}
}

// --- Tags ---

func TestEnvelopeWithTag(t *testing.T) {
	e := NewEnvelope(1).WithTag("env", "test").WithTag("ver", "1")
	if e.Meta.Tags["env"] != "test" {
		t.Fatal("expected tag 'env'='test'")
	}
	if e.Meta.Tags["ver"] != "1" {
		t.Fatal("expected tag 'ver'='1'")
	}
}

func TestWithTagOverwrite(t *testing.T) {
	e := NewEnvelope(1).WithTag("k", "v1").WithTag("k", "v2")
	if e.Meta.Tags["k"] != "v2" {
		t.Fatalf("expected overwrite to 'v2', got %q", e.Meta.Tags["k"])
	}
}

func TestWithTagImmutable(t *testing.T) {
	original := NewEnvelope(1).WithTag("a", "1")
	_ = original.WithTag("b", "2")

	if _, ok := original.Meta.Tags["b"]; ok {
		t.Fatal("original should not have tag 'b'")
	}
}

// --- CarryMeta ---

func TestCarryMeta(t *testing.T) {
	e := NewEnvelope("original").
		WithHint("h", "hint-msg", "").
		WithTag("k", "v")

	carried := e.CarryMeta("new-value")
	if carried.Value != "new-value" {
		t.Fatalf("expected 'new-value', got %q", carried.Value)
	}
	if carried.Meta.Hints.Len() != 1 || carried.Meta.Hints.Slice()[0].Code != "h" {
		t.Fatal("hints should be carried")
	}
	if carried.Meta.Tags["k"] != "v" {
		t.Fatal("tags should be carried")
	}
}

func TestCarryMetaPreservesTrace(t *testing.T) {
	e := NewEnvelope(1)
	e.Meta.Trace = e.Meta.Trace.append(Step{Phase: "resolve", Status: "ok"})

	carried := e.CarryMeta(2)
	if carried.Meta.Trace.Len() != 1 {
		t.Fatalf("expected 1 trace step, got %d", carried.Meta.Trace.Len())
	}
}

// --- Map ---

func TestMapCrossType(t *testing.T) {
	in := NewEnvelope("hello").
		WithHint("h", "msg", "span").
		WithTag("k", "v")
	in.Meta.Trace = in.Meta.Trace.append(Step{Phase: "resolve", Status: "ok"})

	out := Map(in, 42)
	if out.Value != 42 {
		t.Fatalf("expected 42, got %d", out.Value)
	}
	if out.Meta.Hints.Len() != 1 || out.Meta.Hints.Slice()[0].Code != "h" {
		t.Fatal("hints should be carried")
	}
	if out.Meta.Tags["k"] != "v" {
		t.Fatal("tags should be carried")
	}
	if out.Meta.Trace.Len() != 1 {
		t.Fatal("trace should be carried")
	}
}

func TestMapWithTagDoesNotMutateSource(t *testing.T) {
	in := NewEnvelope("original").WithTag("a", "1")
	out := Map(in, 99).WithTag("b", "2")

	if _, ok := in.Meta.Tags["b"]; ok {
		t.Fatal("WithTag on mapped envelope should not mutate source")
	}
	if out.Meta.Tags["b"] != "2" {
		t.Fatal("mapped envelope should have the new tag")
	}
}

func TestMapStructToStruct(t *testing.T) {
	type A struct{ Name string }
	type B struct{ Count int }

	in := NewEnvelope(A{Name: "alice"}).WithHint("src", "a", "")
	out := Map(in, B{Count: 5})
	if out.Value.Count != 5 {
		t.Fatalf("expected 5, got %d", out.Value.Count)
	}
	if out.Meta.Hints.Len() != 1 {
		t.Fatal("hints should be carried across struct types")
	}
}

// --- Property-based tests ---

func TestPBTMetaPreservation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "key")
		val := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "val")

		e := NewEnvelope(0).WithTag(key, val)
		if e.Meta.Tags[key] != val {
			t.Fatalf("tag %q lost: expected %q, got %q", key, val, e.Meta.Tags[key])
		}
	})
}

func TestPBTHintImmutability(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 10).Draw(t, "hints")

		e := NewEnvelope("x")
		for i := range n {
			e = e.WithHint("code", rapid.String().Draw(t, "msg"), "")
			if e.Meta.Hints.Len() != i+1 {
				t.Fatalf("expected %d hints after %d WithHint calls, got %d", i+1, i+1, e.Meta.Hints.Len())
			}
		}
	})
}

func TestPBTWithTagDoesNotMutateOriginal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		k1 := rapid.StringMatching(`[a-z]{3,6}`).Draw(t, "k1")
		v1 := rapid.StringMatching(`[a-z]{3,6}`).Draw(t, "v1")
		k2 := rapid.StringMatching(`[a-z]{3,6}`).Draw(t, "k2")
		v2 := rapid.StringMatching(`[a-z]{3,6}`).Draw(t, "v2")

		original := NewEnvelope(0).WithTag(k1, v1)
		_ = original.WithTag(k2, v2)

		if _, ok := original.Meta.Tags[k2]; ok && k1 != k2 {
			t.Fatal("WithTag mutated the original envelope")
		}
	})
}
