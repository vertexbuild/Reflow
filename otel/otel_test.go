package reflowtel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vertexbuild/reflow"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// --- Test infrastructure ---

func newTracer() (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	return exp, tp
}

func newMeter() (*sdkmetric.ManualReader, *sdkmetric.MeterProvider) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	return reader, mp
}

func newTraceOnlyInst() (*tracetest.InMemoryExporter, *Instrumenter) {
	exp, tp := newTracer()
	return exp, NewInstrumenter(tp)
}

func newFullInst(tags ...string) (*tracetest.InMemoryExporter, *sdkmetric.ManualReader, *Instrumenter) {
	exp, tp := newTracer()
	reader, mp := newMeter()
	opts := []Option{WithMeter(mp)}
	if len(tags) > 0 {
		opts = append(opts, WithAllowedTags(tags...))
	}
	return exp, reader, NewInstrumenter(tp, opts...)
}

// collectMetrics gathers all metrics from the reader into a map keyed by name.
func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	out := make(map[string]metricdata.Metrics)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// --- Export ---

func TestExportCreatesSpans(t *testing.T) {
	exp, inst := newTraceOnlyInst()

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Node: "parse", Phase: "resolve", Status: "ok", Duration: 10 * time.Millisecond},
			{Node: "parse", Phase: "settle", Status: "ok", Duration: 5 * time.Millisecond},
			{Node: "ollama.chat", Phase: "tool", Status: "ok", Duration: 200 * time.Millisecond},
			{Node: "classify", Phase: "settle", Status: "ok", Duration: 2 * time.Millisecond},
		},
		Tags: map[string]string{"env": "test"},
	}

	inst.Export(context.Background(), "my-pipeline", meta)

	spans := exp.GetSpans()
	// 1 parent + 4 children = 5
	if len(spans) != 5 {
		t.Fatalf("expected 5 spans, got %d", len(spans))
	}

	parent := spans[len(spans)-1]
	if parent.Name != "my-pipeline" {
		t.Fatalf("expected parent span 'my-pipeline', got %q", parent.Name)
	}
}

func TestExportErrorStatus(t *testing.T) {
	exp, inst := newTraceOnlyInst()

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Node: "api", Phase: "tool", Status: "connection refused", Duration: 50 * time.Millisecond},
		},
		Tags: map[string]string{},
	}

	inst.Export(context.Background(), "pipeline", meta)

	for _, s := range exp.GetSpans() {
		if s.Name == "api" {
			if s.Status.Code != codes.Error {
				t.Fatalf("expected error status, got %v", s.Status.Code)
			}
			return
		}
	}
	t.Fatal("expected span named 'api'")
}

func TestExportTagsAsAttributes(t *testing.T) {
	exp, inst := newTraceOnlyInst()

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Phase: "resolve", Status: "ok", Duration: time.Millisecond},
		},
		Tags: map[string]string{"env": "prod", "version": "1.2"},
	}

	inst.Export(context.Background(), "p", meta)


	parent := exp.GetSpans()[len(exp.GetSpans())-1]

	hasEnv, hasVer := false, false
	for _, a := range parent.Attributes {
		if a.Key == "reflow.tag.env" && a.Value.AsString() == "prod" {
			hasEnv = true
		}
		if a.Key == "reflow.tag.version" && a.Value.AsString() == "1.2" {
			hasVer = true
		}
	}
	if !hasEnv || !hasVer {
		t.Fatalf("expected tag attributes, got %v", parent.Attributes)
	}
}

func TestExportHintEvent(t *testing.T) {
	exp, inst := newTraceOnlyInst()

	meta := reflow.Meta{
		Hints: []reflow.Hint{
			{Code: "a", Message: "one"},
			{Code: "b", Message: "two"},
		},
		Trace: []reflow.Step{
			{Phase: "resolve", Status: "ok", Duration: time.Millisecond},
		},
		Tags: map[string]string{},
	}

	inst.Export(context.Background(), "p", meta)


	parent := exp.GetSpans()[len(exp.GetSpans())-1]
	if len(parent.Events) == 0 {
		t.Fatal("expected hint event on parent")
	}
	found := false
	for _, e := range parent.Events {
		if e.Name == "reflow.hints" {
			for _, a := range e.Attributes {
				if a.Key == "reflow.hint.count" && a.Value.AsInt64() == 2 {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("expected reflow.hints event with count=2")
	}
}

func TestExportPhaseOnlySpanName(t *testing.T) {
	exp, inst := newTraceOnlyInst()

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Phase: "resolve", Status: "ok", Duration: time.Millisecond},
		},
		Tags: map[string]string{},
	}

	inst.Export(context.Background(), "p", meta)


	for _, s := range exp.GetSpans() {
		if s.Name == "resolve" {
			return
		}
	}
	t.Fatal("expected span named 'resolve' when Node is empty")
}

// --- Run ---

func TestRunCreatesParentSpan(t *testing.T) {
	exp, inst := newTraceOnlyInst()

	node := &reflow.Func[int, int]{
		ActFn: reflow.Pass(func(n int) int { return n * 2 }),
	}

	out, err := Run(context.Background(), inst, "double", node, reflow.NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 10 {
		t.Fatalf("expected 10, got %d", out.Value)
	}



	found := false
	for _, s := range exp.GetSpans() {
		if s.Name == "double" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected parent span named 'double'")
	}
}

func TestRunRecordsError(t *testing.T) {
	exp, inst := newTraceOnlyInst()

	node := &reflow.Func[int, int]{
		ActFn: func(_ context.Context, _ reflow.Envelope[int]) (reflow.Envelope[int], error) {
			return reflow.Envelope[int]{}, errors.New("boom")
		},
	}

	_, err := Run(context.Background(), inst, "failing", node, reflow.NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}



	for _, s := range exp.GetSpans() {
		if s.Name == "failing" {
			if s.Status.Code != codes.Error {
				t.Fatalf("expected error status on parent, got %v", s.Status.Code)
			}
			return
		}
	}
	t.Fatal("expected span named 'failing'")
}

func TestRunBackfillsNodeName(t *testing.T) {
	_, inst := newTraceOnlyInst()

	node := &reflow.Func[int, int]{
		ActFn: reflow.Pass(func(n int) int { return n }),
	}

	out, err := Run(context.Background(), inst, "mynode", node, reflow.NewEnvelope(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, step := range out.Meta.Trace {
		if step.Node == "" {
			t.Fatalf("expected Node to be backfilled, got empty for phase %q", step.Phase)
		}
		if step.Node != "mynode" {
			t.Fatalf("expected Node 'mynode', got %q", step.Node)
		}
	}
}

// --- WrapTool ---

type mockTool struct {
	name string
	fn   func(context.Context, string) (int, error)
}

func (m *mockTool) Name() string                                     { return m.name }
func (m *mockTool) Call(ctx context.Context, in string) (int, error) { return m.fn(ctx, in) }

func TestWrapToolCreatesSpan(t *testing.T) {
	exp, inst := newTraceOnlyInst()

	tool := &mockTool{
		name: "my.api",
		fn:   func(_ context.Context, s string) (int, error) { return len(s), nil },
	}

	wrapped := WrapTool(inst, "my.api", tool)

	if wrapped.Name() != "my.api" {
		t.Fatalf("expected name 'my.api', got %q", wrapped.Name())
	}

	result, err := wrapped.Call(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 5 {
		t.Fatalf("expected 5, got %d", result)
	}



	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name != "my.api" {
		t.Fatalf("expected span 'my.api', got %q", spans[0].Name)
	}

	found := false
	for _, a := range spans[0].Attributes {
		if a.Key == "reflow.tool" && a.Value.AsString() == "my.api" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected reflow.tool attribute")
	}
}

func TestWrapToolRecordsError(t *testing.T) {
	exp, inst := newTraceOnlyInst()

	tool := &mockTool{
		name: "failing",
		fn:   func(_ context.Context, _ string) (int, error) { return 0, errors.New("nope") },
	}

	wrapped := WrapTool(inst, "failing", tool)
	_, err := wrapped.Call(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error")
	}



	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Fatalf("expected error status, got %v", spans[0].Status.Code)
	}
}

func TestWrapToolPropagatesContext(t *testing.T) {
	_, inst := newTraceOnlyInst()

	type ctxKey string
	ctx := context.WithValue(context.Background(), ctxKey("k"), "hello")

	tool := &mockTool{
		name: "ctx-checker",
		fn: func(ctx context.Context, _ string) (int, error) {
			span := trace.SpanFromContext(ctx)
			if !span.SpanContext().IsValid() {
				return 0, errors.New("expected valid span in context")
			}
			if ctx.Value(ctxKey("k")) != "hello" {
				return 0, errors.New("lost context value")
			}
			return 1, nil
		},
	}

	wrapped := WrapTool(inst, "ctx-checker", tool)
	_, err := wrapped.Call(ctx, "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWrapToolWithUse(t *testing.T) {
	exp, inst := newTraceOnlyInst()

	tool := WrapTool(inst, "strlen", &mockTool{
		name: "strlen",
		fn:   func(_ context.Context, s string) (int, error) { return len(s), nil },
	})

	result, step, err := reflow.Use(context.Background(), tool, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 5 {
		t.Fatalf("expected 5, got %d", result)
	}
	if step.Node != "strlen" {
		t.Fatalf("expected step node 'strlen', got %q", step.Node)
	}
	if step.Phase != "tool" {
		t.Fatalf("expected step phase 'tool', got %q", step.Phase)
	}



	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 OTel span, got %d", len(spans))
	}
	if step.Duration <= 0 {
		t.Fatal("expected positive step duration")
	}
}

// --- Metrics ---

func TestRunRecordsMetrics(t *testing.T) {
	_, reader, inst := newFullInst()

	node := &reflow.Func[int, int]{
		ActFn: reflow.Pass(func(n int) int { return n * 2 }),
	}

	_, err := Run(context.Background(), inst, "double", node, reflow.NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	metrics := collectMetrics(t, reader)

	// Run metrics
	if _, ok := metrics["reflow.run.duration"]; !ok {
		t.Fatal("expected reflow.run.duration metric")
	}
	if _, ok := metrics["reflow.run.count"]; !ok {
		t.Fatal("expected reflow.run.count metric")
	}

	// Step metrics (resolve + settle from runOnce)
	if _, ok := metrics["reflow.step.count"]; !ok {
		t.Fatal("expected reflow.step.count metric")
	}

	// Verify run count value
	m := metrics["reflow.run.count"]
	sum := m.Data.(metricdata.Sum[int64])
	if len(sum.DataPoints) != 1 {
		t.Fatalf("expected 1 run count data point, got %d", len(sum.DataPoints))
	}
	dp := sum.DataPoints[0]
	if dp.Value != 1 {
		t.Fatalf("expected run count 1, got %d", dp.Value)
	}

	// Check status attribute
	statusVal, ok := dp.Attributes.Value(attribute.Key("status"))
	if !ok {
		t.Fatal("expected status attribute on run count")
	}
	if statusVal.AsString() != "ok" {
		t.Fatalf("expected status 'ok', got %q", statusVal.AsString())
	}
}

func TestExportRecordsStepMetrics(t *testing.T) {
	_, reader, inst := newFullInst()

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Node: "parse", Phase: "resolve", Status: "ok", Duration: 10 * time.Millisecond},
			{Node: "parse", Phase: "settle", Status: "ok", Duration: 5 * time.Millisecond},
			{Node: "ollama.chat", Phase: "tool", Status: "ok", Duration: 200 * time.Millisecond},
		},
		Tags: map[string]string{},
	}

	inst.Export(context.Background(), "pipeline", meta)

	metrics := collectMetrics(t, reader)

	// Step metrics for resolve + settle
	if _, ok := metrics["reflow.step.count"]; !ok {
		t.Fatal("expected reflow.step.count metric")
	}

	// Tool metrics for the tool step
	if _, ok := metrics["reflow.tool.count"]; !ok {
		t.Fatal("expected reflow.tool.count metric")
	}

	// No run metrics from Export
	if _, ok := metrics["reflow.run.count"]; ok {
		t.Fatal("Export should not record reflow.run.count")
	}
}

func TestExportRecordsRetryMetric(t *testing.T) {
	_, reader, inst := newFullInst()

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Node: "classify", Phase: "resolve", Status: "ok", Duration: time.Millisecond},
			{Node: "classify", Phase: "settle", Status: "retry", Duration: time.Millisecond},
			{Node: "classify", Phase: "resolve", Status: "ok", Duration: time.Millisecond},
			{Node: "classify", Phase: "settle", Status: "ok", Duration: time.Millisecond},
		},
		Tags: map[string]string{},
	}

	inst.Export(context.Background(), "pipeline", meta)

	metrics := collectMetrics(t, reader)

	m, ok := metrics["reflow.retry.count"]
	if !ok {
		t.Fatal("expected reflow.retry.count metric")
	}
	sum := m.Data.(metricdata.Sum[int64])
	if len(sum.DataPoints) != 1 {
		t.Fatalf("expected 1 retry data point, got %d", len(sum.DataPoints))
	}
	if sum.DataPoints[0].Value != 1 {
		t.Fatalf("expected retry count 1, got %d", sum.DataPoints[0].Value)
	}
}

func TestExportRecordsHintMetrics(t *testing.T) {
	_, reader, inst := newFullInst()

	meta := reflow.Meta{
		Hints: []reflow.Hint{
			{Code: "json.malformed", Message: "bad input"},
			{Code: "json.malformed", Message: "another bad input"},
			{Code: "timeout", Message: "too slow"},
		},
		Trace: []reflow.Step{
			{Phase: "resolve", Status: "ok", Duration: time.Millisecond},
		},
		Tags: map[string]string{},
	}

	inst.Export(context.Background(), "pipeline", meta)

	metrics := collectMetrics(t, reader)

	m, ok := metrics["reflow.hint.count"]
	if !ok {
		t.Fatal("expected reflow.hint.count metric")
	}
	sum := m.Data.(metricdata.Sum[int64])

	// Two distinct hint codes: json.malformed (count 2) and timeout (count 1).
	totalHints := int64(0)
	for _, dp := range sum.DataPoints {
		totalHints += dp.Value
	}
	if totalHints != 3 {
		t.Fatalf("expected 3 total hints, got %d", totalHints)
	}
}

func TestStatusNormalization(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ok", "ok"},
		{"retry", "retry"},
		{"", "ok"},
		{"connection refused", "error"},
		{"timeout", "error"},
	}
	for _, tt := range tests {
		got := normalizeStatus(tt.input)
		if got != tt.want {
			t.Errorf("normalizeStatus(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNoMetricsWithoutMeter(t *testing.T) {
	reader, mp := newMeter()
	_, tp := newTracer()

	// Create instrumenter WITHOUT WithMeter.
	inst := NewInstrumenter(tp)

	node := &reflow.Func[int, int]{
		ActFn: reflow.Pass(func(n int) int { return n }),
	}

	_, err := Run(context.Background(), inst, "noop", node, reflow.NewEnvelope(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reader from the unused meter provider should have no metrics.
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		if len(sm.Metrics) > 0 {
			t.Fatalf("expected no metrics, got %d", len(sm.Metrics))
		}
	}

	_ = mp // keep the meter provider alive
}

func TestAllowedTagsFiltering(t *testing.T) {
	_, reader, inst := newFullInst("env")

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Node: "api", Phase: "tool", Status: "ok", Duration: 10 * time.Millisecond},
		},
		Tags: map[string]string{"env": "prod", "user_id": "123"},
	}

	inst.Export(context.Background(), "pipeline", meta)

	metrics := collectMetrics(t, reader)
	m := metrics["reflow.tool.count"]
	sum := m.Data.(metricdata.Sum[int64])
	dp := sum.DataPoints[0]

	// Allowed tag should be present.
	envVal, ok := dp.Attributes.Value(attribute.Key("reflow.tag.env"))
	if !ok {
		t.Fatal("expected reflow.tag.env attribute")
	}
	if envVal.AsString() != "prod" {
		t.Fatalf("expected 'prod', got %q", envVal.AsString())
	}

	// Disallowed tag should be absent.
	_, ok = dp.Attributes.Value(attribute.Key("reflow.tag.user_id"))
	if ok {
		t.Fatal("expected reflow.tag.user_id to be filtered out")
	}
}

func TestTagFuncOverridesAllowlist(t *testing.T) {
	_, tp := newTracer()
	reader, mp := newMeter()

	inst := NewInstrumenter(tp,
		WithMeter(mp),
		WithAllowedTags("env"),
		WithTagFunc(func(tags map[string]string) []attribute.KeyValue {
			// Custom function that emits a different key.
			if v, ok := tags["env"]; ok {
				return []attribute.KeyValue{attribute.String("custom.env", v)}
			}
			return nil
		}),
	)

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Node: "api", Phase: "tool", Status: "ok", Duration: 10 * time.Millisecond},
		},
		Tags: map[string]string{"env": "staging"},
	}

	inst.Export(context.Background(), "pipeline", meta)

	metrics := collectMetrics(t, reader)
	m := metrics["reflow.tool.count"]
	sum := m.Data.(metricdata.Sum[int64])
	dp := sum.DataPoints[0]

	// TagFunc should produce custom.env, not reflow.tag.env.
	customVal, ok := dp.Attributes.Value(attribute.Key("custom.env"))
	if !ok {
		t.Fatal("expected custom.env attribute from TagFunc")
	}
	if customVal.AsString() != "staging" {
		t.Fatalf("expected 'staging', got %q", customVal.AsString())
	}

	_, ok = dp.Attributes.Value(attribute.Key("reflow.tag.env"))
	if ok {
		t.Fatal("TagFunc should override allowlist; reflow.tag.env should not exist")
	}
}

func TestWrapToolRecordsToolMetrics(t *testing.T) {
	_, reader, inst := newFullInst()

	tool := WrapTool(inst, "my.api", &mockTool{
		name: "my.api",
		fn:   func(_ context.Context, s string) (int, error) { return len(s), nil },
	})

	_, err := tool.Call(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	metrics := collectMetrics(t, reader)

	m, ok := metrics["reflow.tool.count"]
	if !ok {
		t.Fatal("expected reflow.tool.count from WrapTool")
	}
	sum := m.Data.(metricdata.Sum[int64])
	if sum.DataPoints[0].Value != 1 {
		t.Fatalf("expected tool count 1, got %d", sum.DataPoints[0].Value)
	}

	toolVal, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("tool"))
	if !ok {
		t.Fatal("expected tool attribute")
	}
	if toolVal.AsString() != "my.api" {
		t.Fatalf("expected 'my.api', got %q", toolVal.AsString())
	}

	if _, ok := metrics["reflow.tool.duration"]; !ok {
		t.Fatal("expected reflow.tool.duration from WrapTool")
	}
}

func TestWrapToolAndExportDuplicateToolMetrics(t *testing.T) {
	_, reader, inst := newFullInst()

	tool := WrapTool(inst, "strlen", &mockTool{
		name: "strlen",
		fn:   func(_ context.Context, s string) (int, error) { return len(s), nil },
	})

	// Call tool directly (WrapTool records metrics).
	_, err := tool.Call(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Export a trace that also contains this tool call (Export records metrics).
	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Node: "strlen", Phase: "tool", Status: "ok", Duration: 10 * time.Millisecond},
		},
		Tags: map[string]string{},
	}
	inst.Export(context.Background(), "pipeline", meta)

	metrics := collectMetrics(t, reader)
	m := metrics["reflow.tool.count"]
	sum := m.Data.(metricdata.Sum[int64])

	// Both paths emit to the same metric with the same attributes,
	// so the counter should aggregate to 2.
	if sum.DataPoints[0].Value != 2 {
		t.Fatalf("expected tool count 2 (WrapTool + Export), got %d", sum.DataPoints[0].Value)
	}
}
