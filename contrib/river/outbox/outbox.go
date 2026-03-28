// Package outbox provides a transactional outbox pattern for Reflow
// pipelines backed by River and Postgres.
//
// Envelopes are enqueued as River jobs within your existing database
// transaction. River picks them up reliably for processing through
// Reflow nodes. This gives you durable, recoverable pipelines without
// a separate message broker.
//
// Usage:
//
//	// Set up the router
//	router := outbox.NewRouter()
//	outbox.Handle(router, "process.claim", claimPipeline)
//	outbox.Handle(router, "process.report", reportPipeline)
//
//	// Register with River
//	workers := river.NewWorkers()
//	river.AddWorkerSafely(workers, router.Worker())
//
//	// Enqueue within a transaction
//	tx, _ := pool.Begin(ctx)
//	outbox.Enqueue(ctx, tx, riverClient, "process.claim", envelope)
//	tx.Commit(ctx)
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/vertexbuild/reflow"
)

// Args is the River job argument for all reflow outbox jobs.
// All jobs share a single River kind ("reflow.outbox") and are
// dispatched to the correct handler by the Router using Kind_.
type Args struct {
	Kind_   string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
	Meta    metaJSON        `json:"meta"`
}

func (Args) Kind() string { return "reflow.outbox" }

// metaJSON is a JSON-friendly representation of reflow.Meta.
type metaJSON struct {
	Hints []reflow.Hint     `json:"hints,omitempty"`
	Trace []reflow.Step     `json:"trace,omitempty"`
	Tags  map[string]string `json:"tags,omitempty"`
}

func metaToJSON(m reflow.Meta) metaJSON {
	return metaJSON{Hints: m.Hints, Trace: m.Trace, Tags: m.Tags}
}

func metaFromJSON(m metaJSON) reflow.Meta {
	tags := m.Tags
	if tags == nil {
		tags = make(map[string]string)
	}
	return reflow.Meta{Hints: m.Hints, Trace: m.Trace, Tags: tags}
}

// Enqueue marshals an envelope and inserts it as a River job within tx.
// The kind string identifies which handler processes this job.
//
// This is the transactional outbox pattern: call Enqueue inside your
// existing database transaction alongside your domain writes.
//
//	tx, _ := pool.Begin(ctx)
//	// ... your domain writes ...
//	outbox.Enqueue(ctx, tx, client, "process.claim", envelope)
//	tx.Commit(ctx)
func Enqueue[T any](
	ctx context.Context,
	tx pgx.Tx,
	client *river.Client[pgx.Tx],
	kind string,
	env reflow.Envelope[T],
	opts ...river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	payload, err := json.Marshal(env.Value)
	if err != nil {
		return nil, fmt.Errorf("outbox: marshal payload: %w", err)
	}

	args := Args{
		Kind_:   kind,
		Payload: payload,
		Meta:    metaToJSON(env.Meta),
	}

	insertOpts := river.InsertOpts{}
	if len(opts) > 0 {
		insertOpts = opts[0]
	}

	return client.InsertTx(ctx, tx, args, &insertOpts)
}

// Router dispatches outbox jobs to registered reflow nodes by kind.
type Router struct {
	mu       sync.RWMutex
	handlers map[string]handler
}

type handler func(ctx context.Context, payload json.RawMessage, meta reflow.Meta) error

// NewRouter creates an empty router.
func NewRouter() *Router {
	return &Router{handlers: make(map[string]handler)}
}

// Handle registers a reflow Node to process jobs of the given kind.
// When a job with this kind arrives, the payload is deserialized into
// type T, the envelope is reconstructed with the persisted Meta, and
// reflow.Run executes the node.
//
// The node must have the same input and output type (Node[T, T])
// because River jobs are fire-and-forget — there is no typed return
// channel. If you need Node[I, O], wrap it to handle the output
// inside Act.
//
//	outbox.Handle(router, "process.claim", claimPipeline)
func Handle[T any](r *Router, kind string, node reflow.Node[T, T]) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.handlers[kind] = func(ctx context.Context, payload json.RawMessage, meta reflow.Meta) error {
		var value T
		if err := json.Unmarshal(payload, &value); err != nil {
			return fmt.Errorf("outbox: unmarshal %q payload: %w", kind, err)
		}
		env := reflow.Envelope[T]{Value: value, Meta: meta}
		_, err := reflow.Run(ctx, node, env)
		return err
	}
}

// Worker returns a River worker backed by this router.
// Register it with River before starting the client:
//
//	workers := river.NewWorkers()
//	river.AddWorkerSafely(workers, router.Worker())
func (r *Router) Worker() *RouterWorker {
	return &RouterWorker{router: r}
}

// RouterWorker implements river.Worker[Args].
type RouterWorker struct {
	river.WorkerDefaults[Args]
	router *Router
}

// Work dispatches the job to the handler registered for its kind.
func (w *RouterWorker) Work(ctx context.Context, job *river.Job[Args]) error {
	w.router.mu.RLock()
	h, ok := w.router.handlers[job.Args.Kind_]
	w.router.mu.RUnlock()

	if !ok {
		return fmt.Errorf("outbox: no handler for kind %q", job.Args.Kind_)
	}

	meta := metaFromJSON(job.Args.Meta)
	return h(ctx, job.Args.Payload, meta)
}
