// Package anthropic provides an LLM provider backed by the Anthropic Messages API.
package anthropic

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/vertexbuild/reflow/llm"
)

// Provider calls the Anthropic Messages API. It reads ANTHROPIC_API_KEY
// from the environment by default.
type Provider struct {
	Client   anthropic.Client
	Model    anthropic.Model
	MaxTok   int64
}

// New creates a provider using the ANTHROPIC_API_KEY env var.
// Model defaults to Claude Sonnet 4.6.
func New(opts ...option.RequestOption) *Provider {
	return &Provider{
		Client: anthropic.NewClient(opts...),
		Model:  anthropic.ModelClaudeSonnet4_6,
		MaxTok: 4096,
	}
}

// WithModel returns a copy of the provider with a different model.
func (p *Provider) WithModel(model anthropic.Model) *Provider {
	cp := *p
	cp.Model = model
	return &cp
}

func (p *Provider) Complete(ctx context.Context, msgs llm.Messages) (llm.Response, error) {
	var apiMsgs []anthropic.MessageParam
	for _, m := range msgs.Messages {
		switch m.Role {
		case "assistant":
			apiMsgs = append(apiMsgs, anthropic.NewAssistantMessage(
				anthropic.NewTextBlock(m.Content),
			))
		default:
			apiMsgs = append(apiMsgs, anthropic.NewUserMessage(
				anthropic.NewTextBlock(m.Content),
			))
		}
	}

	maxTok := p.MaxTok
	if maxTok <= 0 {
		maxTok = 4096
	}

	params := anthropic.MessageNewParams{
		Model:     p.Model,
		MaxTokens: maxTok,
		Messages:  apiMsgs,
	}

	if msgs.System != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: msgs.System},
		}
	}

	resp, err := p.Client.Messages.New(ctx, params)
	if err != nil {
		return llm.Response{}, fmt.Errorf("anthropic: %w", err)
	}

	var parts []string
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			parts = append(parts, v.Text)
		}
	}

	return llm.Response{
		Content: strings.Join(parts, ""),
		Meta: map[string]any{
			"model":         string(resp.Model),
			"input_tokens":  resp.Usage.InputTokens,
			"output_tokens": resp.Usage.OutputTokens,
			"stop_reason":   string(resp.StopReason),
		},
	}, nil
}
