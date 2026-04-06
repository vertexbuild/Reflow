// Example: Pipeline Ring
//
// Pipeline: stream events -> normalize -> classify -> score (with Ring)
//
// A stream of log events flows through a Pipeline. The scoring node
// uses a Ring to track a sliding window and detect bursts — when
// errors cluster, the scorer raises an alert hint.
//
// Run: go run ./examples/pipeline_ring/
package main

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/vertexbuild/reflow"
)

type RawEvent struct {
	Timestamp time.Time
	Source    string
	Line     string
}

type Event struct {
	Timestamp time.Time
	Source    string
	Level     string
	Message   string
}

type Scored struct {
	Event
	Score    float64
	Alert    bool
	WindowID int
}

// EmitEvents streams raw log events one at a time.
type EmitEvents struct{}

func (EmitEvents) Resolve(_ context.Context, in reflow.Envelope[[]RawEvent]) (reflow.Envelope[[]RawEvent], error) {
	return in, nil
}

func (EmitEvents) Act(_ context.Context, in reflow.Envelope[[]RawEvent]) iter.Seq2[reflow.Envelope[RawEvent], error] {
	return func(yield func(reflow.Envelope[RawEvent], error) bool) {
		for _, raw := range in.Value {
			if !yield(reflow.Map(in, raw), nil) {
				return
			}
		}
	}
}

func (EmitEvents) Settle(_ context.Context, _ reflow.Envelope[[]RawEvent], out reflow.Envelope[RawEvent], actErr error) (reflow.Envelope[RawEvent], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// Normalize trims and lowercases the raw line.
type Normalize struct{}

func (Normalize) Resolve(_ context.Context, in reflow.Envelope[RawEvent]) (reflow.Envelope[RawEvent], error) {
	return in, nil
}

func (Normalize) Act(_ context.Context, in reflow.Envelope[RawEvent]) (reflow.Envelope[RawEvent], error) {
	out := in
	out.Value.Line = strings.TrimSpace(strings.ToLower(in.Value.Line))
	return out, nil
}

func (Normalize) Settle(_ context.Context, _ reflow.Envelope[RawEvent], out reflow.Envelope[RawEvent], actErr error) (reflow.Envelope[RawEvent], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// Classify detects the log level from the normalized line.
type Classify struct{}

func (Classify) Resolve(_ context.Context, in reflow.Envelope[RawEvent]) (reflow.Envelope[RawEvent], error) {
	return in, nil
}

func (Classify) Act(_ context.Context, in reflow.Envelope[RawEvent]) (reflow.Envelope[RawEvent], error) {
	line := in.Value.Line
	out := in
	switch {
	case strings.Contains(line, "error") || strings.Contains(line, "fatal"):
		out = out.WithHint("classify.level", "error", in.Value.Source)
	case strings.Contains(line, "warn"):
		out = out.WithHint("classify.level", "warn", in.Value.Source)
	default:
		out = out.WithHint("classify.level", "info", in.Value.Source)
	}
	return out, nil
}

func (Classify) Settle(_ context.Context, _ reflow.Envelope[RawEvent], out reflow.Envelope[RawEvent], actErr error) (reflow.Envelope[RawEvent], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// Score uses a Ring to track the last N events and detect bursts.
// When errors cluster in the window, the score rises and triggers alerts.
type Score struct {
	Window *reflow.Ring[string]
}

func (s Score) Resolve(_ context.Context, in reflow.Envelope[RawEvent]) (reflow.Envelope[RawEvent], error) {
	return in, nil
}

func (s Score) Act(_ context.Context, in reflow.Envelope[RawEvent]) (reflow.Envelope[RawEvent], error) {
	// Read the level from hints left by Classify.
	level := "info"
	if hints := in.HintsByCode("classify.level"); len(hints) > 0 {
		level = hints[len(hints)-1].Message
	}

	s.Window.Push(level)

	// Count errors in the sliding window.
	errors := 0
	s.Window.Each(func(l string) bool {
		if l == "error" {
			errors++
		}
		return true
	})

	score := float64(errors) / float64(s.Window.Len())
	out := in
	if score >= 0.5 {
		out = out.WithHint("score.alert", fmt.Sprintf("%.0f%% errors in last %d events", score*100, s.Window.Len()), in.Value.Source)
	}
	out = out.WithHint("score.value", fmt.Sprintf("%.2f", score), in.Value.Source)
	return out, nil
}

func (s Score) Settle(_ context.Context, _ reflow.Envelope[RawEvent], out reflow.Envelope[RawEvent], actErr error) (reflow.Envelope[RawEvent], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

func main() {
	ctx := context.Background()
	now := time.Now()

	events := []RawEvent{
		{Timestamp: now, Source: "api", Line: "  GET /health 200 OK  "},
		{Timestamp: now.Add(time.Second), Source: "api", Line: "GET /users 200 OK"},
		{Timestamp: now.Add(2 * time.Second), Source: "api", Line: "POST /checkout 500 Error: payment timeout"},
		{Timestamp: now.Add(3 * time.Second), Source: "api", Line: "POST /checkout 500 Error: connection refused"},
		{Timestamp: now.Add(4 * time.Second), Source: "worker", Line: "Fatal: worker pool exhausted"},
		{Timestamp: now.Add(5 * time.Second), Source: "api", Line: "POST /checkout 500 Error: payment timeout"},
		{Timestamp: now.Add(6 * time.Second), Source: "api", Line: "GET /health 200 OK"},
		{Timestamp: now.Add(7 * time.Second), Source: "api", Line: "GET /users 200 OK"},
		{Timestamp: now.Add(8 * time.Second), Source: "api", Line: "GET /status 200 OK"},
		{Timestamp: now.Add(9 * time.Second), Source: "api", Line: "GET /health 200 OK"},
	}

	window := reflow.NewRing[string](5)
	pipeline := reflow.Pipeline[RawEvent]("log-pipeline",
		Normalize{},
		Classify{},
		Score{Window: window},
	)

	stream := reflow.Stream(ctx, EmitEvents{}, reflow.NewEnvelope(events))

	fmt.Println("Pipeline Ring")
	fmt.Println(strings.Repeat("=", 60))

	alerts := 0
	total := 0
	for env, err := range stream {
		if err != nil {
			fmt.Printf("error: %v\n", err)
			return
		}

		processed, err := reflow.Run(ctx, pipeline, env)
		if err != nil {
			fmt.Printf("error: %v\n", err)
			return
		}
		total++

		level := "info"
		if hints := processed.HintsByCode("classify.level"); len(hints) > 0 {
			level = hints[len(hints)-1].Message
		}
		score := "0.00"
		if hints := processed.HintsByCode("score.value"); len(hints) > 0 {
			score = hints[len(hints)-1].Message
		}
		alert := ""
		if hints := processed.HintsByCode("score.alert"); len(hints) > 0 {
			alert = " << " + hints[len(hints)-1].Message
			alerts++
		}

		fmt.Printf("%-7s %-5s  score=%s  %s%s\n",
			processed.Value.Source, level, score,
			truncate(processed.Value.Line, 40), alert,
		)
	}

	fmt.Println()
	fmt.Printf("Events:  %d\n", total)
	fmt.Printf("Alerts:  %d\n", alerts)
	fmt.Printf("Window:  %d capacity\n", window.Cap())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
