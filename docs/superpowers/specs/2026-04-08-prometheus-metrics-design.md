# Prometheus & OTel Metrics for Reflow

## Summary

Add metrics support to reflow through two complementary paths:

1. **`reflow/prometheus`** — new module, native Prometheus client integration
2. **`reflow/otel` additions** — extend the existing tracing module with OTel metrics, exportable to Prometheus via the standard OTel-to-Prometheus bridge

Both modules follow the same pattern established by the existing `reflow/otel` tracing integration: post-hoc export from `Meta.Trace`, an instrumented `Run` wrapper, and a `WrapTool` for live per-call metrics.

## Motivation

Reflow's graph structure captures execution data automatically — every tool call, phase transition, and retry is recorded in `Meta.Trace`. This data is already projected into OTel spans via `reflow/otel`. Metrics are the natural complement: counters, histograms, and rates derived from the same trace data, surfaced on dashboards for operational monitoring and agent efficiency tracking.

A primary use case is LLM agent workloads built with `WithRetry` and `Compose`, where metrics like tool call frequency, retry pressure, and steps-per-run are directly useful for cost/latency budgeting and regression detection.

## Approach

Mirror the existing `reflow/otel` pattern. No changes to core.

- **Post-hoc export** — after `Run` completes, walk `Meta.Trace` and record metrics
- **Instrumented runner** — `RunWithMetrics` wraps `Run` and exports automatically
- **Tool wrappers** — `WrapTool` / `WrapToolWithMetrics` emit metrics live per call, important for long-running agent loops where post-hoc export would delay visibility

This was chosen over alternatives (core hooks, per-node wrappers) because it's consistent with the existing extension pattern, requires no core changes, and the "metrics land after completion" limitation is acceptable for most workflows. For long-running graphs, `WrapTool` provides live metrics where it matters most (external calls like LLMs).

## Metrics

All metrics use the same names and labels across both modules (Prometheus uses underscores, OTel uses dots per convention).

| Metric | Type | Labels | Source |
|--------|------|--------|--------|
| `reflow_step_duration_seconds` | Histogram | `node`, `phase`, `status` | Each `Step` in `Meta.Trace` |
| `reflow_step_total` | Counter | `node`, `phase`, `status` | Each `Step` in `Meta.Trace` |
| `reflow_run_duration_seconds` | Histogram | `graph` | Wall clock time of `RunWithMetrics` |
| `reflow_run_total` | Counter | `graph`, `status` | One per `RunWithMetrics` completion |
| `reflow_run_steps` | Histogram | `graph` | `len(meta.Trace)` per completion |
| `reflow_hints_total` | Counter | `graph`, `code` | Each `Hint` in `Meta.Hints` |

### Label definitions

- **`node`** — tool name from `Step.Node` (set by `Use`, `UseRetry`, `Invoke`, `Try`). Empty for resolve/settle steps; see Considerations below.
- **`phase`** — `Step.Phase`: `resolve`, `act`, `settle`, or `tool`
- **`status`** — normalized from `Step.Status`: `ok`, `retry`, or `error`. Raw error strings are not used as label values to avoid unbounded cardinality; error detail belongs in traces.
- **`graph`** — user-provided name for the run (analogous to `spanName` in the otel module)
- **`code`** — `Hint.Code` (e.g., `json.malformed`, `tone.mismatch`)

### Metric rationale

- **`reflow_step_*`** — per-step granularity. Tool calls (`phase="tool"`) are the high-value signal for agents: LLM latency percentiles, tool call frequency, error rates.
- **`reflow_run_*`** — per-envelope granularity. Throughput (`rate(reflow_run_total)`), success rate, and overall latency.
- **`reflow_run_steps`** — efficiency metric. "How much work did it take to process one input?" For agents: p50 steps = 5, p99 = 14 means your agent usually settles fast but sometimes struggles. If this creeps up over time, prompts may be degrading. This is a metric you'd write more than once across projects.
- **`reflow_hints_total`** — feedback loop pressure. Hints drive retry behavior in `WithRetry`; tracking them by code surfaces which failure modes are most common.

## Module 1: `reflow/prometheus`

New module at `./prometheus/`, added to `go.work`.

### Dependencies

- `github.com/prometheus/client_golang`
- `github.com/ploffredo/reflow`

### API

```go
package reflowprom

import (
    "context"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/ploffredo/reflow"
)

// Metrics holds pre-registered Prometheus metrics for reflow pipelines.
// Fields are exported so users can inspect or extend them (e.g., CurryWith).
type Metrics struct {
    StepDuration     *prometheus.HistogramVec
    StepTotal        *prometheus.CounterVec
    RunDuration      *prometheus.HistogramVec
    RunTotal         *prometheus.CounterVec
    RunSteps         *prometheus.HistogramVec
    HintsTotal       *prometheus.CounterVec
}

// NewMetrics creates and registers all metrics with the given registerer.
// Works with prometheus.DefaultRegisterer or a custom registry.
func NewMetrics(reg prometheus.Registerer) *Metrics

// ExportMetrics records metrics from a completed run's trace.
// Call after reflow.Run when not using RunWithMetrics.
//
//   out, err := reflow.Run(ctx, node, in)
//   reflowprom.ExportMetrics(m, "my-agent", out.Meta, err)
func ExportMetrics(m *Metrics, graph string, meta reflow.Meta, err error)

// RunWithMetrics executes a node via reflow.Run and records metrics.
// Measures wall clock duration for reflow_run_duration_seconds.
//
//   out, err := reflowprom.RunWithMetrics(ctx, m, "my-agent", agent, in)
func RunWithMetrics[I, O any](
    ctx context.Context, m *Metrics, graph string,
    n reflow.Node[I, O], in reflow.Envelope[I],
) (reflow.Envelope[O], error)

// WrapTool wraps a tool to emit live step metrics per call.
// Use for long-running graphs where post-hoc export delays visibility.
//
//   traced := reflowprom.WrapTool(m, myLLM)
//   resp, step, err := reflow.Use(ctx, traced, input)
func WrapTool[I, O any](m *Metrics, t reflow.Tool[I, O]) reflow.Tool[I, O]
```

### Design notes

- `Metrics` is a struct with exported fields, not an interface. Users can access the underlying `*prometheus.HistogramVec` / `*prometheus.CounterVec` for advanced use (custom labels, currying).
- `NewMetrics` takes `prometheus.Registerer` (not `*prometheus.Registry`) for flexibility.
- `RunWithMetrics` uses wall clock for run duration, not sum of step durations. This is more accurate for concurrent composition patterns like `ForkJoin`.

## Module 2: `reflow/otel` metrics additions

Extend the existing `reflow/otel` module. No new external dependencies — `go.opentelemetry.io/otel/metric` is already an indirect dependency.

### API additions

```go
package reflowtel

import (
    "context"

    "github.com/ploffredo/reflow"
    "go.opentelemetry.io/otel/metric"
    "go.opentelemetry.io/otel/trace"
)

// Metrics holds OTel metric instruments for reflow pipelines.
type Metrics struct {
    stepDuration metric.Float64Histogram
    stepTotal    metric.Int64Counter
    runDuration  metric.Float64Histogram
    runTotal     metric.Int64Counter
    runSteps     metric.Int64Histogram
    hintsTotal   metric.Int64Counter
}

// NewMetrics creates OTel metric instruments from a meter.
func NewMetrics(meter metric.Meter) (*Metrics, error)

// ExportMetrics records metrics from a completed run's trace.
func (m *Metrics) ExportMetrics(graph string, meta reflow.Meta, err error)

// RunWithMetrics executes a node and records metrics.
func RunWithMetrics[I, O any](
    ctx context.Context, m *Metrics, graph string,
    n reflow.Node[I, O], in reflow.Envelope[I],
) (reflow.Envelope[O], error)

// RunWithTelemetry executes a node and records both traces and metrics.
// Convenience for users who want full observability in one call.
func RunWithTelemetry[I, O any](
    ctx context.Context, tracer trace.Tracer, m *Metrics, graph string,
    n reflow.Node[I, O], in reflow.Envelope[I],
) (reflow.Envelope[O], error)

// WrapToolWithMetrics wraps a tool to emit live metrics per call.
// Separate from the existing WrapTool (which emits spans).
// Compose for both: WrapTool(tracer, WrapToolWithMetrics(m, tool))
func WrapToolWithMetrics[I, O any](m *Metrics, t reflow.Tool[I, O]) reflow.Tool[I, O]
```

### Design notes

- `NewMetrics` returns error because OTel instrument creation can fail (unlike Prometheus which panics on registration failure).
- `ExportMetrics` is a method on `*Metrics` rather than a free function. This differs from the prometheus module but is more natural given the fallible construction.
- `WrapToolWithMetrics` is separate from the existing `WrapTool` (spans). Users compose them for both: `WrapTool(tracer, WrapToolWithMetrics(m, tool))`. This avoids coupling the two concerns and keeps each wrapper simple.
- `RunWithTelemetry` is a convenience that combines `RunWithTrace` + `RunWithMetrics`. Users who want only one or the other use the individual functions.
- Same metric semantics as the prometheus module. OTel metric names use dots per convention (`reflow.step.duration`).

### Existing API unchanged

`ExportTrace`, `RunWithTrace`, and `WrapTool` remain as-is. The metrics additions are purely additive.

## Testing

### Both modules

- **`ExportMetrics` unit tests** — construct a `Meta` with known steps and hints, export, assert metric values match expected counts/observations. For prometheus: use `prometheus.NewRegistry()` + `testutil.ToFloat64()`. For otel: use the SDK metric test reader.
- **`WrapTool` unit tests** — wrap a fake tool, call it, assert step metrics recorded with correct labels (node name, phase, status).
- **`RunWithMetrics` integration tests** — run a real node (e.g., a `Func`), assert both run-level and step-level metrics land correctly.
- **Unnamed node label test** — run a `Chain` or `Compose` graph, assert that resolve/settle steps produce metrics with an empty `node` label. Documents the behavior explicitly.

### OTel-specific

- **`RunWithTelemetry` integration test** — assert both spans and metrics are emitted from a single call.

### Out of scope

End-to-end Prometheus scrape tests or OTel collector tests. That's infrastructure, not library concern.

## Considerations

### Unnamed resolve/settle steps

The `Node` interface has no `Name()` method — only `Tool` does. When `runOnce` records resolve and settle steps (`run.go:31`, `run.go:45`), they have no `Node` field:

```go
Step{Phase: "resolve", Status: "ok"}  // no Node
```

Tool steps are named because `Use()` sets `Node: t.Name()`.

**Implication for metrics:** `reflow_step_total{phase="resolve", node="", status="ok"}` aggregates all resolve phases across all nodes in a composed graph. You cannot distinguish which node's resolve was slow from metrics alone. The same applies to settle steps.

**Practical impact is low:** resolve and settle are typically cheap operations. The expensive work happens in Act, which produces tool steps that are named. For agents (single node with `WithRetry`), there's no ambiguity. For per-node phase breakdown, use traces (otel spans), not metrics.

**Workaround:** users can wrap expensive resolve logic as a tool call via `Invoke()`, which gives it a name in the trace.

This is a core design question (whether `Node` should have a `Name()` method) that is out of scope for the metrics modules.

### Status label cardinality

`Step.Status` is `"ok"`, `"retry"`, or an error string. Error strings as Prometheus labels risk unbounded cardinality. Both modules should normalize status to a bounded set: `"ok"`, `"retry"`, `"error"`. The original error string is available in traces, not metrics.

### Business metrics

The metrics modules provide operational metrics automatically. For business-level metrics (tokens consumed, tickets processed, domain-specific counters), users create their own metrics using the same registry/meter and use them directly in their node implementations. No special machinery is needed — this is standard Prometheus/OTel usage.

### Histogram bucket defaults

`reflow_step_duration_seconds` and `reflow_run_duration_seconds` should use sensible default buckets for the expected latency range. Tool calls (especially LLM calls) can take seconds; resolve/settle are typically sub-millisecond. The prometheus module should use `prometheus.DefBuckets` as a starting point, possibly with wider upper bounds. Users can override via `prometheus.HistogramOpts` if needed, since `Metrics` fields are exported.

`reflow_run_steps` needs integer-friendly buckets (1, 2, 3, 5, 8, 13, 21, ...) since it counts discrete steps.

## Open Questions

### Stream instrumentation

The current design covers `Run`. Streams (`Stream()`, `Pool()`, `Collect()`) process multiple envelopes and have different instrumentation needs — per-item metrics, backpressure visibility, in-flight gauges. This is deferred to a follow-up. The `WrapTool` pattern provides live metrics for tool calls within streams today.

### Composing tool wrappers

For the otel module, users who want both spans and metrics on a tool compose two wrappers: `WrapTool(tracer, WrapToolWithMetrics(m, tool))`. This works but adds nesting. If this proves cumbersome in practice, a combined `WrapToolWithTelemetry(tracer, m, tool)` could be added later. Start simple.

### Adding `Name()` to Node

If `Node` gained a `Name() string` method in core, resolve/settle steps could carry node identity, and metrics would get per-node granularity for all phases. This would be a breaking change to the core interface and is a separate design decision. The metrics modules work correctly without it.

### OTel-to-Prometheus exporter configuration

The otel module emits OTel metrics. Exporting them to Prometheus requires the user to configure `go.opentelemetry.io/otel/exporters/prometheus` in their application. This is standard OTel setup and not something the reflow module should own, but documentation/examples should show the wiring.
