# Structural Sharing for Metadata

## Summary

Replace `[]Step` and `[]Hint` in `Meta` with a generic `Log[T]` type that uses
structural sharing. Fork is O(1) (one pointer allocation), append is O(1)
amortized, and materialization to a flat slice happens only when the caller
explicitly requests it. This eliminates the O(N^2) trace cloning cost in deep
pipelines and compositions.

## Motivation

In a pipeline of N nodes, each `runOnce` call clones the full trace (O(K)
steps) twice. As the trace grows with each node, total copy work is O(N^2).
Benchmarks show Pipeline/10 at 5,798ns and 41KB — most of that is cloning.

For production agents composing 50+ nodes across retries, sub-chains, and tool
calls, this scaling becomes meaningful even though the absolute numbers are
small relative to real work (LLM calls, API calls).

## Design

### `Log[T]`

An exported generic type in the `reflow` package. Provides append-only
structural sharing via a linked list of segments.

```go
type Log[T any] struct {
    steps []T
    tail  *Log[T]
    total int
}
```

Fields are unexported. The type is public.

**Operations:**

`fork() Log[T]` — Freezes the current state by pushing it into a tail pointer.
Returns a new Log with an empty local segment and the frozen state as tail. One
pointer allocation. O(1). If the log is empty, returns the value unchanged (no
allocation).

`append(items ...T) Log[T]` — Adds items to the local segment. Since the local
segment is exclusively owned (tail is shared/immutable), no aliasing is
possible. O(1) amortized.

Both `fork` and `append` are unexported — they are internal operations used by
the framework (`runOnce`, `WithStep`, `WithHint`, etc.). Users don't call them.

**Public methods:**

```go
func (l Log[T]) Slice() []T       // Materialize to flat slice. O(N), allocates.
func (l Log[T]) Each(fn func(T))  // Zero-alloc iteration, oldest to newest.
func (l Log[T]) Len() int         // Cached total count. O(1).
```

`Slice()` allocates once (`make([]T, 0, total)`) and walks the linked structure
tail-first to preserve chronological order.

`Each` walks tail-first recursively, calling `fn` for each element. No
allocation.

### `Meta` Changes

```go
type Meta struct {
    Hints Log[Hint]
    Trace Log[Step]
    Tags  map[string]string
}
```

`Hints` and `Trace` change from `[]Hint` / `[]Step` to `Log[Hint]` / `Log[Step]`.
`Tags` stays as `map[string]string` — its access patterns are different (keyed
lookup, not append-only).

### Internal Changes

**`cloneTrace` and `cloneHints` are removed.** Replaced by `fork()`:

`runOnce` (run.go:31,45):
```go
// Before
resolved.Meta.Trace = append(cloneTrace(resolved.Meta.Trace), Step{...})

// After
resolved.Meta.Trace = resolved.Meta.Trace.fork().append(Step{...})
```

Same pattern for `retryNode.Run`, `Stream`, `WithStep`, `WithHint`.

**`retryNode.Run`** uses `Len()` as the baseline offset and `Slice()` to
extract new steps when needed. Materialization happens once per retry iteration
(acceptable — retries are capped in practice).

**`ForkJoin`** calls `fork()` on both `Trace` and `Hints` of the input envelope
before dispatching goroutines. This ensures all goroutines share the frozen
prefix and diverge safely on first append. The `fork()` happens before goroutine
creation, so Go's memory model guarantees visibility without atomics.

**`NewEnvelope`** returns an envelope with zero-value `Log` fields (no
allocation). The `Tags` map is still allocated eagerly.

### User-Facing API Changes

This is a breaking change. `Meta.Trace` and `Meta.Hints` change type.

Before:
```go
for _, s := range out.Meta.Trace {
    fmt.Println(s.Phase)
}
```

After:
```go
// Zero-alloc iteration
out.Meta.Trace.Each(func(s reflow.Step) {
    fmt.Println(s.Phase)
})

// Or materialize when a slice is needed
steps := out.Meta.Trace.Slice()
for _, s := range steps { ... }

// Length
n := out.Meta.Trace.Len()
```

### `WithStep` / `WithHint`

```go
func (e Envelope[T]) WithStep(steps ...Step) Envelope[T] {
    e.Meta.Trace = e.Meta.Trace.fork().append(steps...)
    return e
}

func (e Envelope[T]) WithHint(code, message, span string) Envelope[T] {
    e.Meta.Hints = e.Meta.Hints.fork().append(Hint{...})
    return e
}
```

`WithTag` is unchanged (map copy semantics remain).

### `HintsByCode`

Currently iterates `Meta.Hints` directly. Updated to use `Each`:

```go
func (e Envelope[T]) HintsByCode(code string) []Hint {
    var out []Hint
    e.Meta.Hints.Each(func(h Hint) {
        if h.Code == code {
            out = append(out, h)
        }
    })
    return out
}
```

### OTEL Integration

`reflowtel` currently reads `Meta.Trace` and `Meta.Hints` as slices. Updated to
use `Each` for span creation and metric recording, or `Slice()` where a flat
view is needed. The materialization cost moves from the pipeline hot path to the
export path — a better place for it.

### Performance Model

| Operation | Before | After |
|---|---|---|
| Fork (cloneTrace) | O(N) — full copy | O(1) — pointer alloc |
| Append (WithStep) | O(N) — clone + append | O(1) — append to local |
| Pipeline of K nodes | O(K^2) total copies | O(K) total pointer allocs |
| Materialize (Slice) | Free (already a slice) | O(N) — one-time build |
| Iterate (Each) | Free (range over slice) | O(N) — pointer chase |
| Length | O(1) — len() | O(1) — cached total |

The cost shifts from the hot path (every node execution) to the cold path
(export/inspection). In production agents where nodes do real work (LLM calls,
API calls), the hot-path savings dominate.

### Testing

- Existing tests adapted to use `Slice()` / `Each` / `Len()` instead of direct
  slice access.
- Benchmarks (`bench_test.go`) re-run to measure improvement. Expected:
  Pipeline/10 drops significantly in both ns/op and allocs/op.
- New unit tests for `Log[T]`: fork semantics, append after fork, nested forks,
  Each ordering, Slice correctness, Len accuracy, empty log behavior.
- Verify ForkJoin correctness under concurrent fork+append (race detector).

### Scope

This prototype lives on a branch. It changes:
- `envelope.go` — `Log[T]` type, updated `Meta`, updated `WithStep`/`WithHint`/`HintsByCode`
- `run.go` — `fork().append()` replaces `cloneTrace`
- `stream.go` — same
- `bench_test.go` — re-run for comparison
- All test files that access `Meta.Trace` or `Meta.Hints` directly
- `otel/` — updated to use `Each`/`Slice` methods

Core reflow package has no new dependencies.
