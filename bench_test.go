package reflow

import (
	"context"
	"fmt"
	"iter"
	"strconv"
	"sync"
	"testing"
)

// --- Shared helpers ---

var doubleNode = &Func[int, int]{ActFn: Pass(func(n int) int { return n * 2 })}

func makeNodes(n int) []Node[int, int] {
	nodes := make([]Node[int, int], n)
	for i := range nodes {
		nodes[i] = doubleNode
	}
	return nodes
}

func envWithTags(n int) Envelope[int] {
	env := NewEnvelope(42)
	for i := range n {
		env = env.WithTag(fmt.Sprintf("key%d", i), fmt.Sprintf("val%d", i))
	}
	return env
}

func envWithHints(n int) Envelope[int] {
	env := NewEnvelope(42)
	for i := range n {
		env = env.WithHint(fmt.Sprintf("code%d", i), "msg", "")
	}
	return env
}

func envWithSteps(n int) Envelope[int] {
	env := NewEnvelope(42)
	steps := make([]Step, n)
	for i := range steps {
		steps[i] = Step{Node: "bench", Phase: "tool", Status: "ok"}
	}
	return env.WithStep(steps...)
}

func intSource(n int) iter.Seq2[Envelope[int], error] {
	return func(yield func(Envelope[int], error) bool) {
		for i := range n {
			if !yield(NewEnvelope(i), nil) {
				return
			}
		}
	}
}

// countingNode settles after exactly target iterations.
type countingNode struct {
	target int
	count  int
}

func (c *countingNode) Resolve(_ context.Context, in Envelope[int]) (Envelope[int], error) {
	return in, nil
}

func (c *countingNode) Act(_ context.Context, in Envelope[int]) (Envelope[int], error) {
	return Envelope[int]{Value: in.Value * 2, Meta: in.Meta}, nil
}

func (c *countingNode) Settle(_ context.Context, _ Envelope[int], out Envelope[int], _ error) (Envelope[int], bool, error) {
	c.count++
	if c.count >= c.target {
		return out, true, nil
	}
	return out.WithHint("retry", "not yet", ""), false, nil
}

// --- Core Execution ---

func BenchmarkRun(b *testing.B) {
	ctx := context.Background()
	env := NewEnvelope(42)
	b.ReportAllocs()
	for b.Loop() {
		Run(ctx, doubleNode, env)
	}
}

func BenchmarkPipeline(b *testing.B) {
	for _, n := range []int{1, 3, 5, 10} {
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			ctx := context.Background()
			env := NewEnvelope(42)
			p := Pipeline("bench", makeNodes(n)...)
			b.ReportAllocs()
			for b.Loop() {
				Run(ctx, p, env)
			}
		})
	}
}

func BenchmarkChain(b *testing.B) {
	intToStr := &Func[int, string]{
		ActFn: Lift(func(n int) (string, error) { return strconv.Itoa(n), nil }),
	}
	strToInt := &Func[string, int]{
		ActFn: Lift(func(s string) (int, error) { return strconv.Atoi(s) }),
	}

	ctx := context.Background()
	env := NewEnvelope(42)
	chained := Chain(intToStr, strToInt)
	b.ReportAllocs()
	for b.Loop() {
		Run(ctx, chained, env)
	}
}

func BenchmarkForkJoin(b *testing.B) {
	merge := func(results []Envelope[int]) Envelope[int] { return results[0] }

	for _, n := range []int{2, 4, 8} {
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			ctx := context.Background()
			env := NewEnvelope(42)
			fj := ForkJoin(merge, makeNodes(n)...)
			b.ReportAllocs()
			for b.Loop() {
				Run(ctx, fj, env)
			}
		})
	}
}

func BenchmarkWithRetry(b *testing.B) {
	for _, attempts := range []int{1, 3, 5} {
		b.Run(fmt.Sprintf("attempts=%d", attempts), func(b *testing.B) {
			ctx := context.Background()
			env := NewEnvelope(42)
			b.ReportAllocs()
			for b.Loop() {
				n := &countingNode{target: attempts}
				Run(ctx, WithRetry(n, attempts), env)
			}
		})
	}
}

// --- Streaming ---

func BenchmarkPool(b *testing.B) {
	for _, conc := range []int{1, 4, 8} {
		b.Run(fmt.Sprintf("concurrency=%d", conc), func(b *testing.B) {
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				Collect(Pool(ctx, doubleNode, intSource(100), conc))
			}
		})
	}
}

func BenchmarkStreamCollect(b *testing.B) {
	for _, items := range []int{10, 100} {
		b.Run(fmt.Sprintf("items=%d", items), func(b *testing.B) {
			ctx := context.Background()
			env := NewEnvelope(0)
			sn := &StreamFunc[int, int]{
				ActFn: func(_ context.Context, in Envelope[int]) iter.Seq2[Envelope[int], error] {
					return func(yield func(Envelope[int], error) bool) {
						for i := range items {
							if !yield(Map(in, i), nil) {
								return
							}
						}
					}
				},
			}
			b.ReportAllocs()
			for b.Loop() {
				Collect(Stream(ctx, sn, env))
			}
		})
	}
}

// --- Metadata ---

func BenchmarkNewEnvelope(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		NewEnvelope(42)
	}
}

func BenchmarkWithTag(b *testing.B) {
	for _, existing := range []int{0, 5, 10} {
		b.Run(fmt.Sprintf("existing=%d", existing), func(b *testing.B) {
			env := envWithTags(existing)
			b.ReportAllocs()
			for b.Loop() {
				env.WithTag("new", "val")
			}
		})
	}
}

func BenchmarkWithHint(b *testing.B) {
	for _, existing := range []int{0, 5, 10} {
		b.Run(fmt.Sprintf("existing=%d", existing), func(b *testing.B) {
			env := envWithHints(existing)
			b.ReportAllocs()
			for b.Loop() {
				env.WithHint("new", "msg", "")
			}
		})
	}
}

func BenchmarkWithStep(b *testing.B) {
	for _, existing := range []int{0, 10, 50} {
		b.Run(fmt.Sprintf("existing=%d", existing), func(b *testing.B) {
			env := envWithSteps(existing)
			step := Step{Node: "bench", Phase: "tool", Status: "ok"}
			b.ReportAllocs()
			for b.Loop() {
				env.WithStep(step)
			}
		})
	}
}

// --- Raw Go Comparison ---

func BenchmarkRawFunc(b *testing.B) {
	fn := func(n int) int { return n * 2 }
	b.ReportAllocs()
	for b.Loop() {
		fn(42)
	}
}

func BenchmarkRawPipeline(b *testing.B) {
	fn := func(n int) int { return n * 2 }
	for _, n := range []int{1, 3, 5, 10} {
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				v := 42
				for range n {
					v = fn(v)
				}
			}
		})
	}
}

func BenchmarkRawForkJoin(b *testing.B) {
	for _, nodes := range []int{2, 4, 8} {
		b.Run(fmt.Sprintf("nodes=%d", nodes), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				results := make([]int, nodes)
				var wg sync.WaitGroup
				for i := range nodes {
					wg.Add(1)
					go func() {
						defer wg.Done()
						results[i] = 42 * 2
					}()
				}
				wg.Wait()
			}
		})
	}
}

func BenchmarkRawPool(b *testing.B) {
	for _, conc := range []int{1, 4, 8} {
		b.Run(fmt.Sprintf("concurrency=%d", conc), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sem := make(chan struct{}, conc)
				results := make([]int, 100)
				var wg sync.WaitGroup
				for i := range 100 {
					wg.Add(1)
					sem <- struct{}{}
					go func() {
						defer func() { <-sem; wg.Done() }()
						results[i] = i * 2
					}()
				}
				wg.Wait()
			}
		})
	}
}
