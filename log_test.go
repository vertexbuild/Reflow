package reflow

import (
	"sync"
	"testing"
)

func TestLogEmpty(t *testing.T) {
	var l Log[int]
	if l.Len() != 0 {
		t.Fatalf("expected Len 0, got %d", l.Len())
	}
	if s := l.Slice(); s != nil {
		t.Fatalf("expected nil Slice, got %v", s)
	}
	// Each on empty should not call fn.
	l.Each(func(int) { t.Fatal("should not be called") })
}

func TestLogAppend(t *testing.T) {
	var l Log[int]
	l = l.append(1, 2, 3)
	if l.Len() != 3 {
		t.Fatalf("expected Len 3, got %d", l.Len())
	}
	got := l.Slice()
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("expected [1 2 3], got %v", got)
	}
}

func TestLogForkAppend(t *testing.T) {
	var l Log[int]
	l = l.append(1, 2)

	forked := l.fork().append(3)

	// Original unchanged.
	if l.Len() != 2 {
		t.Fatalf("original Len should be 2, got %d", l.Len())
	}
	orig := l.Slice()
	if len(orig) != 2 || orig[0] != 1 || orig[1] != 2 {
		t.Fatalf("original should be [1 2], got %v", orig)
	}

	// Forked version has all three.
	if forked.Len() != 3 {
		t.Fatalf("forked Len should be 3, got %d", forked.Len())
	}
	forkSlice := forked.Slice()
	if len(forkSlice) != 3 || forkSlice[0] != 1 || forkSlice[1] != 2 || forkSlice[2] != 3 {
		t.Fatalf("forked should be [1 2 3], got %v", forkSlice)
	}
}

func TestLogNestedForks(t *testing.T) {
	var l Log[int]
	l = l.append(1)
	l = l.fork().append(2)
	l = l.fork().append(3)

	if l.Len() != 3 {
		t.Fatalf("expected Len 3, got %d", l.Len())
	}
	got := l.Slice()
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("expected [1 2 3], got %v", got)
	}
}

func TestLogEachOrdering(t *testing.T) {
	var l Log[int]
	l = l.append(1, 2)
	l = l.fork().append(3, 4)
	l = l.fork().append(5)

	var got []int
	l.Each(func(v int) { got = append(got, v) })

	if len(got) != 5 {
		t.Fatalf("expected 5 items, got %d", len(got))
	}
	for i, want := range []int{1, 2, 3, 4, 5} {
		if got[i] != want {
			t.Fatalf("index %d: expected %d, got %d", i, want, got[i])
		}
	}
}

func TestLogSince(t *testing.T) {
	var l Log[int]
	l = l.append(10, 20, 30, 40, 50)

	got := l.Since(3)
	if len(got) != 2 || got[0] != 40 || got[1] != 50 {
		t.Fatalf("expected [40 50], got %v", got)
	}

	// Since beyond length returns nil.
	if s := l.Since(10); s != nil {
		t.Fatalf("expected nil, got %v", s)
	}

	// Since at zero returns all.
	all := l.Since(0)
	if len(all) != 5 {
		t.Fatalf("expected 5 items, got %d", len(all))
	}
}

func TestLogFrom(t *testing.T) {
	items := []int{1, 2, 3}
	l := LogFrom(items)
	if l.Len() != 3 {
		t.Fatalf("expected Len 3, got %d", l.Len())
	}
	got := l.Slice()
	for i, want := range items {
		if got[i] != want {
			t.Fatalf("index %d: expected %d, got %d", i, want, got[i])
		}
	}
}

func TestLogFromEmpty(t *testing.T) {
	l := LogFrom[int](nil)
	if l.Len() != 0 {
		t.Fatalf("expected Len 0, got %d", l.Len())
	}
}

func TestLogForkEmptyIsNoop(t *testing.T) {
	var l Log[int]
	forked := l.fork()
	if forked.Len() != 0 {
		t.Fatalf("expected Len 0, got %d", forked.Len())
	}
	// Appending to fork of empty should work.
	forked = forked.append(1)
	if forked.Len() != 1 {
		t.Fatalf("expected Len 1, got %d", forked.Len())
	}
}

func TestLogConcurrentFork(t *testing.T) {
	var l Log[int]
	l = l.append(1, 2, 3)

	var wg sync.WaitGroup
	results := make([][]int, 8)

	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			forked := l.fork().append(i + 10)
			results[i] = forked.Slice()
		}()
	}
	wg.Wait()

	// Each goroutine should see [1, 2, 3, 10+i].
	for i, r := range results {
		if len(r) != 4 {
			t.Fatalf("goroutine %d: expected 4 items, got %d: %v", i, len(r), r)
		}
		if r[0] != 1 || r[1] != 2 || r[2] != 3 {
			t.Fatalf("goroutine %d: prefix should be [1 2 3], got %v", i, r[:3])
		}
		if r[3] != i+10 {
			t.Fatalf("goroutine %d: expected last item %d, got %d", i, i+10, r[3])
		}
	}

	// Original unchanged.
	if l.Len() != 3 {
		t.Fatalf("original should be unchanged, Len=%d", l.Len())
	}
}
