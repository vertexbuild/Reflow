package reflow

import (
	"testing"

	"pgregory.net/rapid"
)

func TestRingBasic(t *testing.T) {
	r := NewRing[int](3)

	if r.Len() != 0 {
		t.Fatalf("expected len 0, got %d", r.Len())
	}
	if r.Full() {
		t.Fatal("expected not full")
	}

	r.Push(1)
	r.Push(2)

	if r.Len() != 2 {
		t.Fatalf("expected len 2, got %d", r.Len())
	}

	got := r.Slice()
	want := []int{1, 2}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRingOverwrite(t *testing.T) {
	r := NewRing[int](3)

	r.Push(1)
	r.Push(2)
	r.Push(3)
	if !r.Full() {
		t.Fatal("expected full")
	}

	r.Push(4) // overwrites 1
	r.Push(5) // overwrites 2

	got := r.Slice()
	want := []int{3, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if r.Len() != 3 {
		t.Fatalf("expected len 3, got %d", r.Len())
	}
}

func TestRingPeek(t *testing.T) {
	r := NewRing[string](5)

	_, ok := r.Peek()
	if ok {
		t.Fatal("expected no peek on empty ring")
	}

	r.Push("a")
	r.Push("b")
	r.Push("c")

	v, ok := r.Peek()
	if !ok || v != "c" {
		t.Fatalf("expected peek 'c', got %q ok=%v", v, ok)
	}
}

func TestRingPeekAfterWrap(t *testing.T) {
	r := NewRing[int](2)

	r.Push(1)
	r.Push(2)
	r.Push(3) // wraps, overwrites 1

	v, ok := r.Peek()
	if !ok || v != 3 {
		t.Fatalf("expected peek 3, got %d", v)
	}

	got := r.Slice()
	want := []int{2, 3}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRingEach(t *testing.T) {
	r := NewRing[int](4)
	r.Push(10)
	r.Push(20)
	r.Push(30)

	var collected []int
	r.Each(func(v int) bool {
		collected = append(collected, v)
		return true
	})

	want := []int{10, 20, 30}
	if !sliceEqual(collected, want) {
		t.Fatalf("got %v, want %v", collected, want)
	}
}

func TestRingEachEarlyStop(t *testing.T) {
	r := NewRing[int](5)
	for i := range 5 {
		r.Push(i)
	}

	var collected []int
	r.Each(func(v int) bool {
		collected = append(collected, v)
		return len(collected) < 3 // stop after 3
	})

	if len(collected) != 3 {
		t.Fatalf("expected 3 items, got %d", len(collected))
	}
}

func TestRingClear(t *testing.T) {
	r := NewRing[int](3)
	r.Push(1)
	r.Push(2)
	r.Push(3)
	r.Clear()

	if r.Len() != 0 {
		t.Fatalf("expected len 0 after clear, got %d", r.Len())
	}
	if r.Full() {
		t.Fatal("expected not full after clear")
	}

	got := r.Slice()
	if len(got) != 0 {
		t.Fatalf("expected empty slice after clear, got %v", got)
	}
}

func TestRingCapOne(t *testing.T) {
	r := NewRing[int](1)

	r.Push(42)
	if r.Len() != 1 {
		t.Fatalf("expected len 1, got %d", r.Len())
	}

	r.Push(99)
	v, ok := r.Peek()
	if !ok || v != 99 {
		t.Fatalf("expected 99, got %d", v)
	}

	got := r.Slice()
	want := []int{99}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// --- Property-based tests ---

func TestPBTRingLenNeverExceedsCap(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cap := rapid.IntRange(1, 50).Draw(t, "cap")
		n := rapid.IntRange(0, 200).Draw(t, "n")

		r := NewRing[int](cap)
		for i := range n {
			r.Push(i)
		}

		if r.Len() > r.Cap() {
			t.Fatalf("len %d > cap %d", r.Len(), r.Cap())
		}
		if n >= cap && r.Len() != cap {
			t.Fatalf("pushed %d items into cap %d ring, expected full, got len %d", n, cap, r.Len())
		}
	})
}

func TestPBTRingSliceIsChronological(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cap := rapid.IntRange(1, 50).Draw(t, "cap")
		n := rapid.IntRange(1, 200).Draw(t, "n")

		r := NewRing[int](cap)
		for i := range n {
			r.Push(i)
		}

		s := r.Slice()
		// Items should be in ascending order (they were pushed 0, 1, 2, ...).
		for i := 1; i < len(s); i++ {
			if s[i] <= s[i-1] {
				t.Fatalf("not chronological at index %d: %v", i, s)
			}
		}
	})
}

func TestPBTRingPeekIsLast(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cap := rapid.IntRange(1, 50).Draw(t, "cap")
		n := rapid.IntRange(1, 200).Draw(t, "n")

		r := NewRing[int](cap)
		for i := range n {
			r.Push(i)
		}

		v, ok := r.Peek()
		if !ok {
			t.Fatal("expected peek to succeed")
		}
		if v != n-1 {
			t.Fatalf("expected peek %d, got %d", n-1, v)
		}
	})
}

func TestPBTRingSliceMatchesEach(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cap := rapid.IntRange(1, 50).Draw(t, "cap")
		n := rapid.IntRange(0, 200).Draw(t, "n")

		r := NewRing[int](cap)
		for i := range n {
			r.Push(i)
		}

		s := r.Slice()
		var eachItems []int
		r.Each(func(v int) bool {
			eachItems = append(eachItems, v)
			return true
		})

		if !sliceEqual(s, eachItems) {
			t.Fatalf("Slice %v != Each %v", s, eachItems)
		}
	})
}

// --- SafeRing ---

func TestSafeRingBasic(t *testing.T) {
	r := NewSafeRing[int](3)

	r.Push(1)
	r.Push(2)

	if r.Len() != 2 {
		t.Fatalf("expected len 2, got %d", r.Len())
	}
	if r.Cap() != 3 {
		t.Fatalf("expected cap 3, got %d", r.Cap())
	}

	got := r.Slice()
	want := []int{1, 2}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSafeRingOverwrite(t *testing.T) {
	r := NewSafeRing[int](3)
	r.Push(1)
	r.Push(2)
	r.Push(3)
	r.Push(4)
	r.Push(5)

	if !r.Full() {
		t.Fatal("expected full")
	}

	got := r.Slice()
	want := []int{3, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSafeRingPeek(t *testing.T) {
	r := NewSafeRing[string](5)

	_, ok := r.Peek()
	if ok {
		t.Fatal("expected no peek on empty ring")
	}

	r.Push("a")
	r.Push("b")
	v, ok := r.Peek()
	if !ok || v != "b" {
		t.Fatalf("expected peek 'b', got %q", v)
	}
}

func TestSafeRingEachAndClear(t *testing.T) {
	r := NewSafeRing[int](4)
	r.Push(10)
	r.Push(20)
	r.Push(30)

	var collected []int
	r.Each(func(v int) bool {
		collected = append(collected, v)
		return true
	})
	want := []int{10, 20, 30}
	if !sliceEqual(collected, want) {
		t.Fatalf("got %v, want %v", collected, want)
	}

	r.Clear()
	if r.Len() != 0 {
		t.Fatalf("expected len 0 after clear, got %d", r.Len())
	}
}

func TestSafeRingConcurrent(t *testing.T) {
	r := NewSafeRing[int](100)
	done := make(chan struct{})

	// Writers
	for w := range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := range 250 {
				r.Push(w*1000 + i)
			}
		}()
	}

	// Readers
	for range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 250 {
				r.Slice()
				r.Len()
				r.Peek()
				r.Full()
			}
		}()
	}

	for range 8 {
		<-done
	}

	if r.Len() != 100 {
		t.Fatalf("expected len 100, got %d", r.Len())
	}
}

func sliceEqual[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
