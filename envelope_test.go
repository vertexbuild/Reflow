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
	if len(e.Meta.Hints) != 0 {
		t.Fatal("expected no hints")
	}
	if len(e.Meta.Trace) != 0 {
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
	if len(e.Meta.Hints) != 2 {
		t.Fatalf("expected 2 hints, got %d", len(e.Meta.Hints))
	}
	if e.Meta.Hints[0].Code != "a" {
		t.Fatalf("expected hint code 'a', got %q", e.Meta.Hints[0].Code)
	}
	if e.Meta.Hints[0].Span != "span-a" {
		t.Fatalf("expected span 'span-a', got %q", e.Meta.Hints[0].Span)
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

	if len(original.Meta.Hints) != 1 {
		t.Fatalf("original should still have 1 hint, got %d", len(original.Meta.Hints))
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
	if len(carried.Meta.Hints) != 1 || carried.Meta.Hints[0].Code != "h" {
		t.Fatal("hints should be carried")
	}
	if carried.Meta.Tags["k"] != "v" {
		t.Fatal("tags should be carried")
	}
}

func TestCarryMetaPreservesTrace(t *testing.T) {
	e := NewEnvelope(1)
	e.Meta.Trace = append(e.Meta.Trace, Step{Phase: "resolve", Status: "ok"})

	carried := e.CarryMeta(2)
	if len(carried.Meta.Trace) != 1 {
		t.Fatalf("expected 1 trace step, got %d", len(carried.Meta.Trace))
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
			if len(e.Meta.Hints) != i+1 {
				t.Fatalf("expected %d hints after %d WithHint calls, got %d", i+1, i+1, len(e.Meta.Hints))
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
