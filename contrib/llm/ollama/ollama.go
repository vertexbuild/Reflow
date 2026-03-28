// Package ollama provides an LLM provider backed by a local Ollama instance.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/vertexbuild/reflow/llm"
)

// Provider calls a local Ollama instance for chat completion.
type Provider struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

// New creates an Ollama provider. Model should be a pulled model
// like "llama3.2" or "mistral".
func New(model string) *Provider {
	return &Provider{
		BaseURL: "http://localhost:11434",
		Model:   model,
		Client:  http.DefaultClient,
	}
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Message message `json:"message"`
}

func (p *Provider) Complete(ctx context.Context, msgs llm.Messages) (llm.Response, error) {
	baseURL := p.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	var apiMsgs []message
	if msgs.System != "" {
		apiMsgs = append(apiMsgs, message{Role: "system", Content: msgs.System})
	}
	for _, m := range msgs.Messages {
		apiMsgs = append(apiMsgs, message{Role: m.Role, Content: m.Content})
	}

	body, err := json.Marshal(chatRequest{
		Model:    p.Model,
		Messages: apiMsgs,
		Stream:   false,
	})
	if err != nil {
		return llm.Response{}, fmt.Errorf("ollama: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, fmt.Errorf("ollama: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return llm.Response{}, fmt.Errorf("ollama: call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return llm.Response{}, fmt.Errorf("ollama: %s: %s", resp.Status, string(respBody))
	}

	var result chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return llm.Response{}, fmt.Errorf("ollama: decode: %w", err)
	}

	return llm.Response{
		Content: result.Message.Content,
		Meta: map[string]any{
			"model": p.Model,
		},
	}, nil
}
