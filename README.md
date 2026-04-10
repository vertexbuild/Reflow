# Reflow

Typed workflow graphs for Go with structured handoffs.

```
go get github.com/ploffredo/reflow
```

Zero core dependencies. See the [package documentation](https://pkg.go.dev/github.com/ploffredo/reflow) for the full API reference.

## Overview

Reflow models workflows as graphs of typed nodes. Each node has three phases:

- **Resolve** — inspect the incoming envelope, read hints from upstream, prepare context.
- **Act** — do the work: parse, transform, call an API, run inference.
- **Settle** — prepare the handoff for the next node. Return `done=true` to pass the result forward, or `done=false` with hints to signal the result isn't ready.

```go
type Node[I, O any] interface {
    Resolve(context.Context, Envelope[I]) (Envelope[I], error)
    Act(context.Context, Envelope[I]) (Envelope[O], error)
    Settle(ctx context.Context, in Envelope[I], out Envelope[O], actErr error) (Envelope[O], bool, error)
}
```

The **envelope** carries a typed value and accumulated context through the graph. Context includes hints (guidance for downstream nodes), trace steps (execution history), and tags (metadata).

## Nodes

Named type for reusable, testable nodes:

```go
type ParseJSON struct{}

func (ParseJSON) Resolve(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
    return in, nil
}

func (ParseJSON) Act(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[JSON], error) {
    var v JSON
    return reflow.Map(in, v), json.Unmarshal([]byte(in.Value), &v)
}

func (ParseJSON) Settle(_ context.Context, _ reflow.Envelope[string], out reflow.Envelope[JSON], actErr error) (reflow.Envelope[JSON], bool, error) {
    if actErr == nil {
        return out, true, nil
    }
    return out.WithHint("json.malformed", actErr.Error(), ""), true, nil
}
```

Inline closure for quick definitions:

```go
double := &reflow.Func[int, int]{
    ActFn: reflow.Pass(func(n int) int { return n * 2 }),
}
```

Both implement `Node[I, O]` and compose with everything below.

## Composition

```go
// Multi-step graph with branching, loops, and conditional logic.
intake := reflow.Compose[Request, Resolution]("intake",
    func(ctx context.Context, s *reflow.Steps, in reflow.Envelope[Request]) reflow.Envelope[Resolution] {
        triaged := reflow.Do(s, ctx, triage, in)
        switch triaged.Value.Department {
        case "billing":
            return reflow.Do(s, ctx, billingDept, triaged)
        default:
            return reflow.Do(s, ctx, escalation, triaged)
        }
    },
)

// Sequential pair with type change.
pipeline := reflow.Chain(parse, validate)

// Sequential, same type.
triage := reflow.Pipeline[Ticket]("triage", normalize, classify, enrich, score)

// Concurrent fan-out with merge.
enrich := reflow.ForkJoin(merge, lookupAccount, lookupHistory, lookupUsage)

// Settle loop with hint feedback.
classify := reflow.WithRetry(ClassifyIntent{LLM: provider}, 3)
```

## Streaming

`StreamNode` yields envelopes one at a time via `iter.Seq2`. Pull-based — stop ranging and the producer stops.

```go
stream := reflow.Stream(ctx, splitter, envelope)
urgent, standard := reflow.Split(stream, isUrgent)
results, err := reflow.Collect(reflow.Merge(
    reflow.Pool(ctx, incidentDesk, urgent, 2),
    reflow.Pool(ctx, supportDesk, standard, 8),
))
```

`Pool` processes items through a node with bounded concurrency, preserving emission order.

## Tools

The `Tool[I, O]` interface wraps external calls with automatic timing and trace recording:

```go
resp, step, err := reflow.Use(ctx, chat, messages)
out = out.WithStep(step)
```

`UseRetry`, `Invoke`, and `Try` provide retry and ad-hoc variants. Every call is recorded in the envelope's trace with name, duration, and status.

## Extensions

Optional modules in the same repo. Each is a separate Go module with its own dependencies.

| Module | Purpose |
|---|---|
| [`reflow/llm`](llm) | LLM provider interface with Ollama and Anthropic implementations |
| [`reflow/otel`](otel) | OpenTelemetry tracing and metrics for pipelines and tool calls |
| [`reflow/river/outbox`](river/outbox) | Transactional outbox for durable pipelines via Postgres and River |

```
go get github.com/ploffredo/reflow/llm
go get github.com/ploffredo/reflow/otel
go get github.com/ploffredo/reflow/river/outbox
```

## License

MIT
