package reflow

import (
	"context"
	"fmt"
	"time"
)

// Tool is something that can be called with an input and returns an output.
// Implement this for any external capability — an LLM, a database, an API,
// a validator, a cache. The Name identifies the tool in trace output.
//
//	type MyAPI struct { client *http.Client }
//
//	func (a *MyAPI) Name() string                                          { return "my.api" }
//	func (a *MyAPI) Call(ctx context.Context, req Request) (Response, error) { ... }
//
// Use [Use] and [UseRetry] to call tools with automatic tracing.
type Tool[I, O any] interface {
	Name() string
	Call(context.Context, I) (O, error)
}

// Use calls a Tool and records the result as a trace step.
// The returned Step has Phase="tool", Duration, and Status="ok" or the error.
//
//	resp, step, err := reflow.Use(ctx, c.LLM, messages)
//	out = out.WithStep(step)
func Use[I, O any](ctx context.Context, t Tool[I, O], input I) (O, Step, error) {
	start := time.Now()
	result, err := t.Call(ctx, input)
	elapsed := time.Since(start)

	status := "ok"
	if err != nil {
		status = err.Error()
	}

	step := Step{
		Node:     t.Name(),
		Phase:    "tool",
		Status:   status,
		Duration: elapsed,
	}

	return result, step, err
}

// UseRetry calls a Tool with retries, recording each attempt as a trace step.
// Returns the result from the first successful call, or the last error
// if all attempts fail. Respects context cancellation between attempts.
//
//	resp, steps, err := reflow.UseRetry(ctx, c.LLM, messages, 3)
//	out = out.WithStep(steps...)
func UseRetry[I, O any](ctx context.Context, t Tool[I, O], input I, attempts int) (O, []Step, error) {
	if attempts <= 0 {
		attempts = 1
	}

	var steps []Step
	var lastErr error

	for i := range attempts {
		if err := ctx.Err(); err != nil {
			var zero O
			steps = append(steps, Step{
				Node:   t.Name(),
				Phase:  "tool",
				Status: err.Error(),
			})
			return zero, steps, err
		}

		result, step, err := Use(ctx, t, input)
		if attempts > 1 {
			step.Node = fmt.Sprintf("%s[%d/%d]", t.Name(), i+1, attempts)
		}
		steps = append(steps, step)

		if err == nil {
			return result, steps, nil
		}
		lastErr = err
	}

	var zero O
	return zero, steps, lastErr
}

// Invoke calls an ad-hoc function and records the result as a trace step.
// Use this for one-off calls that don't need a formal Tool implementation.
//
//	result, step, err := reflow.Invoke(ctx, "parse.json", func(ctx context.Context) (JSON, error) {
//	    return parseJSON(raw)
//	})
//	out = out.WithStep(step)
func Invoke[T any](ctx context.Context, name string, fn func(context.Context) (T, error)) (T, Step, error) {
	start := time.Now()
	result, err := fn(ctx)
	elapsed := time.Since(start)

	status := "ok"
	if err != nil {
		status = err.Error()
	}

	step := Step{
		Node:     name,
		Phase:    "tool",
		Status:   status,
		Duration: elapsed,
	}

	return result, step, err
}

// Try calls an ad-hoc function with retries, recording each attempt.
// Use this for one-off calls that don't need a formal Tool implementation.
//
//	resp, steps, err := reflow.Try(ctx, "flaky.api", 3, func(ctx context.Context) (string, error) {
//	    return callAPI(ctx, input)
//	})
//	out = out.WithStep(steps...)
func Try[T any](ctx context.Context, name string, attempts int, fn func(context.Context) (T, error)) (T, []Step, error) {
	if attempts <= 0 {
		attempts = 1
	}

	var steps []Step
	var lastErr error

	for i := range attempts {
		if err := ctx.Err(); err != nil {
			var zero T
			steps = append(steps, Step{
				Node:   name,
				Phase:  "tool",
				Status: err.Error(),
			})
			return zero, steps, err
		}

		result, step, err := Invoke(ctx, name, fn)
		if attempts > 1 {
			step.Node = fmt.Sprintf("%s[%d/%d]", name, i+1, attempts)
		}
		steps = append(steps, step)

		if err == nil {
			return result, steps, nil
		}
		lastErr = err
	}

	var zero T
	return zero, steps, lastErr
}
