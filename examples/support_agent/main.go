// Example: Support Graph
//
// Compose: triage -> enrich (fork/join) -> generate response (tool) ->
// validate with retry
//
// The graph is the control plane. The LLM is one tool in it. Swap
// FakeLLM for llm.AsTool(provider, "llm") and this graph runs against
// a real model. The graph does not change.
//
// Run: go run ./examples/support_agent/
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/vertexbuild/reflow"
)

type Ticket struct {
	ID       string
	Customer string
	Subject  string
	Body     string
}

type Context struct {
	Ticket   Ticket
	Intent   string
	Priority string
	Account  string
	History  string
}

type Draft struct {
	Context  Context
	Response string
}

// --- Nodes ---

// Triage classifies intent and priority from the ticket text.
type Triage struct{}

func (Triage) Resolve(_ context.Context, in reflow.Envelope[Ticket]) (reflow.Envelope[Ticket], error) {
	return in, nil
}

func (Triage) Act(_ context.Context, in reflow.Envelope[Ticket]) (reflow.Envelope[Context], error) {
	text := strings.ToLower(in.Value.Subject + " " + in.Value.Body)
	c := Context{Ticket: in.Value}

	switch {
	case strings.Contains(text, "refund") || strings.Contains(text, "charged"):
		c.Intent = "billing"
	case strings.Contains(text, "password") || strings.Contains(text, "login"):
		c.Intent = "account"
	case strings.Contains(text, "outage") || strings.Contains(text, "down") || strings.Contains(text, "error"):
		c.Intent = "technical"
	default:
		c.Intent = "general"
	}

	switch {
	case strings.Contains(text, "urgent") || strings.Contains(text, "outage"):
		c.Priority = "high"
	default:
		c.Priority = "normal"
	}

	out := reflow.Map(in, c)
	out = out.WithHint("triage.intent", c.Intent, c.Priority)
	return out, nil
}

func (Triage) Settle(_ context.Context, _ reflow.Envelope[Ticket], out reflow.Envelope[Context], actErr error) (reflow.Envelope[Context], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// LookupAccount enriches context with account information.
type LookupAccount struct{}

func (LookupAccount) Resolve(_ context.Context, in reflow.Envelope[Context]) (reflow.Envelope[Context], error) {
	return in, nil
}

func (LookupAccount) Act(_ context.Context, in reflow.Envelope[Context]) (reflow.Envelope[Context], error) {
	out := in
	out.Value.Account = fmt.Sprintf("Enterprise plan, customer since 2021, account owner: %s", in.Value.Ticket.Customer)
	out = out.WithHint("enrich.account", "account data attached", in.Value.Ticket.Customer)
	return out, nil
}

func (LookupAccount) Settle(_ context.Context, _ reflow.Envelope[Context], out reflow.Envelope[Context], actErr error) (reflow.Envelope[Context], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// LookupHistory enriches context with past interactions.
type LookupHistory struct{}

func (LookupHistory) Resolve(_ context.Context, in reflow.Envelope[Context]) (reflow.Envelope[Context], error) {
	return in, nil
}

func (LookupHistory) Act(_ context.Context, in reflow.Envelope[Context]) (reflow.Envelope[Context], error) {
	out := in
	out.Value.History = "3 prior tickets in last 90 days, last contact 2 weeks ago about billing"
	out = out.WithHint("enrich.history", "interaction history attached", in.Value.Ticket.ID)
	return out, nil
}

func (LookupHistory) Settle(_ context.Context, _ reflow.Envelope[Context], out reflow.Envelope[Context], actErr error) (reflow.Envelope[Context], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// --- The LLM tool ---

// FakeLLM simulates an LLM that reads hints to generate a response.
// Replace with llm.AsTool(provider, "llm") for a real model.
type FakeLLM struct{}

func (FakeLLM) Name() string { return "llm.generate" }

func (FakeLLM) Call(_ context.Context, prompt string) (string, error) {
	// A real LLM would receive this prompt and generate a response.
	// The fake reads the intent line to produce something plausible.
	prompt = strings.ToLower(prompt)
	switch {
	case strings.Contains(prompt, "intent: technical"):
		return "I can see the outage you're reporting. Our team is actively investigating and I've linked your ticket to the incident. I'll update you as we learn more.", nil
	case strings.Contains(prompt, "intent: billing"):
		return "I've reviewed your account and can see the duplicate charge. I'm processing a refund now — you should see it within 3-5 business days.", nil
	case strings.Contains(prompt, "intent: account"):
		return "I've sent a password reset link to your registered email. If you don't receive it within 5 minutes, please check your spam folder.", nil
	default:
		return "Thank you for reaching out. I've reviewed your request and our team will follow up shortly.", nil
	}
}

// GenerateResponse uses the LLM tool to draft a response from enriched context.
type GenerateResponse struct {
	LLM reflow.Tool[string, string]
}

func (g GenerateResponse) Resolve(_ context.Context, in reflow.Envelope[Context]) (reflow.Envelope[Context], error) {
	return in, nil
}

func (g GenerateResponse) Act(ctx context.Context, in reflow.Envelope[Context]) (reflow.Envelope[Draft], error) {
	// Build a prompt from the enriched context.
	prompt := fmt.Sprintf(
		"Intent: %s | Priority: %s\nAccount: %s\nHistory: %s\nTicket: %s\n%s",
		in.Value.Intent, in.Value.Priority,
		in.Value.Account, in.Value.History,
		in.Value.Ticket.Subject, in.Value.Ticket.Body,
	)

	// Check for feedback hints from prior attempts.
	for _, h := range in.HintsByCode("review.feedback") {
		prompt += "\nFeedback from review: " + h.Message
	}

	response, step, err := reflow.Use(ctx, g.LLM, prompt)
	out := reflow.Map(in, Draft{Context: in.Value, Response: response}).WithStep(step)
	return out, err
}

func (g GenerateResponse) Settle(_ context.Context, _ reflow.Envelope[Context], out reflow.Envelope[Draft], actErr error) (reflow.Envelope[Draft], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}

	// Validate the generated response.
	response := out.Value.Response
	if len(response) < 20 {
		out = out.WithHint("review.feedback", "response is too short, elaborate on the resolution", "")
		return out, false, nil
	}
	if out.Value.Context.Priority == "high" && !strings.Contains(strings.ToLower(response), "team") {
		out = out.WithHint("review.feedback", "high-priority tickets must mention team involvement", "")
		return out, false, nil
	}

	out = out.WithHint("review.pass", "response met quality checks", "")
	return out, true, nil
}

func main() {
	ctx := context.Background()

	ticket := Ticket{
		ID:       "TKT-4401",
		Customer: "Northwind Health",
		Subject:  "Urgent: API outage affecting checkout",
		Body:     "Our checkout flow has been down for 30 minutes. All API calls return 502.",
	}

	// The graph: triage -> fork(account, history) -> generate with retry.
	// The LLM is one tool. The graph is the agent.
	enrich := reflow.ForkJoin(mergeContext, LookupAccount{}, LookupHistory{})
	generate := reflow.WithRetry(GenerateResponse{LLM: FakeLLM{}}, 3)

	agent := reflow.Compose[Ticket, Draft]("support",
		func(ctx context.Context, s *reflow.Steps, in reflow.Envelope[Ticket]) reflow.Envelope[Draft] {
			triaged := reflow.Do(s, ctx, Triage{}, in)
			enriched := reflow.Do(s, ctx, enrich, triaged)
			return reflow.Do(s, ctx, generate, enriched)
		},
	)

	out, err := reflow.Run(ctx, agent, reflow.NewEnvelope(ticket))
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	retries := 0
	tools := 0
	for _, step := range out.Meta.Trace {
		if step.Phase == "settle" && step.Status == "retry" {
			retries++
		}
		if step.Phase == "tool" {
			tools++
		}
	}

	fmt.Println("Support Graph")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Ticket:   %s\n", out.Value.Context.Ticket.ID)
	fmt.Printf("Customer: %s\n", out.Value.Context.Ticket.Customer)
	fmt.Printf("Intent:   %s\n", out.Value.Context.Intent)
	fmt.Printf("Priority: %s\n", out.Value.Context.Priority)
	fmt.Println()
	fmt.Printf("Response: %s\n", out.Value.Response)
	fmt.Println()
	fmt.Printf("Retries:    %d\n", retries)
	fmt.Printf("Tool calls: %d\n", tools)
	fmt.Printf("Hints:      %d\n", len(out.Meta.Hints))
	fmt.Printf("Trace:      %d steps\n", len(out.Meta.Trace))
	fmt.Println()
	fmt.Println("Hints:")
	for _, h := range out.Meta.Hints {
		fmt.Printf("  [%s] %s\n", h.Code, h.Message)
	}
}

func mergeContext(results []reflow.Envelope[Context]) reflow.Envelope[Context] {
	merged := results[0]
	for _, r := range results[1:] {
		if r.Value.Account != "" {
			merged.Value.Account = r.Value.Account
		}
		if r.Value.History != "" {
			merged.Value.History = r.Value.History
		}
		merged.Meta.Hints = append(merged.Meta.Hints, r.Meta.Hints...)
		merged.Meta.Trace = append(merged.Meta.Trace, r.Meta.Trace...)
	}
	return merged
}
