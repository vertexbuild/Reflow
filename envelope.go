package reflow

import "time"

// Envelope carries a value and structured context through a workflow graph.
// Each node receives an envelope, does its work, and settles the result
// into a better handoff for the next node.
type Envelope[T any] struct {
	Value T
	Meta  Meta
}

// Meta holds structured context that accumulates as an envelope
// moves through the graph.
type Meta struct {
	Hints []Hint
	Trace []Step
	Tags  map[string]string
}

// Hint is guidance left by one node for downstream nodes.
// A parser might leave a hint like {Code: "json.malformed", Message: "likely trailing comma", Span: "byte 248"}.
type Hint struct {
	Code    string
	Message string
	Span    string
}

// Step records what happened at a node during execution.
// The executor records resolve/settle steps automatically.
// Use Tool or Try to record tool calls inside Act.
type Step struct {
	Node     string
	Phase    string
	Status   string
	Duration time.Duration
}

// NewEnvelope creates an envelope with the given value and empty metadata.
func NewEnvelope[T any](v T) Envelope[T] {
	return Envelope[T]{
		Value: v,
		Meta: Meta{
			Tags: make(map[string]string),
		},
	}
}

// WithHint returns a copy of the envelope with an additional hint.
func (e Envelope[T]) WithHint(code, message, span string) Envelope[T] {
	e.Meta.Hints = append(cloneHints(e.Meta.Hints), Hint{
		Code:    code,
		Message: message,
		Span:    span,
	})
	return e
}

// WithTag returns a copy of the envelope with an additional tag.
func (e Envelope[T]) WithTag(key, value string) Envelope[T] {
	tags := make(map[string]string, len(e.Meta.Tags)+1)
	for k, v := range e.Meta.Tags {
		tags[k] = v
	}
	tags[key] = value
	e.Meta.Tags = tags
	return e
}

// HintsByCode returns all hints matching the given code.
func (e Envelope[T]) HintsByCode(code string) []Hint {
	var out []Hint
	for _, h := range e.Meta.Hints {
		if h.Code == code {
			out = append(out, h)
		}
	}
	return out
}

// WithStep returns a copy of the envelope with additional trace steps.
// Use this to record tool calls or custom events inside Act.
//
//	result, step, err := reflow.Tool(ctx, "ollama.chat", func(ctx context.Context) (string, error) {
//	    return llm.Chat(ctx, provider, system, user)
//	})
//	out = out.WithStep(step)
func (e Envelope[T]) WithStep(steps ...Step) Envelope[T] {
	e.Meta.Trace = append(cloneTrace(e.Meta.Trace), steps...)
	return e
}

// CarryMeta returns a new envelope with the given value and this envelope's metadata.
func (e Envelope[T]) CarryMeta(v T) Envelope[T] {
	return Envelope[T]{Value: v, Meta: e.Meta}
}

func cloneHints(hints []Hint) []Hint {
	if hints == nil {
		return nil
	}
	out := make([]Hint, len(hints))
	copy(out, hints)
	return out
}

func cloneTrace(trace []Step) []Step {
	if trace == nil {
		return nil
	}
	out := make([]Step, len(trace))
	copy(out, trace)
	return out
}
