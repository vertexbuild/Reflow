package reflowtel

import (
	"context"
	"time"

	"github.com/vertexbuild/reflow"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Run executes a node via reflow.Run and exports the resulting trace as
// OTel spans. When metrics are enabled on the Instrumenter, it also
// records run, step, tool, retry, and hint metrics.
//
// Steps created by the reflow framework (resolve/settle) have empty Node
// fields. Run backfills these with name so that spans and metrics carry
// proper node attribution.
//
//	out, err := reflowtel.Run(ctx, inst, "classify", classifier, envelope)
func Run[I, O any](
	ctx context.Context,
	inst *Instrumenter,
	name string,
	n reflow.Node[I, O],
	env reflow.Envelope[I],
) (reflow.Envelope[O], error) {
	ctx, span := inst.tracer.Start(ctx, name)
	start := time.Now()

	out, err := reflow.Run(ctx, n, env)

	elapsed := time.Since(start)

	// Backfill empty Node fields on framework-generated steps.
	for i := range out.Meta.Trace {
		if out.Meta.Trace[i].Node == "" {
			out.Meta.Trace[i].Node = name
		}
	}

	// Tag attributes on parent span.
	for k, v := range out.Meta.Tags {
		span.SetAttributes(attribute.String("reflow.tag."+k, v))
	}
	if len(out.Meta.Hints) > 0 {
		span.AddEvent("reflow.hints",
			trace.WithAttributes(attribute.Int("reflow.hint.count", len(out.Meta.Hints))),
		)
	}

	// Child spans for each step.
	for _, step := range out.Meta.Trace {
		stepName := step.Node
		if stepName == "" {
			stepName = step.Phase
		}
		_, child := inst.tracer.Start(ctx, stepName,
			trace.WithAttributes(
				attribute.String("reflow.phase", step.Phase),
				attribute.String("reflow.node", step.Node),
				attribute.String("reflow.status", step.Status),
			),
		)
		if step.Status != "ok" && step.Status != "retry" && step.Status != "" {
			child.SetStatus(codes.Error, step.Status)
		}
		child.End()
	}

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	// Record metrics while span is still active for exemplar linking.
	runStatus := "ok"
	if err != nil {
		runStatus = "error"
	}
	inst.recordRun(ctx, name, elapsed, runStatus, out.Meta.Tags)
	inst.recordTraceMetrics(ctx, out.Meta)

	span.End()

	return out, err
}
