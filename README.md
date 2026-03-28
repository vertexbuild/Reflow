# Reflow

Concurrent, streaming workflow graphs for Go with structured handoffs — for deterministic pipelines, agentic workflows, or both.

```
go get github.com/vertexbuild/reflow
```

Reflow passes an **envelope** between nodes — the current value plus structured context gathered along the way. Each node can resolve the situation, act on it, and settle the result into a better handoff for the next node.

---

## What it looks like

Fan out to three threat intel sources concurrently, merge results, and synthesize a verdict:

```go
enrich := Enrich{
    Sources: []reflow.Node[Query, Intel]{ AbuseDB{}, GeoReputation{}, BehaviorAnalysis{} },
}
synthesize := reflow.WithRetry(SynthesizeAssessment{LLM: provider}, 3)

pipeline := reflow.Chain[Query, Intel, Assessment](enrich, synthesize)
out, err := reflow.Run(ctx, pipeline, reflow.NewEnvelope(query))
```

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

12 hints and 12 trace steps, accumulated across concurrent branches into one envelope. Nothing lost.

Stream 15 lines of access logs, classify each one, silently drop normal traffic:

```go
events, err := reflow.Collect(reflow.Stream(ctx, AnalyzeLog{}, reflow.NewEnvelope(rawLog)))
```

```
$ go run ./examples/log_stream/

Streamed 15 lines → 9 events (6 normal traffic dropped)

  CRITICAL:
    203.0.113.42    GET     /api/users?id=1'+OR+'1'='1               → 400  (SQL injection attempt)
    203.0.113.42    GET     /../../etc/passwd                        → 400  (path traversal attempt)

  WARNING:
    10.0.0.55       GET     /wp-admin/login.php                      → 404  (reconnaissance scan)
    10.0.0.55       GET     /.env                                    → 403  (reconnaissance scan)
    172.16.0.99     GET     /api/health                              → 500  (server error 500)

Pipeline: 9 hints across 9 events | 18 trace steps
```

One `StreamNode`. Settle drops noise, annotates threats. No buffering the whole file.

---

## The model

Each node has three phases:

**Resolve** — inspect the envelope, read hints, derive local context.

**Act** — do the work. Transform data, call tools, run inference.

**Settle** — turn the outcome into a stable handoff. Attach hints, normalize errors into guidance, prepare the next node.

```go
type ParseJSON struct{}

func (ParseJSON) Resolve(ctx context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
    return in, nil
}

func (ParseJSON) Act(ctx context.Context, in reflow.Envelope[string]) (reflow.Envelope[JSON], error) {
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

That's a node. Three methods. The hint it settles tells the next node exactly what went wrong, so it doesn't have to rediscover the problem from scratch.

For inline one-offs:

```go
double := &reflow.Func[int, int]{
    ActFn: reflow.Pass(func(n int) int { return n * 2 }),
}
```

---

## Composition

```go
pipeline := reflow.Chain(parse, repair)                        // A → B → C
combined := reflow.ForkJoin(merge, sourceA, sourceB, sourceC)  // fan-out → merge
```

ForkJoin uses `errgroup`. If any branch fails, the rest are cancelled.

---

## Settle loop

When Settle returns `done=false`, the hints it produced get merged back into the input and the loop retries from Resolve:

```go
classifier := reflow.WithRetry(ClassifyTicket{LLM: provider}, 5)
```

The LLM gets better guidance on every retry. Not a blind re-roll — each iteration's feedback accumulates.

---

## Streaming

`StreamNode` is the streaming counterpart to `Node`. Act yields envelopes via `iter.Seq2`. Each item is settled individually — Settle can filter, annotate, or reject inline.

```go
for event, err := range reflow.Stream(ctx, analyzer, input) {
    // each event has been through resolve and settle
}

// or collect back to batch
items, err := reflow.Collect(reflow.Stream(ctx, analyzer, input))
```

Pull-based. Backpressure is free — stop ranging and the producer stops.

---

## Tools

Any external call — an LLM, a database, an API — gets automatic timing, status, and trace recording.

```go
type Tool[I, O any] interface {
    Name() string
    Call(context.Context, I) (O, error)
}
```

```go
resp, step, err := reflow.Use(ctx, c.LLM, messages)
out = out.WithStep(step)
```

With retries:

```go
resp, steps, err := reflow.UseRetry(ctx, c.LLM, messages, 3)
out = out.WithStep(steps...)
```

For one-off calls without a formal Tool:

```go
result, step, err := reflow.Invoke(ctx, "my.api", func(ctx context.Context) (string, error) {
    return callAPI(ctx, req)
})
```

Every call lands in the trace. Print `out.Meta.Trace` and see exactly what happened, how long each tool took, and where it failed.

---

## LLM support

`llm/` provides a `Provider` interface with Ollama and Anthropic implementations. Wrap any provider as a traced, retryable Tool:

```go
chat := llm.AsTool(ollama.New("llama3.2"), "ollama.chat")

resp, step, err := reflow.Use(ctx, chat, llm.Messages{
    System:   "Classify this ticket.",
    Messages: []llm.Message{{Role: "user", Content: ticket}},
})
```

LLM support is an extension, not the core. Most of your graph should be deterministic.

---

## Examples

```
go run ./examples/threat_intel/       # concurrent fan-out → merge → LLM synthesis
go run ./examples/log_stream/         # streaming access log analysis
go run ./examples/document_analyzer/  # extract → validate → LLM summarize
go run ./examples/malformed_json/     # parse → hint → repair → validate
go run ./examples/csv_validation/     # parse → validate → route by confidence
go run ./examples/log_classify/       # detect known → hint unknowns → analyze
```

Integration tests against Ollama:

```
ollama pull llama3.2
go test -tags live -run TestLive -v -timeout 120s
```

---

## Design

- Each node settles context that makes downstream work easier.
- Hints communicate nuance. Not just "this failed" — "this is suspect, here's why, look here."
- LLMs are one kind of node. Use them where synthesis or ambiguity actually helps.
- Use the cheapest node that can correctly advance the envelope.

## Contrib

The core module has zero external dependencies. Everything else lives in `contrib/` as separate modules — use what you need.

| Package                              | Import                                       | What it does                                                             |
| ------------------------------------ | -------------------------------------------- | ------------------------------------------------------------------------ |
| [LLM](contrib/llm)                   | `github.com/vertexbuild/reflow/llm`          | Provider interface + Ollama, Anthropic implementations                   |
| [OpenTelemetry](contrib/otel)        | `github.com/vertexbuild/reflow/otel`         | Export Reflow traces as OTel spans. Plug into Jaeger, Datadog, Honeycomb |
| [River Outbox](contrib/river/outbox) | `github.com/vertexbuild/reflow/river/outbox` | Transactional outbox for durable pipelines backed by Postgres + River    |

```
go get github.com/vertexbuild/reflow/llm
go get github.com/vertexbuild/reflow/otel
go get github.com/vertexbuild/reflow/river/outbox
```

## Acknowledgments

Reflow was inspired by [PocketFlow](https://github.com/The-Pocket/PocketFlow) from [Zachary Huang](https://github.com/zachary62) — which showed how simple a workflow graph can be when you strip it down to the right primitives.

## License

MIT
