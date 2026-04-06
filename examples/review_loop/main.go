// Example: Review Loop
//
// Pipeline: draft -> settle -> retry with feedback
//
// A single node drafts an incident runbook. Settle rejects incomplete
// drafts and leaves structured hints. WithRetry feeds those hints back
// into the next iteration, so each pass gets a better handoff.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/vertexbuild/reflow"
)

type Incident struct {
	ID       string
	Service  string
	Symptoms string
}

type Runbook struct {
	Incident Incident
	Summary  string
	Actions  []string
	Owner    string
	Escalate bool
}

type DraftRunbook struct{}

func (DraftRunbook) Resolve(_ context.Context, in reflow.Envelope[Incident]) (reflow.Envelope[Incident], error) {
	return in, nil
}

func (DraftRunbook) Act(ctx context.Context, in reflow.Envelope[Incident]) (reflow.Envelope[Runbook], error) {
	draft, step, err := reflow.Invoke(ctx, "draft.runbook", func(context.Context) (Runbook, error) {
		runbook := Runbook{
			Incident: in.Value,
			Summary:  fmt.Sprintf("%s requires a recovery runbook.", in.Value.Service),
		}

		if len(in.HintsByCode("runbook.missing_actions")) > 0 {
			runbook.Actions = []string{
				"acknowledge incident in the on-call channel",
				"capture the last 15 minutes of service logs",
			}
		}

		if len(in.HintsByCode("runbook.missing_owner")) > 0 {
			switch {
			case strings.Contains(strings.ToLower(in.Value.Service), "payments"):
				runbook.Owner = "payments-oncall"
			default:
				runbook.Owner = "platform-oncall"
			}
		}

		if len(in.HintsByCode("runbook.missing_escalation")) > 0 {
			runbook.Escalate = true
		}

		return runbook, nil
	})

	out := reflow.Map(in, draft).WithStep(step)
	return out, err
}

func (DraftRunbook) Settle(_ context.Context, _ reflow.Envelope[Incident], out reflow.Envelope[Runbook], actErr error) (reflow.Envelope[Runbook], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}

	if strings.TrimSpace(out.Value.Summary) == "" {
		out = out.WithHint("runbook.missing_summary", "summary cannot be empty", "")
		return out, false, nil
	}
	if len(out.Value.Actions) < 2 {
		out = out.WithHint("runbook.missing_actions", "need at least two concrete recovery actions", "")
		return out, false, nil
	}
	if out.Value.Owner == "" {
		out = out.WithHint("runbook.missing_owner", "assign an owning team", "")
		return out, false, nil
	}
	if strings.Contains(strings.ToLower(out.Value.Incident.Symptoms), "outage") && !out.Value.Escalate {
		out = out.WithHint("runbook.missing_escalation", "customer-visible outages must set escalate=true", "")
		return out, false, nil
	}

	out = out.WithHint("runbook.ready", "draft passed review", "")
	return out, true, nil
}

func main() {
	ctx := context.Background()

	incident := Incident{
		ID:       "INC-2041",
		Service:  "payments-api",
		Symptoms: "customer-visible outage after deploy; checkout requests are failing",
	}

	out, err := reflow.Run(ctx, reflow.WithRetry(DraftRunbook{}, 4), reflow.NewEnvelope(incident))
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	retries := 0
	for _, step := range out.Meta.Trace {
		if step.Phase == "settle" && step.Status == "retry" {
			retries++
		}
	}

	fmt.Println("Review Loop")
	fmt.Println(strings.Repeat("=", 44))
	fmt.Printf("Incident:   %s (%s)\n", out.Value.Incident.ID, out.Value.Incident.Service)
	fmt.Printf("Summary:    %s\n", out.Value.Summary)
	fmt.Printf("Owner:      %s\n", out.Value.Owner)
	fmt.Printf("Escalate:   %t\n", out.Value.Escalate)
	fmt.Println("Actions:")
	for _, action := range out.Value.Actions {
		fmt.Printf("  - %s\n", action)
	}

	fmt.Println()
	fmt.Printf("Retries:    %d\n", retries)
	fmt.Printf("Hints:      %d\n", len(out.Meta.Hints))
	fmt.Printf("Trace:      %d steps\n", len(out.Meta.Trace))
	fmt.Println("Feedback:")
	for _, hint := range out.Meta.Hints {
		fmt.Printf("  [%s] %s\n", hint.Code, hint.Message)
	}
}
