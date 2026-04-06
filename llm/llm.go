// Package llm provides a common interface for language model providers.
//
// LLM support is an extension to Reflow, not the core package.
// An LLM is just one kind of node — not privileged, not required.
//
// Providers implement [reflow.Tool] via [AsTool], giving you automatic
// tracing and retry when used with [reflow.Use] and [reflow.UseRetry].
package llm

import (
	"context"

	"github.com/vertexbuild/reflow"
)

// Provider is something that takes messages and returns a response.
type Provider interface {
	Complete(ctx context.Context, msgs Messages) (Response, error)
}

// Messages carries a conversation for the provider.
type Messages struct {
	System   string
	Messages []Message
}

// Message is a single turn in a conversation.
type Message struct {
	Role    string // "user", "assistant"
	Content string
}

// Response is what a provider produces.
type Response struct {
	Content string
	Meta    map[string]any // token counts, model info, stop reason, etc.
}

// Chat is a convenience function that sends a single system + user
// message and returns the response content.
func Chat(ctx context.Context, p Provider, system, user string) (string, error) {
	resp, err := p.Complete(ctx, Messages{
		System: system,
		Messages: []Message{
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// chatTool wraps a Provider as a reflow.Tool[Messages, Response].
type chatTool struct {
	provider Provider
	name     string
}

func (t *chatTool) Name() string { return t.name }
func (t *chatTool) Call(ctx context.Context, msgs Messages) (Response, error) {
	return t.provider.Complete(ctx, msgs)
}

// AsTool wraps a Provider as a [reflow.Tool] for use with [reflow.Use]
// and [reflow.UseRetry]. The name identifies the tool in trace output.
//
//	chat := llm.AsTool(ollama.New("llama3.2"), "ollama.chat")
//
//	func (c Classify) Act(ctx context.Context, in reflow.Envelope[string]) (reflow.Envelope[Result], error) {
//	    resp, step, err := reflow.Use(ctx, c.Chat, llm.Messages{...})
//	    out := reflow.Envelope[Result]{Meta: in.Meta}.WithStep(step)
//	    ...
//	}
func AsTool(p Provider, name string) reflow.Tool[Messages, Response] {
	return &chatTool{provider: p, name: name}
}
