# Reflow

Typed workflow graphs for Go — deterministic pipelines, streaming fan-outs, or agentic compositions with structured handoffs.

```
go get github.com/vertexbuild/reflow
```

Zero core dependencies. The entire public API fits on one screen.

---

## The graph is the control plane

**You write the graph.** The graph encodes the correct sequence of operations — not the model. Each node does one thing well and settles a prepared handoff for whatever comes next. The LLM, or any external tool, receives structured context and focuses on the one task it's best at.

```go
support := reflow.Compose[Ticket, Draft]("support",
    func(ctx context.Context, s *reflow.Steps, in reflow.Envelope[Ticket]) reflow.Envelope[Draft] {
        triaged  := reflow.Do(s, ctx, triage, in)
        enriched := reflow.Do(s, ctx, enrich, triaged)   // ForkJoin: account + history
        return reflow.Do(s, ctx, generate, enriched)      // Tool call with retry
    },
)

out, err := reflow.Run(ctx, support, reflow.NewEnvelope(ticket))
```

```
$ go run ./examples/support_agent/

Ticket:   TKT-4401
Customer: Northwind Health
Intent:   technical
Priority: high

Response: I can see the outage you're reporting. Our team is actively
investigating and I've linked your ticket to the incident.

Tool calls: 1
Hints:      5
Trace:      14 steps
```

Three deterministic nodes prepared the context. The tool call got a structured, validated envelope — not "figure out what the user wants and also look up their account and also write a response." The graph enforced the right structure. The tool did the one thing it's best at.

Swap the tool implementation and the graph doesn't change:

```go
// Simulated — for tests and examples:
generate := reflow.WithRetry(GenerateResponse{LLM: FakeLLM{}}, 3)

// Real — same graph, real model:
generate := reflow.WithRetry(GenerateResponse{LLM: llm.AsTool(provider, "llm")}, 3)
```

---

## The model

Every node has three phases:

**Resolve** — Read the envelope. Inspect hints from upstream, derive local context.

**Act** — Do the work. Parse, transform, call an API, run inference.

**Settle** — Prepare the handoff. Attach hints, record what happened, decide if the result is ready.

```go
type Node[I, O any] interface {
    Resolve(context.Context, Envelope[I]) (Envelope[I], error)
    Act(context.Context, Envelope[I]) (Envelope[O], error)
    Settle(ctx context.Context, in Envelope[I], out Envelope[O], actErr error) (Envelope[O], bool, error)
}
```

Settle returns `done=true` to pass the result forward, or `done=false` with hints to signal that the result isn't ready. WithRetry feeds those hints back — each pass gets better context, not a blind re-roll.

The **envelope** carries the current value plus structured context that accumulates through the graph: hints (guidance for downstream nodes), trace steps (execution history), and tags (metadata).

---

## Composition

### Compose — multi-step graphs with plain Go

```go
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
```

Branching, loops, conditional logic — it's just Go. Each `Do` call runs a node through resolve → act → settle. If any step fails, subsequent calls are no-ops (like `bufio.Scanner`).

### Chain — sequential, type-transforming

```go
pipeline := reflow.Chain(parse, reflow.Chain(repair, validate))
```

> [!TIP]
> Chain works well for 2-3 nodes. For longer sequences, use Compose — it reads top-to-bottom and supports branching.

### Pipeline — sequential, same type

```go
triage := reflow.Pipeline[Ticket]("triage", normalize, classify, enrich, score)
```

### ForkJoin — concurrent fan-out, merge results

```go
enrich := reflow.ForkJoin(merge, lookupAccount, lookupHistory, lookupUsage)
```

### WithRetry — settle loop with hint feedback

```go
classify := reflow.WithRetry(ClassifyIntent{LLM: provider}, 3)
```

Settle returns `done=false` with hints. WithRetry feeds those hints back into the next iteration.

---

## Streaming

`StreamNode` yields envelopes one at a time via `iter.Seq2`. Settle runs per-item — it can filter, annotate, or reject inline.

```go
stream := reflow.Stream(ctx, triageInbox, reflow.NewEnvelope(inbox))
urgent, standard := reflow.Split(stream, isUrgent)
urgentLane := reflow.Pool(ctx, incidentDesk, urgent, 2)
standardLane := reflow.Pool(ctx, supportDesk, standard, 8)
results, err := reflow.Collect(reflow.Merge(urgentLane, standardLane))
```

Pull-based. Backpressure is free — stop ranging and the producer stops. Pool preserves emission order while bounding concurrency.

### Batch processing

Nothing in reflow processes batches directly. Instead, a `StreamNode` splits a batch into individual items, and `Pool` handles bounded concurrency.

> [!TIP]
> The type signature tells the story: `StreamNode[[]Ticket, Ticket]` makes the batch-to-individual transition explicit. Pool processes each item through a regular `Node[Ticket, Result]`.

```go
// StreamNode Act: yield one ticket at a time from the batch
func (e EmitTickets) Act(_ context.Context, in reflow.Envelope[[]Ticket]) iter.Seq2[reflow.Envelope[Ticket], error] {
    return func(yield func(reflow.Envelope[Ticket], error) bool) {
        for _, t := range in.Value {
            if !yield(reflow.Map(in, t), nil) {
                return
            }
        }
    }
}

// Stream → Pool → Collect
source := reflow.Stream(ctx, EmitTickets{}, reflow.NewEnvelope(tickets))
results, err := reflow.Collect(reflow.Pool(ctx, processTicket, source, 10))
```

`Stream` splits. `Pool` bounds concurrency. `Collect` reassembles. Add `Split` and `Merge` to route items into different lanes before pooling.

---

## Tools and tracing

The `Tool[I, O]` interface wraps any external call — LLM, database, API — with automatic timing and trace recording:

```go
type Tool[I, O any] interface {
    Name() string
    Call(context.Context, I) (O, error)
}
```

```go
resp, step, err := reflow.Use(ctx, chat, messages)
out = out.WithStep(step)
```

Every tool call lands in the envelope's trace with name, duration, and status. Print `out.Meta.Trace` and see the full execution story.

---

## Core API

```go
// Execute
reflow.Run(ctx, node, envelope)                        // single node
reflow.Stream(ctx, streamNode, envelope)                // pull-based iterator
reflow.Collect(stream)                                  // drain stream to slice

// Compose
reflow.Compose(name, func)                              // multi-step graph as code
reflow.Chain(ab, bc)                                    // sequential pair
reflow.Pipeline(name, nodes...)                         // sequential, same type
reflow.ForkJoin(merge, nodes...)                        // concurrent fan-out
reflow.Pool(ctx, node, source, concurrency)             // bounded parallel stream
reflow.Split(stream, pred)                              // two-lane routing
reflow.Merge(streams...)                                // interleave stream results
reflow.WithRetry(node, maxIter)                         // settle loop with feedback

// Helpers
reflow.Map(in, newValue)                                // carry meta to a new type
reflow.Lift(func(I) (O, error))                         // wrap a function as Act
reflow.Pass(func(I) O)                                  // wrap infallible function
reflow.NewRing[T](capacity)                             // sliding window buffer

// Tool tracing
reflow.Use(ctx, tool, input)                            // call + trace
reflow.UseRetry(ctx, tool, input, attempts)             // call + retry + trace
reflow.Invoke(ctx, name, func)                          // ad-hoc call + trace
```

---

## Writing a node

Named type — reusable, testable:

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

Inline closure — quick and disposable:

```go
double := &reflow.Func[int, int]{
    ActFn: reflow.Pass(func(n int) int { return n * 2 }),
}
```

Both implement `Node[I, O]`. Both compose with everything.

---

## Extensions

The core module has zero external dependencies. Optional extensions are separate modules in the same repo.

| Module | What it does |
|---|---|
| [`reflow/llm`](llm) | Provider interface + Ollama and Anthropic implementations |
| [`reflow/otel`](otel) | Export Reflow traces as OpenTelemetry spans |
| [`reflow/river/outbox`](river/outbox) | Transactional outbox for durable pipelines backed by Postgres + River |

```
go get github.com/vertexbuild/reflow/llm
go get github.com/vertexbuild/reflow/otel
go get github.com/vertexbuild/reflow/river/outbox
```

---

## Examples

Each example is self-contained and demonstrates different composition patterns.

```
go run ./examples/malformed_json/     # Chain, hints — parse fails, repair reads hints to fix it
go run ./examples/review_loop/        # WithRetry — settle rejects, hints refine the next attempt
go run ./examples/pipeline_ring/      # Pipeline, Ring — sliding window anomaly detection
go run ./examples/stream_router/      # Stream, Split, Pool, Merge — triage inbox to specialist lanes
go run ./examples/fanout_consensus/   # Compose, ForkJoin — concurrent evidence, conditional explanation
go run ./examples/support_agent/      # Compose, Tool, WithRetry — the graph is the control plane
go run ./examples/intake_service/     # Full HTTP service with routing, tools, and streaming
```

With a real LLM (requires `llm` and a running model):

```
ollama pull llama3.2
cd llm && go run ./examples/triage_agent/
```

---

## Design

- Each node settles context that makes downstream work easier.
- Hints carry nuance. Not just "this failed" — "this is suspect, here's why, look here."
- LLMs are one kind of tool. Use them where synthesis or ambiguity actually helps.
- Use the cheapest node that can correctly advance the envelope.

## License

MIT
