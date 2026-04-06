// Example: Stream Router
//
// Pipeline: stream inbox -> triage per item -> split urgent/standard ->
// pool specialist handling -> merge results
//
// This shows pull-based streaming, per-item settle, bounded concurrency,
// and cost-aware routing through different specialist lanes.
package main

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/vertexbuild/reflow"
)

type Message struct {
	ID      string
	Subject string
	Body    string
}

type Routed struct {
	Message  Message
	Priority string
	Queue    string
	Reason   string
}

type Handled struct {
	Routed
	Desk   string
	Action string
}

type InboxTriage struct{}

func (InboxTriage) Resolve(_ context.Context, in reflow.Envelope[[]Message]) (reflow.Envelope[[]Message], error) {
	return in, nil
}

func (InboxTriage) Act(_ context.Context, in reflow.Envelope[[]Message]) iter.Seq2[reflow.Envelope[Routed], error] {
	return func(yield func(reflow.Envelope[Routed], error) bool) {
		for _, msg := range in.Value {
			routed := classify(msg)
			env := reflow.Map(in, routed)
			if routed.Queue == "drop" {
				env = env.WithHint("triage.drop", routed.Reason, msg.ID)
			} else {
				env = env.WithHint("triage.route", fmt.Sprintf("%s -> %s", msg.ID, routed.Queue), routed.Reason)
			}
			if !yield(env, nil) {
				return
			}
		}
	}
}

func (InboxTriage) Settle(_ context.Context, _ reflow.Envelope[[]Message], out reflow.Envelope[Routed], actErr error) (reflow.Envelope[Routed], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	if out.Value.Queue == "drop" {
		return out, false, nil
	}
	return out, true, nil
}

type Desk struct {
	Name       string
	Action     string
	Processing time.Duration
}

func (d Desk) Resolve(_ context.Context, in reflow.Envelope[Routed]) (reflow.Envelope[Routed], error) {
	return in, nil
}

func (d Desk) Act(_ context.Context, in reflow.Envelope[Routed]) (reflow.Envelope[Handled], error) {
	time.Sleep(d.Processing)
	out := reflow.Map(in, Handled{
		Routed: in.Value,
		Desk:   d.Name,
		Action: d.Action,
	})
	out = out.WithHint("desk.handled", fmt.Sprintf("%s handled by %s", in.Value.Message.ID, d.Name), d.Action)
	return out, nil
}

func (d Desk) Settle(_ context.Context, _ reflow.Envelope[Routed], out reflow.Envelope[Handled], actErr error) (reflow.Envelope[Handled], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

func main() {
	ctx := context.Background()

	inbox := []Message{
		{ID: "MSG-100", Subject: "Question about invoice due date", Body: "Can you confirm when invoice INV-2004 is due?"},
		{ID: "MSG-101", Subject: "URGENT outage in checkout", Body: "Checkout is down for all customers. We need help immediately."},
		{ID: "MSG-102", Subject: "Weekly newsletter", Body: "Marketing blast for this week."},
		{ID: "MSG-103", Subject: "Reset link expired", Body: "My password reset link expired again."},
		{ID: "MSG-104", Subject: "Refund request", Body: "Please review duplicate billing on invoice INV-2005."},
		{ID: "MSG-105", Subject: "P1 API errors", Body: "Seeing elevated 502s on the public API after the deploy."},
	}

	stream := reflow.Stream(ctx, InboxTriage{}, reflow.NewEnvelope(inbox))
	urgent, standard := reflow.Split(stream, func(env reflow.Envelope[Routed]) bool {
		return env.Value.Priority == "urgent"
	})

	urgentLane := reflow.Pool(ctx, Desk{
		Name:       "incident-desk",
		Action:     "page on-call and open an incident bridge",
		Processing: 30 * time.Millisecond,
	}, urgent, 2)

	standardLane := reflow.Pool(ctx, Desk{
		Name:       "support-desk",
		Action:     "respond with known next steps",
		Processing: 10 * time.Millisecond,
	}, standard, 3)

	handled, err := reflow.Collect(reflow.Merge(urgentLane, standardLane))
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	urgentCount := 0
	standardCount := 0
	totalHints := 0
	totalTrace := 0

	fmt.Println("Stream Router")
	fmt.Println(strings.Repeat("=", 44))
	for _, env := range handled {
		if env.Value.Priority == "urgent" {
			urgentCount++
		} else {
			standardCount++
		}
		totalHints += len(env.Meta.Hints)
		totalTrace += len(env.Meta.Trace)
		fmt.Printf("%s  %-8s  %-13s  %s\n",
			env.Value.Message.ID,
			env.Value.Priority,
			env.Value.Desk,
			env.Value.Action,
		)
	}

	fmt.Println()
	fmt.Printf("Urgent lane:   %d\n", urgentCount)
	fmt.Printf("Standard lane: %d\n", standardCount)
	fmt.Printf("Dropped:       %d\n", len(inbox)-len(handled))
	fmt.Printf("Hints:         %d\n", totalHints)
	fmt.Printf("Trace:         %d steps across handled items\n", totalTrace)
}

func classify(msg Message) Routed {
	text := strings.ToLower(msg.Subject + " " + msg.Body)

	switch {
	case strings.Contains(text, "newsletter"):
		return Routed{Message: msg, Queue: "drop", Reason: "non-support traffic"}
	case strings.Contains(text, "urgent") || strings.Contains(text, "p1") || strings.Contains(text, "outage") || strings.Contains(text, "502"):
		return Routed{
			Message:  msg,
			Priority: "urgent",
			Queue:    "incident",
			Reason:   "customer-facing issue needs rapid response",
		}
	default:
		return Routed{
			Message:  msg,
			Priority: "standard",
			Queue:    "support",
			Reason:   "handled by normal support flow",
		}
	}
}
