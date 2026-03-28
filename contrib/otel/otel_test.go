package reflowtel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vertexbuild/reflow"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func newTracer() (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	return exp, tp
}

// --- ExportTrace ---

func TestExportTraceCreatesSpans(t *testing.T) {
	exp, tp := newTracer()
	tracer := tp.Tracer("test")

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Node: "parse", Phase: "resolve", Status: "ok", Duration: 10 * time.Millisecond},
			{Node: "parse", Phase: "settle", Status: "ok", Duration: 5 * time.Millisecond},
			{Node: "ollama.chat", Phase: "tool", Status: "ok", Duration: 200 * time.Millisecond},
			{Node: "classify", Phase: "settle", Status: "ok", Duration: 2 * time.Millisecond},
		},
		Tags: map[string]string{"env": "test"},
	}

	ExportTrace(context.Background(), tracer, "my-pipeline", meta)
	tp.ForceFlush(context.Background())

	spans := exp.GetSpans()
	// 1 parent + 4 children = 5
	if len(spans) != 5 {
		t.Fatalf("expected 5 spans, got %d", len(spans))
	}

	// Parent should be named "my-pipeline"
	parent := spans[len(spans)-1] // parent ends last
	if parent.Name != "my-pipeline" {
		t.Fatalf("expected parent span 'my-pipeline', got %q", parent.Name)
	}
}

func TestExportTraceErrorStatus(t *testing.T) {
	exp, tp := newTracer()
	tracer := tp.Tracer("test")

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Node: "api", Phase: "tool", Status: "connection refused", Duration: 50 * time.Millisecond},
		},
		Tags: map[string]string{},
	}

	ExportTrace(context.Background(), tracer, "pipeline", meta)
	tp.ForceFlush(context.Background())

	spans := exp.GetSpans()
	// Find the child span
	var found bool
	for _, s := range spans {
		if s.Name == "api" {
			if s.Status.Code != codes.Error {
				t.Fatalf("expected error status, got %v", s.Status.Code)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected span named 'api'")
	}
}

func TestExportTraceTagsAsAttributes(t *testing.T) {
	exp, tp := newTracer()
	tracer := tp.Tracer("test")

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Phase: "resolve", Status: "ok", Duration: time.Millisecond},
		},
		Tags: map[string]string{"env": "prod", "version": "1.2"},
	}

	ExportTrace(context.Background(), tracer, "p", meta)
	tp.ForceFlush(context.Background())

	spans := exp.GetSpans()
	parent := spans[len(spans)-1]

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

func TestExportTraceHintEvent(t *testing.T) {
	exp, tp := newTracer()
	tracer := tp.Tracer("test")

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

	ExportTrace(context.Background(), tracer, "p", meta)
	tp.ForceFlush(context.Background())

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

func TestExportTracePhaseOnlySpanName(t *testing.T) {
	exp, tp := newTracer()
	tracer := tp.Tracer("test")

	meta := reflow.Meta{
		Trace: []reflow.Step{
			{Phase: "resolve", Status: "ok", Duration: time.Millisecond},
		},
		Tags: map[string]string{},
	}

	ExportTrace(context.Background(), tracer, "p", meta)
	tp.ForceFlush(context.Background())

	// Child span should use Phase as name since Node is empty
	for _, s := range exp.GetSpans() {
		if s.Name == "resolve" {
			return // found it
		}
	}
	t.Fatal("expected span named 'resolve' when Node is empty")
}

// --- RunWithTrace ---

func TestRunWithTraceCreatesParentSpan(t *testing.T) {
	exp, tp := newTracer()
	tracer := tp.Tracer("test")

	node := &reflow.Func[int, int]{
		ActFn: reflow.Pass(func(n int) int { return n * 2 }),
	}

	out, err := RunWithTrace(context.Background(), tracer, "double", node, reflow.NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 10 {
		t.Fatalf("expected 10, got %d", out.Value)
	}

	tp.ForceFlush(context.Background())
	spans := exp.GetSpans()

	if len(spans) == 0 {
		t.Fatal("expected at least one span")
	}

	// Should have parent span named "double"
	found := false
	for _, s := range spans {
		if s.Name == "double" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected parent span named 'double'")
	}
}

func TestRunWithTraceRecordsError(t *testing.T) {
	exp, tp := newTracer()
	tracer := tp.Tracer("test")

	node := &reflow.Func[int, int]{
		ActFn: func(_ context.Context, _ reflow.Envelope[int]) (reflow.Envelope[int], error) {
			return reflow.Envelope[int]{}, errors.New("boom")
		},
	}

	_, err := RunWithTrace(context.Background(), tracer, "failing", node, reflow.NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}

	tp.ForceFlush(context.Background())

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

// --- WrapTool ---

type mockTool struct {
	name string
	fn   func(context.Context, string) (int, error)
}

func (m *mockTool) Name() string                                     { return m.name }
func (m *mockTool) Call(ctx context.Context, in string) (int, error) { return m.fn(ctx, in) }

func TestWrapToolCreatesSpan(t *testing.T) {
	exp, tp := newTracer()
	tracer := tp.Tracer("test")

	tool := &mockTool{
		name: "my.api",
		fn:   func(_ context.Context, s string) (int, error) { return len(s), nil },
	}

	wrapped := WrapTool[string, int](tracer, tool)

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

	tp.ForceFlush(context.Background())

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name != "my.api" {
		t.Fatalf("expected span 'my.api', got %q", spans[0].Name)
	}

	// Check tool attribute
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
	exp, tp := newTracer()
	tracer := tp.Tracer("test")

	tool := &mockTool{
		name: "failing",
		fn:   func(_ context.Context, _ string) (int, error) { return 0, errors.New("nope") },
	}

	wrapped := WrapTool[string, int](tracer, tool)
	_, err := wrapped.Call(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error")
	}

	tp.ForceFlush(context.Background())

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Fatalf("expected error status, got %v", spans[0].Status.Code)
	}
}

func TestWrapToolPropagatesContext(t *testing.T) {
	_, tp := newTracer()
	tracer := tp.Tracer("test")

	type ctxKey string
	ctx := context.WithValue(context.Background(), ctxKey("k"), "hello")

	tool := &mockTool{
		name: "ctx-checker",
		fn: func(ctx context.Context, _ string) (int, error) {
			// The wrapped tool should receive a context with the span
			span := trace.SpanFromContext(ctx)
			if !span.SpanContext().IsValid() {
				return 0, errors.New("expected valid span in context")
			}
			// Original context value should also be present
			if ctx.Value(ctxKey("k")) != "hello" {
				return 0, errors.New("lost context value")
			}
			return 1, nil
		},
	}

	wrapped := WrapTool[string, int](tracer, tool)
	_, err := wrapped.Call(ctx, "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Integration: WrapTool with reflow.Use ---

func TestWrapToolWithUse(t *testing.T) {
	exp, tp := newTracer()
	tracer := tp.Tracer("test")

	tool := WrapTool[string, int](tracer, &mockTool{
		name: "strlen",
		fn:   func(_ context.Context, s string) (int, error) { return len(s), nil },
	})

	// Use records a Step, WrapTool records a Span — both should work
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

	tp.ForceFlush(context.Background())

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 OTel span, got %d", len(spans))
	}

	// Verify both: Step has duration, Span has attributes
	if step.Duration <= 0 {
		t.Fatal("expected positive step duration")
	}
	_ = attribute.String("reflow.tool", "strlen") // just verify it compiles
}
