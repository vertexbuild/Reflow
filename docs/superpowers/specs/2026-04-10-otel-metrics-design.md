# OTEL Metrics Support for reflowtel

## Summary

Add OpenTelemetry metrics to the existing `reflowtel` package, providing a full
observability surface (pipeline execution, tool calls, retries, hints) that
works with any OTEL-compatible backend. Prometheus support comes for free via
`go.opentelemetry.io/otel/exporters/prometheus`.

This is a breaking change to the `reflowtel` API. The three standalone functions
(`RunWithTrace`, `ExportTrace`, `WrapTool`) are replaced by an `Instrumenter`
struct with a consistent configuration surface. Metrics are opt-in: if no
`MeterProvider` is configured, the Instrumenter emits traces only, preserving
today's behavior.

## Motivation

Observability is hard for agent/workflow systems. Reflow already captures rich
structured trace data in `Meta` (Steps, Hints, Tags), making it straightforward
to expose as metrics without core changes. A full metrics surface lets users
build Grafana dashboards answering: how are my pipelines performing, how are my
external dependencies behaving, and how often are feedback loops firing.

## Design

### Instrumenter

A configured, reusable object that holds tracing and optional metrics state:

```go
type Instrumenter struct {
    tracer  trace.Tracer
    meter   metric.Meter       // nil if no MeterProvider given
    tags    map[string]bool    // allowed tag keys for metric attributes
    tagFunc TagFunc            // optional transform, overrides allowlist

    // pre-created instruments (nil when meter is nil)
    runDuration   metric.Float64Histogram
    runCount      metric.Int64Counter
    stepDuration  metric.Float64Histogram
    stepCount     metric.Int64Counter
    toolDuration  metric.Float64Histogram
    toolCount     metric.Int64Counter
    retryCount    metric.Int64Counter
    hintCount     metric.Int64Counter
}

type TagFunc func(tags map[string]string) []attribute.KeyValue

func NewInstrumenter(tp trace.TracerProvider, opts ...Option) *Instrumenter
```

Options:

```go
func WithMeter(mp metric.MeterProvider) Option
func WithAllowedTags(keys ...string) Option
func WithTagFunc(fn TagFunc) Option
```

- Instruments are created eagerly in `NewInstrumenter` so `Run`/`WrapTool` never
  allocate.
- If no `WithMeter` is passed, all metric fields stay nil and recording is a
  no-op.
- `WithTagFunc` overrides `WithAllowedTags` (not additive). When provided, the
  user takes full control of tag-to-attribute mapping.
- `WithAllowedTags` builds a simple filter that passes matching `Meta.Tags`
  through as `reflow.tag.<key>` attributes.

### Function Signatures

Generic functions stay package-level (Go methods cannot have type parameters).
`Export` is non-generic and lives as a method:

```go
func Run[I, O any](ctx context.Context, inst *Instrumenter, name string,
    n reflow.Node[I, O], env reflow.Envelope[I]) (reflow.Envelope[O], error)

func WrapTool[I, O any](inst *Instrumenter, name string,
    t reflow.Tool[I, O]) reflow.Tool[I, O]

func (inst *Instrumenter) Export(ctx context.Context, m reflow.Meta) error
```

### Metrics

All metrics use the `reflow.` prefix. Units follow OTEL conventions (seconds
for durations).

| Metric | Type | Emitted by | Attributes |
|---|---|---|---|
| `reflow.run.duration` | Float64Histogram | `Run` | `node`, `status`, + allowed tags |
| `reflow.run.count` | Int64Counter | `Run` | `node`, `status`, + allowed tags |
| `reflow.step.duration` | Float64Histogram | `Export` | `node`, `phase`, `status`, + allowed tags |
| `reflow.step.count` | Int64Counter | `Export` | `node`, `phase`, `status`, + allowed tags |
| `reflow.tool.duration` | Float64Histogram | `Export`, `WrapTool` | `tool`, `status`, + allowed tags |
| `reflow.tool.count` | Int64Counter | `Export`, `WrapTool` | `tool`, `status`, + allowed tags |
| `reflow.retry.count` | Int64Counter | `Export` | `node`, + allowed tags |
| `reflow.hint.count` | Int64Counter | `Export` | `hint.code`, + allowed tags |

Attribute values:

- `node`: the node name from `Step.Node`
- `phase`: `resolve`, `act`, `settle`, or `tool`
- `status`: `ok`, `retry`, or `error` (any non-ok/non-retry status is
  normalized to `error` for metric cardinality; the original status string is
  preserved on spans)
- `tool`: the tool name (from `Step.Node` when `Step.Phase == "tool"`, or the
  name passed to `WrapTool`)
- `hint.code`: the semantic hint code (e.g., `json.malformed`)
- Allowed tags: `reflow.tag.<key>` for each key in the allowlist (or as
  returned by `TagFunc`)

### Recording Behavior

`Run` executes the node, then calls `Export` internally. A single `Run` call
emits both top-level run metrics and all step/tool/retry/hint metrics from the
accumulated trace.

`Export` is exposed separately for users who execute pipelines themselves and
export post-hoc.

`WrapTool` records tool metrics live on each call, independent of `Export`. If
both are used, tool metrics may be recorded twice. This is expected: `WrapTool`
gives live per-call observability, while `Export` replays from the trace. Users
choose one or both depending on their needs.

### Tag-to-Attribute Mapping

Prometheus cardinality is a real concern. Tags are not automatically promoted to
metric labels.

**Allowlist (default):** Pass `WithAllowedTags("entity_type", "org_id")` to
promote specific tag keys. Only low-cardinality categorical values should be
allowed. UUIDs and other high-cardinality values must be excluded.

**TagFunc (power users):** Pass `WithTagFunc(fn)` for full control over
selection and transformation. Overrides the allowlist entirely.

Example with domain context:

```go
inst := reflowtel.NewInstrumenter(tp,
    reflowtel.WithMeter(mp),
    reflowtel.WithAllowedTags("entity_type", "org_id"),
)

// entity_type and org_id become metric labels
// entity_id, order_id, actor_id stay in traces only
```

### Tracing Behavior

Tracing behavior is unchanged from today:

- `Run` creates a parent span for the pipeline, child spans for each step
- `Export` creates spans from `Meta.Trace` post-hoc
- `WrapTool` creates a span per tool call
- `Meta.Tags` map to span attributes (prefixed `reflow.tag.*`)
- `Meta.Hints` map to span events
- Error status set for non-ok/non-retry step statuses

### River/Outbox Considerations

The Instrumenter does not interfere with future River trace context propagation:

- **Allowlist is safe by default.** If River stores trace context in reserved
  tags (e.g., `_otel.traceparent`), those never become metric labels unless
  explicitly added to the allowlist.
- **No transport coupling.** The Instrumenter reads `context.Context` and `Meta`,
  nothing more. River integration can inject/extract span context independently
  using its own `TracerProvider` access.

Trace context propagation across the River queue boundary is out of scope for
this work and belongs in the River package.

## Migration

This is a breaking change. The old API maps to the new API as follows:

```go
// Before
reflowtel.RunWithTrace(ctx, tp, "name", node, env)
reflowtel.ExportTrace(ctx, tp, meta)
reflowtel.WrapTool(tp, "name", tool)

// After (tracing only, equivalent behavior)
inst := reflowtel.NewInstrumenter(tp)
reflowtel.Run(ctx, inst, "name", node, env)
inst.Export(ctx, meta)
reflowtel.WrapTool(inst, "name", tool)

// After (tracing + metrics)
inst := reflowtel.NewInstrumenter(tp,
    reflowtel.WithMeter(mp),
    reflowtel.WithAllowedTags("entity_type", "org_id"),
)
reflowtel.Run(ctx, inst, "name", node, env)
inst.Export(ctx, meta)
reflowtel.WrapTool(inst, "name", tool)
```

## Testing

- Verify all 8 metrics are recorded with correct attributes using OTEL SDK's
  in-memory exporter (`sdkmetric.NewManualReader`)
- Verify tracing behavior is unchanged (existing tests adapted to new API)
- Verify no metrics are recorded when `WithMeter` is not configured
- Verify `WithAllowedTags` filters correctly (only allowed tags appear as
  metric attributes)
- Verify `WithTagFunc` overrides allowlist behavior
- Verify `WrapTool` records tool metrics independently of `Export`

## File Structure

- `otel/instrumenter.go` — `Instrumenter`, `NewInstrumenter`, `Option`, `TagFunc`
- `otel/trace.go` — span creation logic (extracted from current `otel.go`)
- `otel/metric.go` — metric instrument creation and recording helpers
- `otel/run.go` — `Run` function
- `otel/tool.go` — `WrapTool` function and `tracedTool` wrapper
- `otel/otel_test.go` — updated tests

## Dependencies

Added to `otel/go.mod`:

```
go.opentelemetry.io/otel/metric v1.42.0
go.opentelemetry.io/otel/sdk/metric v1.42.0  // for tests
```

No changes to the core `reflow` module.
