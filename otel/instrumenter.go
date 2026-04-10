package reflowtel

import (
	"context"
	"time"

	"github.com/ploffredo/reflow"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// TagFunc selects and transforms envelope tags into metric attributes.
// Use WithTagFunc to provide one; it overrides WithAllowedTags.
type TagFunc func(tags map[string]string) []attribute.KeyValue

// Option configures an Instrumenter.
type Option func(*config)

type config struct {
	meterProvider metric.MeterProvider
	allowedTags   map[string]bool
	tagFunc       TagFunc
}

// WithMeter enables metrics on the Instrumenter. Without this option,
// only tracing is active.
func WithMeter(mp metric.MeterProvider) Option {
	return func(c *config) { c.meterProvider = mp }
}

// WithAllowedTags sets the tag keys that are promoted to metric attributes.
// Only matching keys from Meta.Tags appear as reflow.tag.<key> on metrics.
// Use this to control Prometheus label cardinality.
func WithAllowedTags(keys ...string) Option {
	return func(c *config) {
		c.allowedTags = make(map[string]bool, len(keys))
		for _, k := range keys {
			c.allowedTags[k] = true
		}
	}
}

// WithTagFunc provides full control over tag-to-attribute mapping for metrics.
// It overrides WithAllowedTags when both are set.
func WithTagFunc(fn TagFunc) Option {
	return func(c *config) { c.tagFunc = fn }
}

// Instrumenter provides OpenTelemetry tracing and optional metrics for
// reflow pipelines. Create one with NewInstrumenter and reuse it across
// all pipeline executions.
type Instrumenter struct {
	tracer  trace.Tracer
	tags    map[string]bool
	tagFunc TagFunc

	runDuration  metric.Float64Histogram
	runCount     metric.Int64Counter
	stepDuration metric.Float64Histogram
	stepCount    metric.Int64Counter
	toolDuration metric.Float64Histogram
	toolCount    metric.Int64Counter
	retryCount   metric.Int64Counter
	hintCount    metric.Int64Counter
}

// NewInstrumenter creates an Instrumenter. Tracing is always enabled.
// Pass WithMeter to also enable metrics.
func NewInstrumenter(tp trace.TracerProvider, opts ...Option) *Instrumenter {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}

	inst := &Instrumenter{
		tracer: tp.Tracer("reflowtel"),
	}

	if cfg.tagFunc != nil {
		inst.tagFunc = cfg.tagFunc
	} else if cfg.allowedTags != nil {
		inst.tags = cfg.allowedTags
	}

	if cfg.meterProvider == nil {
		return inst
	}

	m := cfg.meterProvider.Meter("reflowtel")

	var err error

	inst.runDuration, err = m.Float64Histogram("reflow.run.duration", metric.WithUnit("s"))
	if err != nil {
		otel.Handle(err)
	}
	inst.runCount, err = m.Int64Counter("reflow.run.count")
	if err != nil {
		otel.Handle(err)
	}

	inst.stepDuration, err = m.Float64Histogram("reflow.step.duration", metric.WithUnit("s"))
	if err != nil {
		otel.Handle(err)
	}
	inst.stepCount, err = m.Int64Counter("reflow.step.count")
	if err != nil {
		otel.Handle(err)
	}

	inst.toolDuration, err = m.Float64Histogram("reflow.tool.duration", metric.WithUnit("s"))
	if err != nil {
		otel.Handle(err)
	}
	inst.toolCount, err = m.Int64Counter("reflow.tool.count")
	if err != nil {
		otel.Handle(err)
	}

	inst.retryCount, err = m.Int64Counter("reflow.retry.count")
	if err != nil {
		otel.Handle(err)
	}
	inst.hintCount, err = m.Int64Counter("reflow.hint.count")
	if err != nil {
		otel.Handle(err)
	}

	return inst
}

// Export creates OTel spans from a completed pipeline's Meta and records
// metrics for each step, tool call, retry, and hint. Use this for post-hoc
// export after running a pipeline yourself.
//
// Export does not record reflow.run.duration or reflow.run.count — those
// are only emitted by Run, which knows the total elapsed time.
func (inst *Instrumenter) Export(ctx context.Context, name string, m reflow.Meta) {
	now := time.Now()

	steps := m.Trace.Slice()

	var total time.Duration
	for _, s := range steps {
		total += s.Duration
	}

	parentStart := now.Add(-total)
	ctx, parent := inst.tracer.Start(ctx, name, trace.WithTimestamp(parentStart))

	for k, v := range m.Tags {
		parent.SetAttributes(attribute.String("reflow.tag."+k, v))
	}
	if m.Hints.Len() > 0 {
		parent.AddEvent("reflow.hints",
			trace.WithAttributes(attribute.Int("reflow.hint.count", m.Hints.Len())),
		)
	}

	cursor := parentStart
	for _, step := range steps {
		cursor = inst.createStepSpan(ctx, step, cursor)
	}

	parent.End(trace.WithTimestamp(now))

	// Record metrics from the trace.
	inst.recordTraceMetrics(ctx, m)
}

// recordTraceMetrics walks a Meta's Trace and Hints, recording the
// appropriate metrics for each entry.
func (inst *Instrumenter) recordTraceMetrics(ctx context.Context, m reflow.Meta) {
	m.Trace.Each(func(step reflow.Step) {
		if step.Phase == "tool" {
			inst.recordTool(ctx, step.Node, step.Duration, step.Status, m.Tags)
		} else {
			inst.recordStep(ctx, step, m.Tags)
		}
		if step.Status == "retry" {
			inst.recordRetry(ctx, step.Node, m.Tags)
		}
	})
	m.Hints.Each(func(h reflow.Hint) {
		inst.recordHint(ctx, h, m.Tags)
	})
}
