// Package ollama provides an LLM provider backed by an Ollama instance.
//
// By default it connects to http://localhost:11434. Use [WithBaseURL]
// for cloud-hosted or remote Ollama deployments.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ploffredo/reflow/llm"
)

// Provider calls an Ollama instance for chat completion.
type Provider struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithBaseURL sets the Ollama API base URL. Use this for cloud-hosted
// or remote deployments instead of the default localhost.
//
//	p := ollama.New("llama3.2", ollama.WithBaseURL("https://ollama.example.com"))
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.BaseURL = url }
}

// WithClient sets a custom HTTP client. Use this for timeouts, proxies,
// or authentication middleware.
func WithClient(c *http.Client) Option {
	return func(p *Provider) { p.Client = c }
}

// New creates an Ollama provider. Model should be a pulled model
// like "llama3.2" or "mistral". Defaults to http://localhost:11434.
//
//	// Local:
//	p := ollama.New("llama3.2")
//
//	// Remote:
//	p := ollama.New("llama3.2", ollama.WithBaseURL("https://ollama.example.com"))
func New(model string, opts ...Option) *Provider {
	p := &Provider{
		BaseURL: "http://localhost:11434",
		Model:   model,
		Client:  http.DefaultClient,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
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
