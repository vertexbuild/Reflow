# Reflow Benchmark Suite

## Summary

Add a comprehensive benchmark suite in a single `bench_test.go` file in the
root package. Establishes a performance baseline for framework overhead and
provides a raw Go comparison to quantify the cost of reflow's abstractions.

## Motivation

Reflow has zero benchmarks today. Without a baseline there's no way to detect
performance regressions or make honest claims about framework overhead. The
benchmark suite serves two purposes:

1. **Internal baseline** — track `ns/op` and `allocs/op` over time, catch
   regressions in CI or local development.
2. **Honest positioning** — show what reflow's typed workflow abstraction
   actually costs relative to the same work done with plain Go functions and
   goroutines.

## Design

### File

`bench_test.go` in the root `reflow` package. All benchmarks in one place.
Run with `go test -bench=. -benchmem`.

### Benchmark Categories

#### Core Execution

Measures framework overhead per operation. Act functions do trivial work
(integer multiply, string conversion) so framework cost dominates.

| Benchmark | What it measures | Parameterized |
|---|---|---|
| `BenchmarkRun` | Single node resolve/act/settle + trace cloning | No |
| `BenchmarkPipeline` | Sequential same-type nodes, trace cloning at scale | nodes=1,3,5,10 |
| `BenchmarkChain` | Two-node type-bridging composition (int→string→int) | No |
| `BenchmarkForkJoin` | Concurrent fan-out + merge overhead | nodes=2,4,8 |
| `BenchmarkWithRetry` | Retry loop with trace accumulation | iterations=1,3,5 |

#### Streaming

Measures throughput under concurrency.

| Benchmark | What it measures | Parameterized |
|---|---|---|
| `BenchmarkPool` | Pool throughput and lock overhead | concurrency=1,4,8 |
| `BenchmarkStreamCollect` | StreamNode producing items, collected to slice | items=10,100 |

#### Metadata

Isolates allocation costs for envelope operations.

| Benchmark | What it measures | Parameterized |
|---|---|---|
| `BenchmarkNewEnvelope` | Baseline envelope creation | No |
| `BenchmarkWithTag` | Map rebuild cost per tag addition | existing=0,5,10 |
| `BenchmarkWithHint` | Slice clone cost per hint addition | existing=0,5,10 |
| `BenchmarkWithStep` | Trace clone scaling per step addition | existing=0,10,50 |

#### Raw Go Comparison

Same work done with plain functions and goroutines, no framework.

| Benchmark | Reflow equivalent |
|---|---|
| `BenchmarkRawFunc` | `BenchmarkRun` |
| `BenchmarkRawPipeline` | `BenchmarkPipeline` |
| `BenchmarkRawForkJoin` | `BenchmarkForkJoin` |
| `BenchmarkRawPool` | `BenchmarkPool` |

### Design Principles

- **Trivial Act work** — framework overhead dominates, not application logic.
- **Sub-benchmarks** — `b.Run("nodes=5", ...)` for parameterized variants.
- **No I/O or randomness** — deterministic, repeatable.
- **`b.ReportAllocs()`** on every benchmark.
- **Paired naming** — each raw Go benchmark corresponds to a reflow benchmark
  so numbers are directly comparable.

### Expected Output Shape

```
BenchmarkRun                     5000000    240 ns/op    3 allocs/op
BenchmarkRawFunc                10000000    12 ns/op     0 allocs/op
BenchmarkPipeline/nodes=1        5000000    250 ns/op    3 allocs/op
BenchmarkPipeline/nodes=5        1000000   1400 ns/op   15 allocs/op
BenchmarkRawPipeline/nodes=5    10000000    60 ns/op     0 allocs/op
...
```

The numbers above are illustrative, not predictions. Actual values will be
established by running the benchmarks.
