package reflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// --- Single pass execution ---

func TestRunSinglePass(t *testing.T) {
	node := &Func[int, int]{ActFn: Pass(func(n int) int { return n * 2 })}
	out, err := Run(context.Background(), node, NewEnvelope(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != 10 {
		t.Fatalf("expected 10, got %d", out.Value)
	}
}

func TestRunDelegatesToRunner(t *testing.T) {
	// WithRetry returns a Runner. Verify Run delegates to it.
	calls := 0
	node := WithRetry(&Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			calls++
			return in, nil
		},
		SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
			if calls < 2 {
				return out, false, nil
			}
			return out, true, nil
		},
	}, 5)

	_, err := Run(context.Background(), node, NewEnvelope(0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls via Runner delegation, got %d", calls)
	}
}

// --- Error handling ---

func TestRunResolveError(t *testing.T) {
	node := &Func[int, int]{
		ResolveFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("resolve failed")
		},
		ActFn: Pass(func(n int) int { return n }),
	}
	_, err := Run(context.Background(), node, NewEnvelope(0))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolve") {
		t.Fatalf("error should mention resolve: %v", err)
	}
}

func TestRunSettleError(t *testing.T) {
	node := &Func[int, int]{
		ActFn: Pass(func(n int) int { return n }),
		SettleFn: func(_ context.Context, _ Envelope[int], _ Envelope[int], _ error) (Envelope[int], bool, error) {
			return Envelope[int]{}, false, errors.New("settle failed")
		},
	}
	_, err := Run(context.Background(), node, NewEnvelope(0))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "settle") {
		t.Fatalf("error should mention settle: %v", err)
	}
}

func TestRunActErrorPassedToSettle(t *testing.T) {
	var receivedErr error
	node := &Func[int, int]{
		ActFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, errors.New("act broke")
		},
		SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], actErr error) (Envelope[int], bool, error) {
			receivedErr = actErr
			// Recover from it
			return Envelope[int]{Value: -1}, true, nil
		},
	}

	out, err := Run(context.Background(), node, NewEnvelope(0))
	if err != nil {
		t.Fatalf("expected recovery, got error: %v", err)
	}
	if receivedErr == nil || receivedErr.Error() != "act broke" {
		t.Fatalf("settle should receive act error, got: %v", receivedErr)
	}
	if out.Value != -1 {
		t.Fatalf("expected recovered value -1, got %d", out.Value)
	}
}

func TestRunSettleRecovery(t *testing.T) {
	node := &Func[string, string]{
		ActFn: func(_ context.Context, _ Envelope[string]) (Envelope[string], error) {
			return Envelope[string]{}, errors.New("act failed")
		},
		SettleFn: func(_ context.Context, in Envelope[string], _ Envelope[string], actErr error) (Envelope[string], bool, error) {
			if actErr != nil {
				return Envelope[string]{Value: "recovered: " + actErr.Error(), Meta: in.Meta}, true, nil
			}
			return Envelope[string]{}, true, nil
		},
	}

	out, err := Run(context.Background(), node, NewEnvelope("input"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "recovered: act failed" {
		t.Fatalf("expected recovery, got %q", out.Value)
	}
}

// --- Context ---

func TestRunContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	node := &Func[int, int]{
		ResolveFn: func(ctx context.Context, _ Envelope[int]) (Envelope[int], error) {
			return Envelope[int]{}, ctx.Err()
		},
		ActFn: Pass(func(n int) int { return n }),
	}
	_, err := Run(ctx, node, NewEnvelope(1))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

// --- Trace ---

func TestRunTraceRecording(t *testing.T) {
	node := &Func[int, int]{
		ActFn: Pass(func(n int) int { return n }),
	}
	out, err := Run(context.Background(), node, NewEnvelope(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Meta.Trace.Len() < 2 {
		t.Fatalf("expected at least 2 trace steps, got %d", out.Meta.Trace.Len())
	}
	if out.Meta.Trace.Slice()[0].Phase != "resolve" {
		t.Fatalf("expected resolve trace, got %+v", out.Meta.Trace.Slice()[0])
	}
	if out.Meta.Trace.Slice()[1].Phase != "settle" {
		t.Fatalf("expected settle trace, got %+v", out.Meta.Trace.Slice()[1])
	}
}

func TestRunTraceRecordsPhases(t *testing.T) {
	node := WithRetry(&Func[int, int]{
		ActFn: Pass(func(n int) int { return n }),
	}, 1)

	out, err := Run(context.Background(), node, NewEnvelope(0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Single pass records resolve + settle
	if out.Meta.Trace.Len() < 2 {
		t.Fatalf("expected at least 2 trace steps, got %d", out.Meta.Trace.Len())
	}
	hasResolve, hasSettle := false, false
	for _, s := range out.Meta.Trace.Slice() {
		if s.Phase == "resolve" {
			hasResolve = true
		}
		if s.Phase == "settle" {
			hasSettle = true
		}
	}
	if !hasResolve || !hasSettle {
		t.Fatalf("expected resolve and settle in trace, got %+v", out.Meta.Trace.Slice())
	}
}

func TestRunTraceAccumulatesAcrossRetries(t *testing.T) {
	iteration := 0
	node := WithRetry(&Func[int, int]{
		ActFn: Pass(func(n int) int { return n }),
		SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
			iteration++
			return out, iteration >= 3, nil
		},
	}, 5)

	out, err := Run(context.Background(), node, NewEnvelope(0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 3 iterations × 2 trace steps (resolve + settle) = 6
	if out.Meta.Trace.Len() != 6 {
		t.Fatalf("expected 6 trace steps for 3 iterations, got %d: %+v", out.Meta.Trace.Len(), out.Meta.Trace.Slice())
	}
}

func TestWithRetryPreservesToolSteps(t *testing.T) {
	iteration := 0
	node := WithRetry(&Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			iteration++
			// Simulate a tool call that adds a trace step (like reflow.Use does).
			out := in.CarryMeta(in.Value)
			out = out.WithStep(Step{Node: "my.tool", Phase: "tool", Status: "ok"})
			return out, nil
		},
		SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
			return out, iteration >= 2, nil
		},
	}, 3)

	out, err := Run(context.Background(), node, NewEnvelope(0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: 2 iterations × (resolve + settle + tool) = 6 steps
	toolSteps := 0
	for _, s := range out.Meta.Trace.Slice() {
		if s.Phase == "tool" {
			toolSteps++
		}
	}
	if toolSteps != 2 {
		t.Fatalf("expected 2 tool steps from 2 iterations, got %d; trace: %+v", toolSteps, out.Meta.Trace.Slice())
	}
}

func TestRunTraceDoesNotCorruptInput(t *testing.T) {
	in := NewEnvelope(1)
	// Pre-populate some trace to create a backing array with potential capacity
	in.Meta.Trace = in.Meta.Trace.append(Step{Phase: "prior", Status: "ok"})

	node := &Func[int, int]{ActFn: Pass(func(n int) int { return n })}
	_, err := Run(context.Background(), node, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The original input trace should be untouched
	if in.Meta.Trace.Len() != 1 {
		t.Fatalf("input trace was corrupted: expected 1 step, got %d: %+v", in.Meta.Trace.Len(), in.Meta.Trace.Slice())
	}
	if in.Meta.Trace.Slice()[0].Phase != "prior" {
		t.Fatalf("input trace was corrupted: expected 'prior', got %+v", in.Meta.Trace.Slice()[0])
	}
}

// --- WithRetry ---

func TestWithRetrySettleLoop(t *testing.T) {
	attempt := 0
	node := WithRetry(&Func[string, string]{
		ResolveFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			for _, h := range in.HintsByCode("quality.low") {
				in = in.CarryMeta(in.Value + " (improved:" + h.Message + ")")
			}
			return in, nil
		},
		ActFn: func(_ context.Context, in Envelope[string]) (Envelope[string], error) {
			attempt++
			return in.CarryMeta(fmt.Sprintf("v%d:%s", attempt, in.Value)), nil
		},
		SettleFn: func(_ context.Context, _ Envelope[string], out Envelope[string], _ error) (Envelope[string], bool, error) {
			if attempt < 3 {
				out = out.WithHint("quality.low", fmt.Sprintf("attempt %d", attempt), "")
				return out, false, nil
			}
			return out, true, nil
		},
	}, 5)

	out, err := Run(context.Background(), node, NewEnvelope("draft"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempt != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempt)
	}
	if !strings.Contains(out.Value, "v3") {
		t.Fatalf("expected v3 in output, got %q", out.Value)
	}
}

func TestWithRetryExhaustion(t *testing.T) {
	node := WithRetry(&Func[int, int]{
		ActFn: Pass(func(n int) int { return n }),
		SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
			return out, false, nil
		},
	}, 3)

	_, err := Run(context.Background(), node, NewEnvelope(0))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "did not settle after 3 iterations") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWithRetryDefaultMaxIter(t *testing.T) {
	calls := 0
	node := WithRetry(&Func[int, int]{
		ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			calls++
			return in, nil
		},
	}, 0) // 0 defaults to 1

	_, err := Run(context.Background(), node, NewEnvelope(42))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestWithRetryHintFeedback(t *testing.T) {
	// Verify hints from settle are merged back into input for next resolve.
	// Each iteration adds exactly 1 hint, so resolve should see 0, 1, 2.
	var resolveHintCounts []int
	node := WithRetry(&Func[int, int]{
		ResolveFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
			resolveHintCounts = append(resolveHintCounts, len(in.HintsByCode("feedback")))
			return in, nil
		},
		ActFn: Pass(func(n int) int { return n }),
		SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
			if len(resolveHintCounts) < 3 {
				out = out.WithHint("feedback", "try harder", "")
				return out, false, nil
			}
			return out, true, nil
		},
	}, 5)

	_, err := Run(context.Background(), node, NewEnvelope(0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolveHintCounts) != 3 {
		t.Fatalf("expected 3 resolve calls, got %d", len(resolveHintCounts))
	}
	// Each iteration adds exactly 1 feedback hint: 0, 1, 2
	expected := []int{0, 1, 2}
	for i, want := range expected {
		if resolveHintCounts[i] != want {
			t.Fatalf("resolve %d: expected %d feedback hints, got %d (all: %v)", i, want, resolveHintCounts[i], resolveHintCounts)
		}
	}
}

func TestWithRetryResolveErrorStopsLoop(t *testing.T) {
	iteration := 0
	node := WithRetry(&Func[int, int]{
		ResolveFn: func(_ context.Context, _ Envelope[int]) (Envelope[int], error) {
			iteration++
			if iteration == 2 {
				return Envelope[int]{}, errors.New("resolve broke on iter 2")
			}
			return NewEnvelope(0), nil
		},
		ActFn: Pass(func(n int) int { return n }),
		SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
			return out, false, nil // never settle
		},
	}, 5)

	_, err := Run(context.Background(), node, NewEnvelope(0))
	if err == nil {
		t.Fatal("expected error")
	}
	if iteration != 2 {
		t.Fatalf("expected to stop at iteration 2, got %d", iteration)
	}
}

// --- Property-based ---

func TestPBTMaxIterBound(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxIter := rapid.IntRange(1, 20).Draw(t, "maxIter")

		iterations := 0
		node := WithRetry(&Func[int, int]{
			ActFn: func(_ context.Context, in Envelope[int]) (Envelope[int], error) {
				iterations++
				return in, nil
			},
			SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
				return out, false, nil
			},
		}, maxIter)

		Run(context.Background(), node, NewEnvelope(0))

		if iterations != maxIter {
			t.Fatalf("ran %d iterations, expected exactly %d", iterations, maxIter)
		}
	})
}

func TestPBTConvergesAtK(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxIter := rapid.IntRange(2, 15).Draw(t, "maxIter")
		settleAt := rapid.IntRange(0, maxIter-1).Draw(t, "settleAt")

		iteration := 0
		node := WithRetry(&Func[int, int]{
			ActFn: Pass(func(n int) int { return n }),
			SettleFn: func(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
				current := iteration
				iteration++
				if current == settleAt {
					return out, true, nil
				}
				return out, false, nil
			},
		}, maxIter)

		out, err := Run(context.Background(), node, NewEnvelope(99))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Value != 99 {
			t.Fatalf("expected 99, got %d", out.Value)
		}
		if iteration != settleAt+1 {
			t.Fatalf("expected %d settle calls, got %d", settleAt+1, iteration)
		}
	})
}

func TestPBTHintAccumulation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxIter := rapid.IntRange(2, 10).Draw(t, "maxIter")

		node := WithRetry(&Func[int, int]{
			ActFn: Pass(func(n int) int { return n }),
			SettleFn: func(_ context.Context, in Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
				out = out.WithHint("iter", fmt.Sprintf("%d", in.Meta.Hints.Len()), "")
				return out, false, nil
			},
		}, maxIter)

		Run(context.Background(), node, NewEnvelope(0))
	})
}
