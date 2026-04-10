package reflowtel

import (
	"context"
	"time"

	"github.com/vertexbuild/reflow"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// normalizeStatus maps step status strings to a bounded set of metric
// attribute values: "ok", "retry", or "error". The original status
// is preserved on spans; only metrics get the normalized form.
func normalizeStatus(status string) string {
	switch status {
	case "ok", "retry":
		return status
	case "":
		return "ok"
	default:
		return "error"
	}
}

func (inst *Instrumenter) tagAttrs(tags map[string]string) []attribute.KeyValue {
	if inst.tagFunc != nil {
		return inst.tagFunc(tags)
	}
	if inst.tags == nil || len(tags) == 0 {
		return nil
	}
	var attrs []attribute.KeyValue
	for k, v := range tags {
		if inst.tags[k] {
			attrs = append(attrs, attribute.String("reflow.tag."+k, v))
		}
	}
	return attrs
}

func (inst *Instrumenter) recordRun(ctx context.Context, name string, d time.Duration, status string, tags map[string]string) {
	if inst.runDuration == nil {
		return
	}
	attrs := append([]attribute.KeyValue{
		attribute.String("node", name),
		attribute.String("status", normalizeStatus(status)),
	}, inst.tagAttrs(tags)...)
	opt := metric.WithAttributes(attrs...)
	inst.runDuration.Record(ctx, d.Seconds(), opt)
	inst.runCount.Add(ctx, 1, opt)
}

func (inst *Instrumenter) recordStep(ctx context.Context, step reflow.Step, tags map[string]string) {
	if inst.stepDuration == nil {
		return
	}
	attrs := append([]attribute.KeyValue{
		attribute.String("node", step.Node),
		attribute.String("phase", step.Phase),
		attribute.String("status", normalizeStatus(step.Status)),
	}, inst.tagAttrs(tags)...)
	opt := metric.WithAttributes(attrs...)
	inst.stepDuration.Record(ctx, step.Duration.Seconds(), opt)
	inst.stepCount.Add(ctx, 1, opt)
}

func (inst *Instrumenter) recordTool(ctx context.Context, name string, d time.Duration, status string, tags map[string]string) {
	if inst.toolDuration == nil {
		return
	}
	attrs := append([]attribute.KeyValue{
		attribute.String("tool", name),
		attribute.String("status", normalizeStatus(status)),
	}, inst.tagAttrs(tags)...)
	opt := metric.WithAttributes(attrs...)
	inst.toolDuration.Record(ctx, d.Seconds(), opt)
	inst.toolCount.Add(ctx, 1, opt)
}

func (inst *Instrumenter) recordRetry(ctx context.Context, node string, tags map[string]string) {
	if inst.retryCount == nil {
		return
	}
	attrs := append([]attribute.KeyValue{
		attribute.String("node", node),
	}, inst.tagAttrs(tags)...)
	inst.retryCount.Add(ctx, 1, metric.WithAttributes(attrs...))
}

func (inst *Instrumenter) recordHint(ctx context.Context, h reflow.Hint, tags map[string]string) {
	if inst.hintCount == nil {
		return
	}
	attrs := append([]attribute.KeyValue{
		attribute.String("hint.code", h.Code),
	}, inst.tagAttrs(tags)...)
	inst.hintCount.Add(ctx, 1, metric.WithAttributes(attrs...))
}
