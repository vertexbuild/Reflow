// Example: Fan-Out Consensus
//
// Pipeline: seed case -> fork/join evidence -> deterministic consensus ->
// synthesize explanation only when evidence is mixed
//
// This shows concurrent evidence gathering with ForkJoin and keeps the
// explanation step downstream of structured, deterministic context.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/vertexbuild/reflow"
)

type Case struct {
	ID       string
	Customer string
	Summary  string
}

type Evidence struct {
	Source     string
	Verdict    string
	Confidence float64
	Detail     string
}

type CaseFile struct {
	Case        Case
	Evidence    []Evidence
	Verdict     string
	Confidence  float64
	Explanation string
}

func main() {
	ctx := context.Background()

	input := Case{
		ID:       "CASE-9921",
		Customer: "Northwind Health",
		Summary:  "Usage spiked 4x after plan downgrade and customers are disputing the latest invoice.",
	}

	seed := &reflow.Func[Case, CaseFile]{
		ActFn: reflow.Pass(func(c Case) CaseFile { return CaseFile{Case: c} }),
	}

	enrich := reflow.ForkJoin(mergeCaseFiles,
		ContractSignals{},
		UsageSignals{},
		HistorySignals{},
	)

	assess := reflow.Compose("assess",
		func(ctx context.Context, s *reflow.Steps, in reflow.Envelope[CaseFile]) reflow.Envelope[CaseFile] {
			evidence := reflow.Do(s, ctx, enrich, in)
			decided := reflow.Do(s, ctx, Consensus{}, evidence)
			if len(decided.HintsByCode("consensus.mixed")) > 0 {
				return reflow.Do(s, ctx, MixedExplainer{}, decided)
			}
			return reflow.Do(s, ctx, DeterministicExplainer{}, decided)
		},
	)

	out, err := reflow.Run(ctx, reflow.Chain(seed, assess), reflow.NewEnvelope(input))
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	mixed := len(out.HintsByCode("consensus.mixed")) > 0

	fmt.Println("Fan-Out Consensus")
	fmt.Println(strings.Repeat("=", 44))
	fmt.Printf("Case:       %s\n", out.Value.Case.ID)
	fmt.Printf("Customer:   %s\n", out.Value.Case.Customer)
	fmt.Printf("Verdict:    %s\n", strings.ToUpper(out.Value.Verdict))
	fmt.Printf("Confidence: %.0f%%\n", out.Value.Confidence*100)
	fmt.Printf("Mixed path: %t\n", mixed)
	fmt.Println()
	fmt.Println("Evidence:")
	for _, evidence := range out.Value.Evidence {
		fmt.Printf("  %-10s %-10s %.2f  %s\n",
			evidence.Source,
			evidence.Verdict,
			evidence.Confidence,
			evidence.Detail,
		)
	}
	fmt.Println()
	fmt.Printf("Explanation: %s\n", out.Value.Explanation)
	fmt.Printf("Hints:       %d\n", len(out.Meta.Hints))
	fmt.Printf("Trace:       %d steps\n", len(out.Meta.Trace))
}

type ContractSignals struct{}

func (ContractSignals) Resolve(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	return in, nil
}

func (ContractSignals) Act(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	return appendEvidence(in, Evidence{
		Source:     "contract",
		Verdict:    "clean",
		Confidence: 0.58,
		Detail:     "Downgrade terms allow lower usage only after next billing cycle.",
	}), nil
}

func (ContractSignals) Settle(_ context.Context, _ reflow.Envelope[CaseFile], out reflow.Envelope[CaseFile], actErr error) (reflow.Envelope[CaseFile], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

type UsageSignals struct{}

func (UsageSignals) Resolve(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	return in, nil
}

func (UsageSignals) Act(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	return appendEvidence(in, Evidence{
		Source:     "usage",
		Verdict:    "suspicious",
		Confidence: 0.83,
		Detail:     "Usage spiked after downgrade and exceeded the new plan cap.",
	}), nil
}

func (UsageSignals) Settle(_ context.Context, _ reflow.Envelope[CaseFile], out reflow.Envelope[CaseFile], actErr error) (reflow.Envelope[CaseFile], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

type HistorySignals struct{}

func (HistorySignals) Resolve(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	return in, nil
}

func (HistorySignals) Act(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	return appendEvidence(in, Evidence{
		Source:     "history",
		Verdict:    "malicious",
		Confidence: 0.71,
		Detail:     "A prior account takeover triggered the same dispute pattern on this customer segment.",
	}), nil
}

func (HistorySignals) Settle(_ context.Context, _ reflow.Envelope[CaseFile], out reflow.Envelope[CaseFile], actErr error) (reflow.Envelope[CaseFile], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

type Consensus struct{}

func (Consensus) Resolve(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	return in, nil
}

func (Consensus) Act(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	out := in
	weights := map[string]float64{
		"clean":      0,
		"suspicious": 0,
		"malicious":  0,
	}
	for _, evidence := range in.Value.Evidence {
		weights[evidence.Verdict] += evidence.Confidence
	}

	bestVerdict := "clean"
	bestWeight := weights["clean"]
	for _, verdict := range []string{"suspicious", "malicious"} {
		if weights[verdict] > bestWeight {
			bestVerdict = verdict
			bestWeight = weights[verdict]
		}
	}

	out.Value.Verdict = bestVerdict
	out.Value.Confidence = bestWeight / float64(len(in.Value.Evidence))

	if closeVote(weights) {
		out = out.WithHint("consensus.mixed", "sources disagree and require a synthesized explanation", "")
	}

	return out, nil
}

func (Consensus) Settle(_ context.Context, _ reflow.Envelope[CaseFile], out reflow.Envelope[CaseFile], actErr error) (reflow.Envelope[CaseFile], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

type DeterministicExplainer struct{}

func (DeterministicExplainer) Resolve(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	return in, nil
}

func (DeterministicExplainer) Act(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	out := in
	out.Value.Explanation = "Consensus was clear enough to explain deterministically from the gathered evidence."
	return out, nil
}

func (DeterministicExplainer) Settle(_ context.Context, _ reflow.Envelope[CaseFile], out reflow.Envelope[CaseFile], actErr error) (reflow.Envelope[CaseFile], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

type MixedExplainer struct{}

func (MixedExplainer) Resolve(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	return in, nil
}

func (MixedExplainer) Act(_ context.Context, in reflow.Envelope[CaseFile]) (reflow.Envelope[CaseFile], error) {
	out := in
	var bullets []string
	for _, evidence := range in.Value.Evidence {
		bullets = append(bullets, fmt.Sprintf("%s=%s(%.2f)", evidence.Source, evidence.Verdict, evidence.Confidence))
	}
	out.Value.Explanation = "Evidence was mixed, so the case file carried all branch outputs forward: " + strings.Join(bullets, "; ")
	return out, nil
}

func (MixedExplainer) Settle(_ context.Context, _ reflow.Envelope[CaseFile], out reflow.Envelope[CaseFile], actErr error) (reflow.Envelope[CaseFile], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

func appendEvidence(in reflow.Envelope[CaseFile], evidence Evidence) reflow.Envelope[CaseFile] {
	out := in
	out.Value.Evidence = append(append([]Evidence(nil), in.Value.Evidence...), evidence)
	out = out.WithHint("evidence."+evidence.Source, evidence.Detail, evidence.Verdict)
	return out
}

func mergeCaseFiles(results []reflow.Envelope[CaseFile]) reflow.Envelope[CaseFile] {
	merged := CaseFile{}
	meta := reflow.Meta{Tags: make(map[string]string)}

	for _, result := range results {
		if merged.Case.ID == "" {
			merged.Case = result.Value.Case
		}
		merged.Evidence = append(merged.Evidence, result.Value.Evidence...)
		meta.Hints = append(meta.Hints, result.Meta.Hints...)
		meta.Trace = append(meta.Trace, result.Meta.Trace...)
		for k, v := range result.Meta.Tags {
			meta.Tags[k] = v
		}
	}

	return reflow.Envelope[CaseFile]{Value: merged, Meta: meta}
}

func closeVote(weights map[string]float64) bool {
	top := 0.0
	second := 0.0
	for _, verdict := range []string{"clean", "suspicious", "malicious"} {
		weight := weights[verdict]
		if weight > top {
			second = top
			top = weight
			continue
		}
		if weight > second {
			second = weight
		}
	}
	return top-second < 0.35
}
