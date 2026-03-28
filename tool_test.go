package reflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// --- Tool interface ---

// mockTool implements Tool[string, int] for testing.
type mockTool struct {
	name string
	fn   func(context.Context, string) (int, error)
}

func (m *mockTool) Name() string                                     { return m.name }
func (m *mockTool) Call(ctx context.Context, in string) (int, error) { return m.fn(ctx, in) }

func TestUseSuccess(t *testing.T) {
	tool := &mockTool{
		name: "str.len",
		fn:   func(_ context.Context, s string) (int, error) { return len(s), nil },
	}

	result, step, err := Use(context.Background(), tool, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 5 {
		t.Fatalf("expected 5, got %d", result)
	}
	if step.Node != "str.len" {
		t.Fatalf("expected node 'str.len', got %q", step.Node)
	}
	if step.Phase != "tool" {
		t.Fatalf("expected phase 'tool', got %q", step.Phase)
	}
	if step.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", step.Status)
	}
	if step.Duration <= 0 {
		t.Fatal("expected positive duration")
	}
}

func TestUseError(t *testing.T) {
	tool := &mockTool{
		name: "failing",
		fn:   func(_ context.Context, _ string) (int, error) { return 0, errors.New("nope") },
	}

	_, step, err := Use(context.Background(), tool, "input")
	if err == nil {
		t.Fatal("expected error")
	}
	if step.Node != "failing" {
		t.Fatalf("expected node 'failing', got %q", step.Node)
	}
	if step.Status == "ok" {
		t.Fatal("expected non-ok status")
	}
}

func TestUseRecordsDuration(t *testing.T) {
	tool := &mockTool{
		name: "slow",
		fn: func(_ context.Context, _ string) (int, error) {
			time.Sleep(50 * time.Millisecond)
			return 1, nil
		},
	}

	_, step, _ := Use(context.Background(), tool, "x")
	if step.Duration < 50*time.Millisecond {
		t.Fatalf("expected at least 50ms, got %v", step.Duration)
	}
}

func TestUseWithStep(t *testing.T) {
	tool := &mockTool{
		name: "lookup",
		fn:   func(_ context.Context, s string) (int, error) { return len(s), nil },
	}

	env := NewEnvelope("data")
	_, step, _ := Use(context.Background(), tool, "test")
	env = env.WithStep(step)

	if len(env.Meta.Trace) != 1 {
		t.Fatalf("expected 1 trace step, got %d", len(env.Meta.Trace))
	}
	if env.Meta.Trace[0].Node != "lookup" {
		t.Fatalf("expected node 'lookup', got %q", env.Meta.Trace[0].Node)
	}
}

// --- UseRetry ---

func TestUseRetrySucceedsFirst(t *testing.T) {
	tool := &mockTool{
		name: "api",
		fn:   func(_ context.Context, _ string) (int, error) { return 42, nil },
	}

	result, steps, err := UseRetry(context.Background(), tool, "input", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 42 {
		t.Fatalf("expected 42, got %d", result)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", steps[0].Status)
	}
}

func TestUseRetrySingleAttemptNoLabel(t *testing.T) {
	tool := &mockTool{
		name: "api",
		fn:   func(_ context.Context, _ string) (int, error) { return 1, nil },
	}

	_, steps, _ := UseRetry(context.Background(), tool, "x", 1)
	if strings.Contains(steps[0].Node, "[") {
		t.Fatalf("single attempt should not have retry label, got %q", steps[0].Node)
	}
}

func TestUseRetrySucceedsAfterFailures(t *testing.T) {
	calls := 0
	tool := &mockTool{
		name: "flaky",
		fn: func(_ context.Context, _ string) (int, error) {
			calls++
			if calls < 3 {
				return 0, errors.New("timeout")
			}
			return 42, nil
		},
	}

	result, steps, err := UseRetry(context.Background(), tool, "input", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 42 {
		t.Fatalf("expected 42, got %d", result)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	if steps[0].Status == "ok" || steps[1].Status == "ok" {
		t.Fatal("first two should fail")
	}
	if steps[2].Status != "ok" {
		t.Fatal("third should succeed")
	}
	// Multi-attempt should have labels
	if !strings.Contains(steps[0].Node, "[1/5]") {
		t.Fatalf("expected attempt label, got %q", steps[0].Node)
	}
}

func TestUseRetryExhaustsAttempts(t *testing.T) {
	tool := &mockTool{
		name: "broken",
		fn:   func(_ context.Context, _ string) (int, error) { return 0, errors.New("fail") },
	}

	_, steps, err := UseRetry(context.Background(), tool, "input", 3)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
}

func TestUseRetryContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := &mockTool{
		name: "api",
		fn:   func(_ context.Context, _ string) (int, error) { return 1, nil },
	}

	_, steps, err := UseRetry(ctx, tool, "input", 5)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
}

// --- Tool in a pipeline (interface style) ---

func TestUseInPipeline(t *testing.T) {
	fetch := &mockTool{
		name: "fetch",
		fn:   func(_ context.Context, s string) (int, error) { return len(s), nil },
	}

	node := &Func[string, int]{
		ActFn: func(ctx context.Context, in Envelope[string]) (Envelope[int], error) {
			result, step, err := Use(ctx, fetch, in.Value)
			return Envelope[int]{Value: result, Meta: in.Meta}.WithStep(step), err
		},
	}

	out, err := Run(context.Background(), node, NewEnvelope("hello world"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 11 {
		t.Fatalf("expected 11, got %d", out.Value)
	}

	toolSteps := 0
	for _, s := range out.Meta.Trace {
		if s.Phase == "tool" && s.Node == "fetch" {
			toolSteps++
		}
	}
	if toolSteps != 1 {
		t.Fatalf("expected 1 tool step, got %d", toolSteps)
	}
}

// --- Invoke (ad-hoc) ---

func TestInvokeSuccess(t *testing.T) {
	result, step, err := Invoke(context.Background(), "my.api", func(_ context.Context) (string, error) {
		return "hello", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello" {
		t.Fatalf("expected 'hello', got %q", result)
	}
	if step.Node != "my.api" {
		t.Fatalf("expected node 'my.api', got %q", step.Node)
	}
	if step.Phase != "tool" {
		t.Fatalf("expected phase 'tool', got %q", step.Phase)
	}
	if step.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", step.Status)
	}
	if step.Duration <= 0 {
		t.Fatal("expected positive duration")
	}
}

func TestInvokeError(t *testing.T) {
	_, step, err := Invoke(context.Background(), "my.api", func(_ context.Context) (string, error) {
		return "", errors.New("connection refused")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if step.Status != "connection refused" {
		t.Fatalf("expected error in status, got %q", step.Status)
	}
	if step.Phase != "tool" {
		t.Fatalf("expected phase 'tool', got %q", step.Phase)
	}
}

func TestInvokeRecordsDuration(t *testing.T) {
	_, step, _ := Invoke(context.Background(), "slow.api", func(_ context.Context) (int, error) {
		time.Sleep(50 * time.Millisecond)
		return 42, nil
	})
	if step.Duration < 50*time.Millisecond {
		t.Fatalf("expected at least 50ms, got %v", step.Duration)
	}
}

func TestInvokeRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, step, err := Invoke(ctx, "my.api", func(ctx context.Context) (string, error) {
		return "", ctx.Err()
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if step.Status == "ok" {
		t.Fatal("expected non-ok status")
	}
}

func TestInvokeWithStep(t *testing.T) {
	env := NewEnvelope("data")

	_, step, _ := Invoke(context.Background(), "parse", func(_ context.Context) (int, error) {
		return 42, nil
	})
	env = env.WithStep(step)

	if len(env.Meta.Trace) != 1 {
		t.Fatalf("expected 1 trace step, got %d", len(env.Meta.Trace))
	}
	if env.Meta.Trace[0].Node != "parse" {
		t.Fatalf("expected node 'parse', got %q", env.Meta.Trace[0].Node)
	}
}

func TestInvokeMultipleCallsAccumulate(t *testing.T) {
	env := NewEnvelope("data")

	_, s1, _ := Invoke(context.Background(), "fetch", func(_ context.Context) (string, error) {
		return "raw", nil
	})
	_, s2, _ := Invoke(context.Background(), "parse", func(_ context.Context) (int, error) {
		return 42, nil
	})
	_, s3, _ := Invoke(context.Background(), "validate", func(_ context.Context) (bool, error) {
		return true, nil
	})
	env = env.WithStep(s1, s2, s3)

	if len(env.Meta.Trace) != 3 {
		t.Fatalf("expected 3 trace steps, got %d", len(env.Meta.Trace))
	}
	names := []string{env.Meta.Trace[0].Node, env.Meta.Trace[1].Node, env.Meta.Trace[2].Node}
	if names[0] != "fetch" || names[1] != "parse" || names[2] != "validate" {
		t.Fatalf("unexpected trace nodes: %v", names)
	}
}

// --- Try ---

func TestTrySucceedsFirst(t *testing.T) {
	calls := 0
	result, steps, err := Try(context.Background(), "my.api", 3, func(_ context.Context) (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Fatalf("expected 'ok', got %q", result)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", steps[0].Status)
	}
	if !strings.Contains(steps[0].Node, "1/3") {
		t.Fatalf("expected attempt label, got %q", steps[0].Node)
	}
}

func TestTrySucceedsAfterFailures(t *testing.T) {
	calls := 0
	result, steps, err := Try(context.Background(), "flaky.api", 5, func(_ context.Context) (int, error) {
		calls++
		if calls < 3 {
			return 0, errors.New("timeout")
		}
		return 42, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 42 {
		t.Fatalf("expected 42, got %d", result)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	// First two should be errors, third should be ok
	if steps[0].Status == "ok" || steps[1].Status == "ok" {
		t.Fatal("expected first two attempts to fail")
	}
	if steps[2].Status != "ok" {
		t.Fatalf("expected third attempt to succeed, got %q", steps[2].Status)
	}
}

func TestTryExhaustsAttempts(t *testing.T) {
	calls := 0
	_, steps, err := Try(context.Background(), "broken.api", 3, func(_ context.Context) (string, error) {
		calls++
		return "", errors.New("always fails")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	for i, s := range steps {
		if s.Status == "ok" {
			t.Fatalf("step %d should not be ok", i)
		}
	}
}

func TestTryDefaultAttempts(t *testing.T) {
	calls := 0
	_, _, _ = Try(context.Background(), "api", 0, func(_ context.Context) (int, error) {
		calls++
		return 0, errors.New("fail")
	})
	if calls != 1 {
		t.Fatalf("expected 1 call with attempts=0, got %d", calls)
	}
}

func TestTryContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, steps, err := Try(ctx, "my.api", 5, func(_ context.Context) (string, error) {
		return "should not reach", nil
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step (cancellation), got %d", len(steps))
	}
}

func TestTryContextCancelledMidRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, steps, err := Try(ctx, "my.api", 5, func(_ context.Context) (string, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return "", errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// Should have done 2 calls then hit cancelled context on attempt 3
	if calls != 2 {
		t.Fatalf("expected 2 calls before cancellation, got %d", calls)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps (2 attempts + 1 cancellation), got %d", len(steps))
	}
}

func TestTryWithStepIntegration(t *testing.T) {
	env := NewEnvelope("input")

	calls := 0
	result, steps, err := Try(context.Background(), "flaky", 3, func(_ context.Context) (string, error) {
		calls++
		if calls < 2 {
			return "", errors.New("nope")
		}
		return "done", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env = env.WithStep(steps...)

	if result != "done" {
		t.Fatalf("expected 'done', got %q", result)
	}
	if len(env.Meta.Trace) != 2 {
		t.Fatalf("expected 2 trace steps, got %d", len(env.Meta.Trace))
	}
	// First should be failure, second success
	if env.Meta.Trace[0].Status == "ok" {
		t.Fatal("first attempt should have failed")
	}
	if env.Meta.Trace[1].Status != "ok" {
		t.Fatal("second attempt should have succeeded")
	}
}

// --- Invoke in a real node ---

func TestInvokeInNamedNode(t *testing.T) {
	type fetcher struct{}

	fetch := func(ctx context.Context) (string, error) {
		return "response-data", nil
	}

	node := &Func[string, string]{
		ActFn: func(ctx context.Context, in Envelope[string]) (Envelope[string], error) {
			result, step, err := Invoke(ctx, "http.get", func(ctx context.Context) (string, error) {
				return fetch(ctx)
			})
			out := Envelope[string]{Value: result, Meta: in.Meta}.WithStep(step)
			if err != nil {
				return out, err
			}
			return out, nil
		},
	}

	out, err := Run(context.Background(), node, NewEnvelope("https://example.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "response-data" {
		t.Fatalf("expected 'response-data', got %q", out.Value)
	}

	// Should have: resolve + tool + settle in trace
	toolSteps := 0
	for _, s := range out.Meta.Trace {
		if s.Phase == "tool" {
			toolSteps++
			if s.Node != "http.get" {
				t.Fatalf("expected tool node 'http.get', got %q", s.Node)
			}
		}
	}
	if toolSteps != 1 {
		t.Fatalf("expected 1 tool step in trace, got %d", toolSteps)
	}
}

func TestInvokeInPipeline(t *testing.T) {
	// Two nodes, each calling a tool — trace should show both
	nodeA := &Func[string, string]{
		ActFn: func(ctx context.Context, in Envelope[string]) (Envelope[string], error) {
			result, step, err := Invoke(ctx, "step-a.fetch", func(_ context.Context) (string, error) {
				return "fetched:" + in.Value, nil
			})
			return Envelope[string]{Value: result, Meta: in.Meta}.WithStep(step), err
		},
	}
	nodeB := &Func[string, string]{
		ActFn: func(ctx context.Context, in Envelope[string]) (Envelope[string], error) {
			result, step, err := Invoke(ctx, "step-b.transform", func(_ context.Context) (string, error) {
				return strings.ToUpper(in.Value), nil
			})
			return Envelope[string]{Value: result, Meta: in.Meta}.WithStep(step), err
		},
	}

	pipeline := Chain[string, string, string](nodeA, nodeB)
	out, err := Run(context.Background(), pipeline, NewEnvelope("data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "FETCHED:DATA" {
		t.Fatalf("expected 'FETCHED:DATA', got %q", out.Value)
	}

	// Count tool steps
	toolSteps := 0
	for _, s := range out.Meta.Trace {
		if s.Phase == "tool" {
			toolSteps++
		}
	}
	if toolSteps != 2 {
		t.Fatalf("expected 2 tool steps in pipeline trace, got %d", toolSteps)
	}
}

// --- Property-based ---

func TestPBTInvokeAlwaysRecordsDuration(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.StringMatching(`[a-z.]{3,12}`).Draw(t, "name")
		shouldFail := rapid.Bool().Draw(t, "fail")

		_, step, _ := Invoke(context.Background(), name, func(_ context.Context) (int, error) {
			if shouldFail {
				return 0, errors.New("fail")
			}
			return 1, nil
		})

		if step.Node != name {
			t.Fatalf("expected node %q, got %q", name, step.Node)
		}
		if step.Phase != "tool" {
			t.Fatalf("expected phase 'tool', got %q", step.Phase)
		}
		if step.Duration < 0 {
			t.Fatalf("duration should not be negative: %v", step.Duration)
		}
		if shouldFail && step.Status == "ok" {
			t.Fatal("expected non-ok status on failure")
		}
		if !shouldFail && step.Status != "ok" {
			t.Fatalf("expected ok status on success, got %q", step.Status)
		}
	})
}

func TestPBTTryAttemptCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxAttempts := rapid.IntRange(1, 10).Draw(t, "attempts")
		failUntil := rapid.IntRange(0, maxAttempts).Draw(t, "failUntil")

		calls := 0
		_, steps, _ := Try(context.Background(), "api", maxAttempts, func(_ context.Context) (int, error) {
			calls++
			if calls <= failUntil {
				return 0, errors.New("fail")
			}
			return 1, nil
		})

		expectedCalls := failUntil + 1
		if failUntil >= maxAttempts {
			expectedCalls = maxAttempts
		}
		if calls != expectedCalls {
			t.Fatalf("expected %d calls, got %d (failUntil=%d, max=%d)", expectedCalls, calls, failUntil, maxAttempts)
		}
		if len(steps) != expectedCalls {
			t.Fatalf("expected %d steps, got %d", expectedCalls, len(steps))
		}
	})
}
