// Package reflowtel provides OpenTelemetry integration for Reflow pipelines.
//
// It bridges Reflow's built-in trace ([]Step with Phase, Node, Duration)
// to OTel spans, so pipeline execution shows up in Jaeger, Datadog,
// Honeycomb, or any OTel-compatible backend.
//
// Three integration points:
//
//   - ExportTrace: post-hoc export of a completed pipeline's trace as spans
//   - RunWithTrace: instrumented reflow.Run that exports automatically
//   - WrapTool: wraps a reflow.Tool to emit live spans per call
package reflowtel

import (
	"context"
	"time"

	"github.com/vertexbuild/reflow"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ExportTrace creates OTel spans from a completed pipeline's trace.
// Call this after reflow.Run completes to flush the execution history
// into your tracing backend.
//
// Creates a parent span named spanName, with child spans for each Step.
// Step.Duration is used to reconstruct timing sequentially. Tags from
// meta become attributes on the parent span.
//
//	out, err := reflow.Run(ctx, pipeline, envelope)
//	reflowtel.ExportTrace(ctx, tracer, "my-pipeline", out.Meta)
func ExportTrace(ctx context.Context, tracer trace.Tracer, spanName string, meta reflow.Meta) {
	now := time.Now()

	// Compute total duration from steps.
	var total time.Duration
	for _, s := range meta.Trace {
		total += s.Duration
	}

	// Parent span covers the whole pipeline.
	parentStart := now.Add(-total)
	ctx, parent := tracer.Start(ctx, spanName, trace.WithTimestamp(parentStart))

	// Tags as attributes on parent.
	for k, v := range meta.Tags {
		parent.SetAttributes(attribute.String("reflow.tag."+k, v))
	}

	// Hint count as an event.
	if len(meta.Hints) > 0 {
		parent.AddEvent("reflow.hints",
			trace.WithAttributes(attribute.Int("reflow.hint.count", len(meta.Hints))),
		)
	}

	// Child spans for each step, laid out sequentially.
	cursor := parentStart
	for _, step := range meta.Trace {
		stepName := step.Node
		if stepName == "" {
			stepName = step.Phase
		}

		_, child := tracer.Start(ctx, stepName,
			trace.WithTimestamp(cursor),
			trace.WithAttributes(
				attribute.String("reflow.phase", step.Phase),
				attribute.String("reflow.node", step.Node),
				attribute.String("reflow.status", step.Status),
			),
		)

		if step.Status != "ok" && step.Status != "retry" && step.Status != "" {
			child.SetStatus(codes.Error, step.Status)
		}

		end := cursor.Add(step.Duration)
		child.End(trace.WithTimestamp(end))
		cursor = end
	}

	parent.End(trace.WithTimestamp(now))
}

// RunWithTrace executes a node via reflow.Run and exports the resulting
// trace as OTel spans. The parent span captures the full execution; child
// spans are created for each Step in the output.
//
//	out, err := reflowtel.RunWithTrace(ctx, tracer, "classify", classifier, envelope)
func RunWithTrace[I, O any](
	ctx context.Context,
	tracer trace.Tracer,
	spanName string,
	n reflow.Node[I, O],
	in reflow.Envelope[I],
) (reflow.Envelope[O], error) {
	ctx, span := tracer.Start(ctx, spanName)

	out, err := reflow.Run(ctx, n, in)

	// Export child spans from the trace.
	for k, v := range out.Meta.Tags {
		span.SetAttributes(attribute.String("reflow.tag."+k, v))
	}
	if len(out.Meta.Hints) > 0 {
		span.AddEvent("reflow.hints",
			trace.WithAttributes(attribute.Int("reflow.hint.count", len(out.Meta.Hints))),
		)
	}

	cursor := span.SpanContext().TraceID() // just need the context for parenting
	_ = cursor
	for _, step := range out.Meta.Trace {
		stepName := step.Node
		if stepName == "" {
			stepName = step.Phase
		}
		_, child := tracer.Start(ctx, stepName,
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
	span.End()

	return out, err
}

// WrapTool wraps a reflow.Tool to emit a live OTel span for each Call.
// The returned tool satisfies the same interface and drops in anywhere.
//
//	traced := reflowtel.WrapTool(tracer, myTool)
//	resp, step, err := reflow.Use(ctx, traced, input)
func WrapTool[I, O any](tracer trace.Tracer, t reflow.Tool[I, O]) reflow.Tool[I, O] {
	return &tracedTool[I, O]{tracer: tracer, inner: t}
}

type tracedTool[I, O any] struct {
	tracer trace.Tracer
	inner  reflow.Tool[I, O]
}

func (t *tracedTool[I, O]) Name() string { return t.inner.Name() }

func (t *tracedTool[I, O]) Call(ctx context.Context, in I) (O, error) {
	ctx, span := t.tracer.Start(ctx, t.inner.Name(),
		trace.WithAttributes(attribute.String("reflow.tool", t.inner.Name())),
	)
	defer span.End()

	out, err := t.inner.Call(ctx, in)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return out, err
}
