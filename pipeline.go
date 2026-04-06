package reflow

import "context"

// Pipeline chains nodes of the same type into a linear sequence.
// Each node transforms the envelope and passes it to the next.
//
// Use Pipeline for skills — fixed sequences of steps that enrich
// the same shared type:
//
//	triage := reflow.Pipeline[Ticket]("triage", normalize, classify, enrich, score)
//
// Use Compose when you need branching or type changes between steps.
//
// Pipeline returns a Node[T, T] that composes with everything:
// Chain, ForkJoin, WithRetry, Compose, Pool.
func Pipeline[T any](name string, nodes ...Node[T, T]) Node[T, T] {
	return &pipeline[T]{name: name, nodes: nodes}
}

type pipeline[T any] struct {
	name  string
	nodes []Node[T, T]
}

func (p *pipeline[T]) Resolve(_ context.Context, in Envelope[T]) (Envelope[T], error) {
	return in, nil
}

func (p *pipeline[T]) Act(ctx context.Context, in Envelope[T]) (Envelope[T], error) {
	env := in
	for _, n := range p.nodes {
		out, err := Run(ctx, n, env)
		if err != nil {
			return env, err
		}
		env = out
	}
	return env, nil
}

func (p *pipeline[T]) Settle(_ context.Context, _ Envelope[T], out Envelope[T], actErr error) (Envelope[T], bool, error) {
	if actErr != nil {
		out = out.WithHint("pipeline.error", actErr.Error(), "")
		return out, false, nil
	}
	return out, true, nil
}
