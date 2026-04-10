package reflow

// Log is an append-only sequence with structural sharing. Fork is O(1)
// and append is O(1) amortized, eliminating the O(N) clone cost that
// a flat slice would incur on every handoff.
//
// Callers interact through Slice, Each, and Len. The zero value is an
// empty log ready to use.
type Log[T any] struct {
	steps []T
	tail  *Log[T]
	total int
}

// LogFrom creates a Log from an existing slice.
func LogFrom[T any](items []T) Log[T] {
	if len(items) == 0 {
		return Log[T]{}
	}
	return Log[T]{steps: items, total: len(items)}
}

// Slice materializes the log into a flat slice, oldest to newest.
// Returns nil for an empty log.
func (l Log[T]) Slice() []T {
	if l.total == 0 {
		return nil
	}
	out := make([]T, 0, l.total)
	l.collect(&out)
	return out
}

// Each calls fn for every element, oldest to newest, without allocating.
func (l Log[T]) Each(fn func(T)) {
	if l.tail != nil {
		l.tail.Each(fn)
	}
	for _, v := range l.steps {
		fn(v)
	}
}

// Len returns the total number of elements across all segments. O(1).
func (l Log[T]) Len() int {
	return l.total
}

// Since returns elements from offset onward as a flat slice.
// Used by the retry loop to extract steps added after a baseline.
func (l Log[T]) Since(offset int) []T {
	if offset >= l.total {
		return nil
	}
	all := l.Slice()
	return all[offset:]
}

// fork pushes the current state into a tail segment and returns
// a new Log with an empty head. One pointer allocation, O(1).
func (l Log[T]) fork() Log[T] {
	if l.total == 0 {
		return l
	}
	return Log[T]{tail: &l, total: l.total}
}

// append adds items to the local segment. O(1) amortized.
func (l Log[T]) append(items ...T) Log[T] {
	l.steps = append(l.steps, items...)
	l.total += len(items)
	return l
}

func (l Log[T]) collect(out *[]T) {
	if l.tail != nil {
		l.tail.collect(out)
	}
	*out = append(*out, l.steps...)
}
