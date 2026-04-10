package reflowtel

import (
	"context"
	"time"

	"github.com/vertexbuild/reflow"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// createStepSpan creates a child span for a single trace step, starting
// at cursor. It returns the end time for the next span's start.
func (inst *Instrumenter) createStepSpan(ctx context.Context, step reflow.Step, cursor time.Time) time.Time {
	name := step.Node
	if name == "" {
		name = step.Phase
	}

	_, child := inst.tracer.Start(ctx, name,
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
	return end
}
