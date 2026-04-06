package reflow

import "sync"

// Ring is a fixed-size sliding window buffer. It gives streaming nodes
// cheap access to recent history for pattern detection, frequency counting,
// or windowed aggregation — without holding the entire stream in memory.
//
//	ring := reflow.NewRing[Event](100)
//
//	// Inside a StreamNode's Act:
//	ring.Push(event)
//	recent := ring.Slice()  // last N items in order
//
// Ring is not safe for concurrent use. Use it within a single node's
// Act or Settle phase, not across goroutines.
type Ring[T any] struct {
	buf   []T
	size  int
	pos   int // next write position
	count int // total items pushed (capped at size for len purposes)
}

// NewRing creates a ring buffer that holds at most n items.
// Older items are silently overwritten as new ones are pushed.
func NewRing[T any](n int) *Ring[T] {
	if n <= 0 {
		n = 1
	}
	return &Ring[T]{
		buf:  make([]T, n),
		size: n,
	}
}

// Push adds an item to the ring, overwriting the oldest if full.
func (r *Ring[T]) Push(v T) {
	r.buf[r.pos] = v
	r.pos = (r.pos + 1) % r.size
	if r.count < r.size {
		r.count++
	}
}

// Len returns the number of items currently in the ring (up to capacity).
func (r *Ring[T]) Len() int {
	return r.count
}

// Cap returns the maximum number of items the ring can hold.
func (r *Ring[T]) Cap() int {
	return r.size
}

// Full returns true if the ring is at capacity.
func (r *Ring[T]) Full() bool {
	return r.count == r.size
}

// Slice returns the contents of the ring in chronological order
// (oldest first). The returned slice is a copy — safe to hold.
func (r *Ring[T]) Slice() []T {
	out := make([]T, r.count)
	if r.count < r.size {
		// Not yet full — data starts at 0.
		copy(out, r.buf[:r.count])
	} else {
		// Full — oldest is at r.pos, wraps around.
		n := copy(out, r.buf[r.pos:])
		copy(out[n:], r.buf[:r.pos])
	}
	return out
}

// Peek returns the most recently pushed item and true,
// or the zero value and false if the ring is empty.
func (r *Ring[T]) Peek() (T, bool) {
	if r.count == 0 {
		var zero T
		return zero, false
	}
	idx := (r.pos - 1 + r.size) % r.size
	return r.buf[idx], true
}

// Each iterates over the ring contents in chronological order.
// The callback receives each item; return false to stop early.
func (r *Ring[T]) Each(fn func(T) bool) {
	if r.count == 0 {
		return
	}
	start := 0
	if r.count == r.size {
		start = r.pos
	}
	for i := range r.count {
		idx := (start + i) % r.size
		if !fn(r.buf[idx]) {
			return
		}
	}
}

// Clear resets the ring to empty without reallocating.
func (r *Ring[T]) Clear() {
	var zero T
	for i := range r.buf {
		r.buf[i] = zero
	}
	r.pos = 0
	r.count = 0
}

// SafeRing is a concurrency-safe version of Ring, protected by a
// read-write mutex. Use this when a ring is shared across goroutines,
// such as a shared accumulator in pooled workers.
//
//	ring := reflow.NewSafeRing[Event](100)
//
//	// Safe to call from multiple goroutines:
//	ring.Push(event)
//	recent := ring.Slice()
type SafeRing[T any] struct {
	mu sync.RWMutex
	r  Ring[T]
}

// NewSafeRing creates a concurrency-safe ring buffer that holds at most n items.
func NewSafeRing[T any](n int) *SafeRing[T] {
	if n <= 0 {
		n = 1
	}
	return &SafeRing[T]{
		r: Ring[T]{
			buf:  make([]T, n),
			size: n,
		},
	}
}

// Push adds an item to the ring, overwriting the oldest if full.
func (s *SafeRing[T]) Push(v T) {
	s.mu.Lock()
	s.r.Push(v)
	s.mu.Unlock()
}

// Len returns the number of items currently in the ring.
func (s *SafeRing[T]) Len() int {
	s.mu.RLock()
	n := s.r.Len()
	s.mu.RUnlock()
	return n
}

// Cap returns the maximum number of items the ring can hold.
func (s *SafeRing[T]) Cap() int {
	return s.r.Cap() // immutable after construction
}

// Full returns true if the ring is at capacity.
func (s *SafeRing[T]) Full() bool {
	s.mu.RLock()
	f := s.r.Full()
	s.mu.RUnlock()
	return f
}

// Slice returns the contents in chronological order. The returned slice is a copy.
func (s *SafeRing[T]) Slice() []T {
	s.mu.RLock()
	out := s.r.Slice()
	s.mu.RUnlock()
	return out
}

// Peek returns the most recently pushed item and true, or the zero value and false if empty.
func (s *SafeRing[T]) Peek() (T, bool) {
	s.mu.RLock()
	v, ok := s.r.Peek()
	s.mu.RUnlock()
	return v, ok
}

// Each iterates over the ring contents in chronological order.
// The callback receives each item; return false to stop early.
// The ring is read-locked for the duration of the iteration.
func (s *SafeRing[T]) Each(fn func(T) bool) {
	s.mu.RLock()
	s.r.Each(fn)
	s.mu.RUnlock()
}

// Clear resets the ring to empty without reallocating.
func (s *SafeRing[T]) Clear() {
	s.mu.Lock()
	s.r.Clear()
	s.mu.Unlock()
}
