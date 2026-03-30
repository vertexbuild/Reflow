# Reflow

Concurrent, streaming workflow graphs for Go with structured handoffs — for deterministic pipelines, agentic workflows, or both.

```
go get github.com/vertexbuild/reflow
```

Reflow passes an **envelope** between nodes: the current value plus structured context gathered along the way. Each node can inspect the situation, act on it, and settle the result into a prepared handoff for whatever comes next.

```
$ go run ./examples/threat_intel/

Querying sources concurrently...
[Behavior]    verdict=malicious   confidence=0.94  (Port scanning on 47 ports in last 24h)
[AbuseDB]     verdict=malicious   confidence=0.88  (IP in 3 blocklists, SSH brute force)
[GeoRep]      verdict=suspicious  confidence=0.72  (Hosting provider, elevated threat region)

VERDICT:    MALICIOUS
CONFIDENCE: 83%

Sources: 3 queried | Hints: 12 accumulated | Trace: 12 steps
```

Three sources ran concurrently. Each settled its own verdict, confidence, and tags as hints. The LLM synthesized from the accumulated context, not from scratch. 12 hints, 12 trace steps, one envelope.

---

## The model

Every node has three phases:

**Resolve** — Read the envelope. Inspect hints from upstream, derive local context.

**Act** — Do the work. Parse, transform, call an API, run inference.

**Settle** — Prepare the handoff. Attach hints, record what happened, give the next node a head start.

```go
type Node[I, O any] interface {
    Resolve(context.Context, Envelope[I]) (Envelope[I], error)
    Act(context.Context, Envelope[I]) (Envelope[O], error)
    Settle(ctx context.Context, in Envelope[I], out Envelope[O], actErr error) (Envelope[O], bool, error)
}
```

A node doesn't just return a result. It settles that result into a better starting point for whatever comes next.

---

## Where it fits

### Data pipelines

Chain deterministic transforms. Each node validates, annotates, and prepares the envelope for the next. Errors become hints, not crashes.

```go
pipeline := reflow.Chain(parse, reflow.Chain(validate, enrich))
out, err := reflow.Run(ctx, pipeline, reflow.NewEnvelope(rawData))
```

### Event and log processing

Stream events line by line. Settle filters noise inline — no buffering the whole file, no separate filter stage.

```go
events, err := reflow.Collect(reflow.Stream(ctx, analyzer, reflow.NewEnvelope(rawLog)))
```

```
Streamed 15 lines → 9 events (6 normal traffic dropped)

  CRITICAL:
    203.0.113.42    GET     /api/users?id=1'+OR+'1'='1  → 400  (SQL injection attempt)
    203.0.113.42    GET     /../../etc/passwd            → 400  (path traversal attempt)

  WARNING:
    10.0.0.55       GET     /wp-admin/login.php          → 404  (reconnaissance scan)
    10.0.0.55       GET     /.env                        → 403  (reconnaissance scan)
```

### Webhook and queue processing

Bounded concurrency over a stream. Pool processes N items at a time, preserves order, respects backpressure.

```go
source := reflow.Stream(ctx, splitter, reflow.NewEnvelope(batch))
results, err := reflow.Collect(reflow.Pool(ctx, processor, source, 20))
```

### LLM-assisted workflows

Use the cheapest node that can correctly advance the envelope. Deterministic nodes handle 95% of the work. The LLM comes in where synthesis or ambiguity actually helps — and it gets prepared context, not a raw dump.

```go
enrich   := reflow.ForkJoin(merge, sourceA, sourceB, sourceC)
analyze  := reflow.WithRetry(SynthesizeWithLLM{LLM: provider}, 3)
pipeline := reflow.Chain(enrich, analyze)
```

The LLM gets better guidance on every retry. Hints accumulate — not a blind re-roll.

---

## Agents: correctness by construction

Most agent frameworks give an LLM a bag of tools and hope it calls them in the right order. The model is the control plane. When it guesses wrong, you add more instructions and hope.

Reflow inverts this. The graph is the control plane. It encodes the correct sequence of operations, and the LLM is one node that does what it's good at — synthesis, ambiguity resolution, explanation — with a prepared envelope that tells it exactly what to focus on.

```go
// The graph enforces the right structure. The LLM doesn't decide what to do next.
// It receives a settled envelope and does the one thing it's best at.
classify  := reflow.WithRetry(ClassifyIntent{LLM: provider}, 3)
extract   := ExtractEntities{}   // deterministic
validate  := ValidateSchema{}    // deterministic
enrich    := FetchContext{DB: db} // deterministic
respond   := reflow.WithRetry(GenerateResponse{LLM: provider}, 2)

agent := reflow.Chain(classify,
    reflow.Chain(extract,
        reflow.Chain(validate,
            reflow.Chain(enrich, respond))))
```

Each node prepares the next. The classify node settles intent and confidence. Extract reads those hints. Validate checks the schema. Enrich fetches relevant context. By the time the response node runs, its envelope contains a structured, validated, enriched handoff — not "figure out what the user wants and also look up their account and also check the policy and also write a response."

The graph is the agent. The LLM is a cell within it.

You don't need to instruct the model to be correct. You construct a pipeline where incorrect results don't propagate — Settle catches them, attaches feedback hints, and the retry loop refines until it settles.

That's correctness by construction, not instruction.

---

## Core API

```go
// Execute
reflow.Run(ctx, node, envelope)                        // single node
reflow.Stream(ctx, streamNode, envelope)                // pull-based iterator
reflow.Collect(stream)                                  // drain stream to slice

// Compose
reflow.Chain(ab, bc)                                    // sequential
reflow.ForkJoin(merge, nodes...)                        // concurrent fan-out
reflow.Pool(ctx, node, source, concurrency)             // bounded parallel stream
reflow.WithRetry(node, maxIter)                         // settle loop with feedback

// Helpers
reflow.Lift(func(I) (O, error))                         // wrap a function as Act
reflow.Pass(func(I) O)                                  // wrap infallible function
reflow.NewRing[T](capacity)                             // sliding window buffer

// Tool tracing
reflow.Use(ctx, tool, input)                            // call + trace
reflow.UseRetry(ctx, tool, input, attempts)             // call + retry + trace
reflow.Invoke(ctx, name, func)                          // ad-hoc call + trace
```

Zero core dependencies. The entire public API fits on one screen.

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
    return reflow.Envelope[JSON]{Value: v, Meta: in.Meta}, json.Unmarshal([]byte(in.Value), &v)
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

Both implement `Node[I, O]`. Both work with Chain, ForkJoin, WithRetry, Pool.

---

## Streaming

`StreamNode` yields envelopes one at a time via `iter.Seq2`. Settle runs per-item — it can filter, annotate, or reject inline.

```go
type StreamNode[I, O any] interface {
    Resolve(context.Context, Envelope[I]) (Envelope[I], error)
    Act(context.Context, Envelope[I]) iter.Seq2[Envelope[O], error]
    Settle(ctx context.Context, in Envelope[I], out Envelope[O], actErr error) (Envelope[O], bool, error)
}
```

Pull-based. Backpressure is free — stop ranging and the producer stops. Bridge back to batch with `Collect`. Process with bounded concurrency using `Pool`.

---

## Tools and tracing

The `Tool[I, O]` interface wraps any external call with automatic timing and trace recording:

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

Every tool call — LLM, database, API — lands in the envelope's trace with name, duration, and status. Print `out.Meta.Trace` and see the full execution story.

---

## Contrib

The core module has zero external dependencies. Extensions live in `contrib/` as separate modules.

| Package | Import | What it does |
|---|---|---|
| [LLM](contrib/llm) | `github.com/vertexbuild/reflow/llm` | Provider interface + Ollama, Anthropic implementations |
| [OpenTelemetry](contrib/otel) | `github.com/vertexbuild/reflow/otel` | Export Reflow traces as OTel spans. Plug into Jaeger, Datadog, Honeycomb |
| [River Outbox](contrib/river/outbox) | `github.com/vertexbuild/reflow/river/outbox` | Transactional outbox for durable pipelines backed by Postgres + River |

```
go get github.com/vertexbuild/reflow/llm
go get github.com/vertexbuild/reflow/otel
go get github.com/vertexbuild/reflow/river/outbox
```

---

## Examples

```
go run ./examples/threat_intel/       # concurrent fan-out → merge → LLM synthesis
go run ./examples/log_stream/         # streaming access log analysis + classification
go run ./examples/document_analyzer/  # extract → validate → LLM summarize
go run ./examples/malformed_json/     # parse → hint → targeted repair → validate
go run ./examples/csv_validation/     # parse → validate → route by confidence
go run ./examples/log_classify/       # detect known patterns → hint unknowns → analyze
```

Integration tests against Ollama:

```
ollama pull llama3.2
go test -tags live -run TestLive -v -timeout 120s
```

---

## Design

- Each node settles context that makes downstream work easier.
- Hints carry nuance. Not just "this failed" — "this is suspect, here's why, look here."
- LLMs are one kind of node. Use them where synthesis or ambiguity actually helps.
- Use the cheapest node that can correctly advance the envelope.

## License

MIT
