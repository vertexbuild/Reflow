//go:build live

// Run with: go test -tags live -run TestLive -v -timeout 120s
// Requires: Ollama running locally with a model pulled (e.g. llama3.2).

package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ploffredo/reflow"
	"github.com/ploffredo/reflow/llm"
	"github.com/ploffredo/reflow/llm/ollama"
)

const liveModel = "llama3.2"

// --- Classification with settle loop ---

type Classification struct {
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
}

type ClassifyTicket struct {
	LLM llm.Provider
}

func (ClassifyTicket) Resolve(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
	return in, nil
}

func (c ClassifyTicket) Act(ctx context.Context, in reflow.Envelope[string]) (reflow.Envelope[Classification], error) {
	resp, err := llm.Chat(ctx, c.LLM,
		`Classify this support ticket. Respond with ONLY valid JSON, no markdown:
{"category": "<billing|technical|account|general>", "confidence": <0.0-1.0>}`,
		in.Value,
	)
	if err != nil {
		return reflow.Envelope[Classification]{Meta: in.Meta}, err
	}
	resp = extractJSON(resp)
	var cl Classification
	if err := json.Unmarshal([]byte(resp), &cl); err != nil {
		return reflow.Envelope[Classification]{Meta: in.Meta}, fmt.Errorf("parse response: %w (raw: %s)", err, resp)
	}
	return reflow.Envelope[Classification]{Value: cl, Meta: in.Meta}, nil
}

func (ClassifyTicket) Settle(_ context.Context, _ reflow.Envelope[string], out reflow.Envelope[Classification], actErr error) (reflow.Envelope[Classification], bool, error) {
	if actErr != nil {
		out = out.WithHint("format.invalid", actErr.Error(), "")
		return out, false, nil
	}
	valid := map[string]bool{"billing": true, "technical": true, "account": true, "general": true}
	if !valid[out.Value.Category] {
		out = out.WithHint("category.invalid",
			fmt.Sprintf("got %q, need: billing/technical/account/general", out.Value.Category), "")
		return out, false, nil
	}
	if out.Value.Confidence < 0.5 {
		out = out.WithHint("confidence.low",
			fmt.Sprintf("%.2f below threshold 0.5", out.Value.Confidence), "")
		return out, false, nil
	}
	return out, true, nil
}

func TestLiveClassification(t *testing.T) {
	provider := ollama.New(liveModel)
	ctx := context.Background()

	classifier := reflow.WithRetry(ClassifyTicket{LLM: provider}, 3)

	tickets := []struct {
		input    string
		expected string
	}{
		{"I was charged twice for my subscription last month", "billing"},
		{"The app crashes every time I try to upload a photo", "technical"},
		{"I can't log into my account", "account"},
		{"What are your business hours?", "general"},
	}

	for _, tt := range tickets {
		name := tt.input
		if len(name) > 40 {
			name = name[:40]
		}
		t.Run(name, func(t *testing.T) {
			out, err := reflow.Run(ctx, classifier, reflow.NewEnvelope(tt.input))
			if err != nil {
				t.Fatalf("failed: %v", err)
			}
			t.Logf("category=%s confidence=%.2f", out.Value.Category, out.Value.Confidence)
			if out.Value.Category != tt.expected {
				t.Logf("WARNING: expected %q, got %q", tt.expected, out.Value.Category)
			}
			if out.Meta.Trace.Len() == 0 {
				t.Error("expected trace entries")
			}
		})
	}
}

// --- LLM JSON repair ---

type JSON = map[string]any

type LLMRepairJSON struct {
	LLM llm.Provider
}

func (LLMRepairJSON) Resolve(_ context.Context, in reflow.Envelope[JSON]) (reflow.Envelope[JSON], error) {
	return in, nil
}

func (r LLMRepairJSON) Act(ctx context.Context, in reflow.Envelope[JSON]) (reflow.Envelope[JSON], error) {
	if in.Value != nil {
		return in, nil
	}
	original := in.Meta.Tags["original_input"]
	if original == "" {
		return in, fmt.Errorf("no original input")
	}
	var hintLines []string
	for _, h := range in.Meta.Hints.Slice() {
		hintLines = append(hintLines, fmt.Sprintf("- %s: %s", h.Code, h.Message))
	}
	resp, err := llm.Chat(ctx, r.LLM,
		"You are a JSON repair tool. Fix the malformed JSON. Return ONLY the fixed JSON, nothing else.",
		fmt.Sprintf("Fix this JSON:\n%s\n\nKnown issues:\n%s", original, strings.Join(hintLines, "\n")),
	)
	if err != nil {
		return in, fmt.Errorf("llm repair: %w", err)
	}
	resp = extractJSON(resp)
	var v JSON
	if err := json.Unmarshal([]byte(resp), &v); err != nil {
		return in, fmt.Errorf("repair produced invalid JSON: %w (raw: %s)", err, resp)
	}
	return reflow.Envelope[JSON]{Value: v, Meta: in.Meta}, nil
}

func (LLMRepairJSON) Settle(_ context.Context, _ reflow.Envelope[JSON], out reflow.Envelope[JSON], actErr error) (reflow.Envelope[JSON], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

func TestLiveJSONRepair(t *testing.T) {
	provider := ollama.New(liveModel)
	ctx := context.Background()

	parse := &reflow.Func[string, JSON]{
		ActFn: reflow.Lift(func(raw string) (JSON, error) {
			var v JSON
			return v, json.Unmarshal([]byte(raw), &v)
		}),
		SettleFn: func(_ context.Context, in reflow.Envelope[string], out reflow.Envelope[JSON], actErr error) (reflow.Envelope[JSON], bool, error) {
			if actErr == nil {
				return out, true, nil
			}
			out = out.WithHint("json.malformed", actErr.Error(), "")
			out = out.WithTag("original_input", in.Value)
			return out, true, nil
		},
	}
	repair := LLMRepairJSON{LLM: provider}
	pipeline := reflow.Chain[string, JSON, JSON](parse, repair)

	cases := []string{
		`{"name": "Jane Doe", "amount": 150.00, }`,
		`{"items": [1, 2, 3,]}`,
		`{'name': 'Bob', "age": 30}`,
	}

	for _, input := range cases {
		t.Run(input[:min(30, len(input))], func(t *testing.T) {
			out, err := reflow.Run(ctx, pipeline, reflow.NewEnvelope(input))
			if err != nil {
				t.Fatalf("pipeline error: %v", err)
			}
			t.Logf("input:  %s", input)
			t.Logf("output: %v", out.Value)
			t.Logf("hints:  %d", out.Meta.Hints.Len())
			for _, h := range out.Meta.Hints.Slice() {
				t.Logf("  [%s] %s", h.Code, h.Message)
			}
			if out.Value == nil {
				t.Error("expected non-nil repaired JSON")
			}
		})
	}
}

// --- Chain with settle loops ---

type ClassifyWord struct {
	LLM llm.Provider
}

func (ClassifyWord) Resolve(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
	return in, nil
}

func (c ClassifyWord) Act(ctx context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
	resp, err := llm.Chat(ctx, c.LLM,
		"Classify this support ticket into exactly one word: billing, technical, account, or general. Respond with ONLY that one word.",
		in.Value,
	)
	if err != nil {
		return reflow.Envelope[string]{Meta: in.Meta}, err
	}
	return reflow.Envelope[string]{Value: strings.TrimSpace(strings.ToLower(resp)), Meta: in.Meta}, nil
}

func (ClassifyWord) Settle(_ context.Context, _ reflow.Envelope[string], out reflow.Envelope[string], actErr error) (reflow.Envelope[string], bool, error) {
	if actErr != nil {
		return out, false, nil
	}
	valid := map[string]bool{"billing": true, "technical": true, "account": true, "general": true}
	if valid[out.Value] {
		return out, true, nil
	}
	out = out.WithHint("category.invalid", fmt.Sprintf("got %q", out.Value), "")
	return out, false, nil
}

type GenerateResponse struct {
	LLM llm.Provider
}

func (GenerateResponse) Resolve(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
	return in, nil
}

func (g GenerateResponse) Act(ctx context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
	resp, err := llm.Chat(ctx, g.LLM,
		fmt.Sprintf("You handle %s support tickets. Write a brief, empathetic one-sentence acknowledgment.", in.Value),
		"Acknowledge the ticket.",
	)
	if err != nil {
		return reflow.Envelope[string]{Meta: in.Meta}, err
	}
	return reflow.Envelope[string]{Value: resp, Meta: in.Meta}, nil
}

func (GenerateResponse) Settle(_ context.Context, _ reflow.Envelope[string], out reflow.Envelope[string], actErr error) (reflow.Envelope[string], bool, error) {
	if actErr != nil {
		return out, false, nil
	}
	if len(out.Value) < 10 {
		out = out.WithHint("response.short", "response too short", "")
		return out, false, nil
	}
	return out, true, nil
}

func TestLiveChainWithSettleLoop(t *testing.T) {
	provider := ollama.New(liveModel)
	ctx := context.Background()

	classify := reflow.WithRetry(ClassifyWord{LLM: provider}, 3)
	respond := reflow.WithRetry(GenerateResponse{LLM: provider}, 2)
	pipeline := reflow.Chain[string, string, string](classify, respond)

	tickets := []string{
		"I was charged twice",
		"App won't load",
		"Reset my password please",
	}

	for _, ticket := range tickets {
		t.Run(ticket, func(t *testing.T) {
			out, err := reflow.Run(ctx, pipeline, reflow.NewEnvelope(ticket))
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			t.Logf("ticket:   %s", ticket)
			t.Logf("response: %s", out.Value)
			t.Logf("trace:    %d steps", out.Meta.Trace.Len())
		})
	}
}

func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		var inner []string
		inFence := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inFence = !inFence
				continue
			}
			if inFence {
				inner = append(inner, line)
			}
		}
		if len(inner) > 0 {
			s = strings.Join(inner, "\n")
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}
	return strings.TrimSpace(s)
}
