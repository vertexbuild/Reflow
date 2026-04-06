// Example: Support Ticket Triage
//
// Compose: classify → extract → route (urgent/standard) → respond
//
// A triage graph that takes raw support tickets, classifies their
// priority and category, extracts key entities, routes based on
// urgency, and generates a response — all using the Compose/Do pattern.
//
// Demonstrates:
//   - Graph composition with reflow.Compose and reflow.Do
//   - Branching with plain Go (urgent vs standard path)
//   - Mix of deterministic and LLM-backed nodes
//   - WithRetry on LLM nodes for format enforcement
//   - Settle with structured hints flowing between steps
//   - Processing a batch of tickets to show the pattern at scale
//
// Run with: go run ./examples/triage_agent/
// Requires: Ollama running with llama3.2 pulled.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/vertexbuild/reflow"
	"github.com/vertexbuild/reflow/llm"
	"github.com/vertexbuild/reflow/llm/ollama"
)

// --- Types ---

type Ticket struct {
	ID        string
	From      string
	Subject   string
	Body      string
	Timestamp time.Time
}

type Classification struct {
	Ticket   Ticket
	Priority string // "urgent", "high", "standard"
	Category string // "billing", "technical", "account", "outage"
}

type Extracted struct {
	Classification
	Entities map[string]string // "account_id", "error_code", "product", etc.
	Sentiment string           // "frustrated", "neutral", "polite"
}

type TriageResult struct {
	Extracted
	Route    string // "escalate", "auto-respond", "queue"
	Response string
}

// --- Nodes ---

// ClassifyTicket uses an LLM to determine priority and category.
type ClassifyTicket struct {
	Chat reflow.Tool[llm.Messages, llm.Response]
}

func (c ClassifyTicket) Resolve(_ context.Context, in reflow.Envelope[Ticket]) (reflow.Envelope[Ticket], error) {
	return in, nil
}

func (c ClassifyTicket) Act(ctx context.Context, in reflow.Envelope[Ticket]) (reflow.Envelope[Classification], error) {
	prompt := fmt.Sprintf(`Classify this support ticket. Return ONLY valid JSON with these exact fields:
{"priority": "urgent|high|standard", "category": "billing|technical|account|outage"}

Subject: %s
Body: %s`, in.Value.Subject, in.Value.Body)

	resp, step, err := reflow.Use(ctx, c.Chat, llm.Messages{
		System:   "You are a support ticket classifier. Return only valid JSON, no explanation.",
		Messages: []llm.Message{{Role: "user", Content: prompt}},
	})
	out := reflow.Envelope[Classification]{
		Value: Classification{Ticket: in.Value},
		Meta:  in.Meta,
	}.WithStep(step)

	if err != nil {
		return out, err
	}

	// Parse the JSON response.
	content := extractJSON(resp.Content)
	var result struct {
		Priority string `json:"priority"`
		Category string `json:"category"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return out, fmt.Errorf("classify: bad json: %w: got %q", err, content)
	}

	out.Value.Priority = result.Priority
	out.Value.Category = result.Category
	return out, nil
}

func (c ClassifyTicket) Settle(_ context.Context, _ reflow.Envelope[Ticket], out reflow.Envelope[Classification], actErr error) (reflow.Envelope[Classification], bool, error) {
	if actErr != nil {
		out = out.WithHint("classify.retry", actErr.Error(), "")
		return out, false, nil
	}
	// Validate
	switch out.Value.Priority {
	case "urgent", "high", "standard":
	default:
		out = out.WithHint("classify.retry", fmt.Sprintf("invalid priority: %q", out.Value.Priority), "")
		return out, false, nil
	}
	switch out.Value.Category {
	case "billing", "technical", "account", "outage":
	default:
		out = out.WithHint("classify.retry", fmt.Sprintf("invalid category: %q", out.Value.Category), "")
		return out, false, nil
	}
	return out, true, nil
}

// ExtractEntities is a deterministic node that pulls structured data from the ticket.
type ExtractEntities struct{}

func (ExtractEntities) Resolve(_ context.Context, in reflow.Envelope[Classification]) (reflow.Envelope[Classification], error) {
	return in, nil
}

func (ExtractEntities) Act(_ context.Context, in reflow.Envelope[Classification]) (reflow.Envelope[Extracted], error) {
	entities := make(map[string]string)
	body := strings.ToLower(in.Value.Ticket.Body)

	// Simple deterministic extraction.
	if idx := strings.Index(body, "account"); idx != -1 {
		// Look for patterns like "account #12345" or "account 12345"
		after := body[idx:]
		for _, prefix := range []string{"account #", "account number ", "account "} {
			if strings.HasPrefix(after, prefix) {
				rest := after[len(prefix):]
				end := strings.IndexAny(rest, " .,\n")
				if end == -1 {
					end = len(rest)
				}
				if end > 0 {
					entities["account_id"] = strings.TrimSpace(rest[:end])
				}
				break
			}
		}
	}

	for _, code := range []string{"err-", "error ", "code "} {
		if idx := strings.Index(body, code); idx != -1 {
			rest := body[idx+len(code):]
			end := strings.IndexAny(rest, " .,\n")
			if end == -1 {
				end = len(rest)
			}
			if end > 0 {
				entities["error_code"] = strings.TrimSpace(rest[:end])
			}
			break
		}
	}

	// Sentiment heuristic.
	sentiment := "neutral"
	frustrated := []string{"frustrated", "angry", "unacceptable", "terrible", "urgent", "immediately", "broken", "down", "outage"}
	polite := []string{"please", "thank you", "appreciate", "kindly", "would you mind"}
	frustratedCount, politeCount := 0, 0
	for _, w := range frustrated {
		if strings.Contains(body, w) {
			frustratedCount++
		}
	}
	for _, w := range polite {
		if strings.Contains(body, w) {
			politeCount++
		}
	}
	if frustratedCount > politeCount {
		sentiment = "frustrated"
	} else if politeCount > frustratedCount {
		sentiment = "polite"
	}

	out := reflow.Envelope[Extracted]{
		Value: Extracted{
			Classification: in.Value,
			Entities:        entities,
			Sentiment:       sentiment,
		},
		Meta: in.Meta,
	}

	// Settle hints about what we found.
	out = out.WithHint("extract.entities", fmt.Sprintf("found %d entities", len(entities)), "")
	out = out.WithHint("extract.sentiment", sentiment, "")

	return out, nil
}

func (ExtractEntities) Settle(_ context.Context, _ reflow.Envelope[Classification], out reflow.Envelope[Extracted], actErr error) (reflow.Envelope[Extracted], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// RespondUrgent generates an escalation response for urgent tickets.
type RespondUrgent struct {
	Chat reflow.Tool[llm.Messages, llm.Response]
}

func (r RespondUrgent) Resolve(_ context.Context, in reflow.Envelope[Extracted]) (reflow.Envelope[Extracted], error) {
	return in, nil
}

func (r RespondUrgent) Act(ctx context.Context, in reflow.Envelope[Extracted]) (reflow.Envelope[TriageResult], error) {
	// Build context from the envelope — the LLM gets a prepared handoff.
	var context strings.Builder
	context.WriteString(fmt.Sprintf("Priority: %s\n", in.Value.Priority))
	context.WriteString(fmt.Sprintf("Category: %s\n", in.Value.Category))
	context.WriteString(fmt.Sprintf("Sentiment: %s\n", in.Value.Sentiment))
	if len(in.Value.Entities) > 0 {
		context.WriteString("Entities:\n")
		for k, v := range in.Value.Entities {
			context.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}
	// Include hints from upstream.
	for _, h := range in.Meta.Hints {
		context.WriteString(fmt.Sprintf("Hint [%s]: %s\n", h.Code, h.Message))
	}

	prompt := fmt.Sprintf(`This is an URGENT support ticket that needs immediate escalation.
Write a brief, empathetic acknowledgment (2-3 sentences) that:
1. Acknowledges the urgency
2. Confirms we're escalating to a specialist
3. References specifics from the ticket

Ticket context:
%s
Subject: %s
Body: %s`, context.String(), in.Value.Ticket.Subject, in.Value.Ticket.Body)

	resp, step, err := reflow.Use(ctx, r.Chat, llm.Messages{
		System:   "You are a senior support agent handling urgent escalations. Be concise and professional.",
		Messages: []llm.Message{{Role: "user", Content: prompt}},
	})

	result := TriageResult{
		Extracted: in.Value,
		Route:     "escalate",
		Response:  resp.Content,
	}
	out := reflow.Envelope[TriageResult]{Value: result, Meta: in.Meta}.WithStep(step)
	return out, err
}

func (r RespondUrgent) Settle(_ context.Context, _ reflow.Envelope[Extracted], out reflow.Envelope[TriageResult], actErr error) (reflow.Envelope[TriageResult], bool, error) {
	if actErr != nil {
		out = out.WithHint("respond.error", actErr.Error(), "")
		return out, false, nil
	}
	if len(out.Value.Response) < 20 {
		out = out.WithHint("respond.retry", "response too short", "")
		return out, false, nil
	}
	return out, true, nil
}

// RespondStandard generates a helpful response for standard tickets.
type RespondStandard struct {
	Chat reflow.Tool[llm.Messages, llm.Response]
}

func (r RespondStandard) Resolve(_ context.Context, in reflow.Envelope[Extracted]) (reflow.Envelope[Extracted], error) {
	return in, nil
}

func (r RespondStandard) Act(ctx context.Context, in reflow.Envelope[Extracted]) (reflow.Envelope[TriageResult], error) {
	var context strings.Builder
	context.WriteString(fmt.Sprintf("Category: %s\n", in.Value.Category))
	context.WriteString(fmt.Sprintf("Sentiment: %s\n", in.Value.Sentiment))
	if len(in.Value.Entities) > 0 {
		context.WriteString("Entities:\n")
		for k, v := range in.Value.Entities {
			context.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}

	prompt := fmt.Sprintf(`Write a helpful support response (2-3 sentences) for this ticket.
Be friendly and provide a next step.

Context:
%s
Subject: %s
Body: %s`, context.String(), in.Value.Ticket.Subject, in.Value.Ticket.Body)

	resp, step, err := reflow.Use(ctx, r.Chat, llm.Messages{
		System:   "You are a helpful support agent. Be concise, friendly, and actionable.",
		Messages: []llm.Message{{Role: "user", Content: prompt}},
	})

	result := TriageResult{
		Extracted: in.Value,
		Route:     "auto-respond",
		Response:  resp.Content,
	}
	out := reflow.Envelope[TriageResult]{Value: result, Meta: in.Meta}.WithStep(step)
	return out, err
}

func (r RespondStandard) Settle(_ context.Context, _ reflow.Envelope[Extracted], out reflow.Envelope[TriageResult], actErr error) (reflow.Envelope[TriageResult], bool, error) {
	if actErr != nil {
		out = out.WithHint("respond.error", actErr.Error(), "")
		return out, false, nil
	}
	if len(out.Value.Response) < 20 {
		out = out.WithHint("respond.retry", "response too short", "")
		return out, false, nil
	}
	return out, true, nil
}

// --- Graph ---

func buildTriageAgent(chat reflow.Tool[llm.Messages, llm.Response]) reflow.Node[Ticket, TriageResult] {
	classify := reflow.WithRetry(ClassifyTicket{Chat: chat}, 3)
	extract := ExtractEntities{}
	respondUrgent := reflow.WithRetry(RespondUrgent{Chat: chat}, 2)
	respondStandard := reflow.WithRetry(RespondStandard{Chat: chat}, 2)

	return reflow.Compose[Ticket, TriageResult]("triage",
		func(ctx context.Context, s *reflow.Steps, in reflow.Envelope[Ticket]) reflow.Envelope[TriageResult] {
			classified := reflow.Do(s, ctx, classify, in)
			extracted := reflow.Do(s, ctx, extract, classified)

			// Route based on classification — plain Go, no routing DSL.
			switch extracted.Value.Priority {
			case "urgent", "high":
				return reflow.Do(s, ctx, respondUrgent, extracted)
			default:
				return reflow.Do(s, ctx, respondStandard, extracted)
			}
		},
	)
}

// --- Fake data ---

var tickets = []Ticket{
	{
		ID:   "TKT-001",
		From: "maria.santos@example.com",
		Subject: "URGENT: Production database is down",
		Body: `Our production database has been completely unresponsive for the last 20 minutes.
All customer-facing services are down. Error code ERR-5012 on all connections.
Account #94201. This is a P0 outage. We need immediate help.`,
		Timestamp: time.Now().Add(-20 * time.Minute),
	},
	{
		ID:   "TKT-002",
		From: "james.chen@example.com",
		Subject: "Billing discrepancy on last invoice",
		Body: `Hi, I noticed my last invoice shows a charge for the Enterprise plan but I'm
on the Team plan. Account #78432. Could you please look into this and issue a
correction? Thank you for your help.`,
		Timestamp: time.Now().Add(-2 * time.Hour),
	},
	{
		ID:   "TKT-003",
		From: "sarah.powell@example.com",
		Subject: "Can't reset my password",
		Body: `I've been trying to reset my password for the last hour. The reset email never
arrives. I've checked spam. This is really frustrating because I can't access
any of my projects. Account #55109.`,
		Timestamp: time.Now().Add(-1 * time.Hour),
	},
	{
		ID:   "TKT-004",
		From: "alex.rivera@example.com",
		Subject: "API rate limiting is too aggressive",
		Body: `We're hitting rate limits on the /v2/search endpoint even though we're well
within our plan's documented limits. Getting error ERR-4029 about 30% of
requests. This started yesterday. Account #61887. Would appreciate a review
of the rate limiting configuration for our account.`,
		Timestamp: time.Now().Add(-4 * time.Hour),
	},
	{
		ID:   "TKT-005",
		From: "ops-team@bigcorp.example.com",
		Subject: "CRITICAL: Data sync failures across all regions",
		Body: `Since 14:00 UTC we're seeing data sync failures across US-East, EU-West, and
AP-South regions simultaneously. Error ERR-9001 in all sync jobs. This is
affecting our compliance reporting pipeline. We need this resolved immediately
as we have a regulatory deadline tomorrow. Account #10042.`,
		Timestamp: time.Now().Add(-45 * time.Minute),
	},
}

// --- Main ---

func main() {
	provider := ollama.New("llama3.2")
	chat := llm.AsTool(provider, "ollama.chat")
	agent := buildTriageAgent(chat)

	ctx := context.Background()

	fmt.Println("Support Ticket Triage")
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println()

	for _, ticket := range tickets {
		fmt.Printf("  %s  %s\n", ticket.ID, ticket.Subject)
	}
	fmt.Println()
	fmt.Println(strings.Repeat("-", 60))

	for _, ticket := range tickets {
		fmt.Printf("\n[%s] %s\n", ticket.ID, ticket.Subject)
		fmt.Printf("  From: %s\n", ticket.From)

		start := time.Now()
		out, err := reflow.Run(ctx, agent, reflow.NewEnvelope(ticket))
		elapsed := time.Since(start)

		if err != nil {
			log.Printf("  ERROR: %v\n", err)
			continue
		}

		r := out.Value

		// Priority + Category
		priorityIcon := "○"
		switch r.Priority {
		case "urgent":
			priorityIcon = "●"
		case "high":
			priorityIcon = "◐"
		}
		fmt.Printf("  %s  %-8s  %-10s  sentiment=%s\n", priorityIcon, r.Priority, r.Category, r.Sentiment)

		// Entities
		if len(r.Entities) > 0 {
			parts := make([]string, 0, len(r.Entities))
			for k, v := range r.Entities {
				parts = append(parts, fmt.Sprintf("%s=%s", k, v))
			}
			fmt.Printf("  Entities: %s\n", strings.Join(parts, ", "))
		}

		// Route + Response
		fmt.Printf("  Route: %s\n", r.Route)
		fmt.Printf("  Response:\n")
		for _, line := range wrapText(r.Response, 70) {
			fmt.Printf("    %s\n", line)
		}

		// Trace summary
		hints := len(out.Meta.Hints)
		steps := len(out.Meta.Trace)
		fmt.Printf("  [%s  hints=%d  trace=%d]\n", elapsed.Round(time.Millisecond), hints, steps)
	}

	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Processed %d tickets\n", len(tickets))
}

// --- Helpers ---

func extractJSON(s string) string {
	// Find the first { and last } to extract JSON from LLM output.
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func wrapText(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		words := strings.Fields(para)
		var current string
		for _, w := range words {
			if current == "" {
				current = w
			} else if len(current)+1+len(w) > width {
				lines = append(lines, current)
				current = w
			} else {
				current += " " + w
			}
		}
		if current != "" {
			lines = append(lines, current)
		}
	}
	return lines
}
