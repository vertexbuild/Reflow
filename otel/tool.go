package reflowtel

import (
	"context"
	"time"

	"github.com/vertexbuild/reflow"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// WrapTool wraps a reflow.Tool to emit a live OTel span and optional
// metrics for each Call. The returned tool satisfies the same interface
// and drops in anywhere.
//
//	traced := reflowtel.WrapTool(inst, "my-api", myTool)
//	resp, step, err := reflow.Use(ctx, traced, input)
func WrapTool[I, O any](inst *Instrumenter, name string, t reflow.Tool[I, O]) reflow.Tool[I, O] {
	return &tracedTool[I, O]{inst: inst, name: name, inner: t}
}

type tracedTool[I, O any] struct {
	inst  *Instrumenter
	name  string
	inner reflow.Tool[I, O]
}

func (t *tracedTool[I, O]) Name() string { return t.name }

func (t *tracedTool[I, O]) Call(ctx context.Context, in I) (O, error) {
	ctx, span := t.inst.tracer.Start(ctx, t.name,
		trace.WithAttributes(attribute.String("reflow.tool", t.name)),
	)

	start := time.Now()
	out, err := t.inner.Call(ctx, in)
	elapsed := time.Since(start)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	status := "ok"
	if err != nil {
		status = "error"
	}
	t.inst.recordTool(ctx, t.name, elapsed, status, nil)

	span.End()
	return out, err
}
