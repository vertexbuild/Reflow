// Example: Threat Intelligence Aggregator
//
// Pipeline: fan-out enrich → merge streams → synthesize
//
// A pipeline that takes a suspicious IP address, concurrently queries
// three different intelligence sources, merges the results with
// confidence-weighted hints, and uses an LLM to determine a final
// threat consensus.
//
// This demonstrates:
//   - ForkJoin for concurrent fan-out to multiple sources
//   - Each source settling its own confidence and signals as hints
//   - Merge function combining evidence from independent branches
//   - LLM synthesis using the accumulated structured context
//   - WithRetry on the synthesis node for format enforcement
//
// Run with: go run ./examples/threat_intel/
// Requires: Ollama running with llama3.2 pulled.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/vertexbuild/reflow"
	"github.com/vertexbuild/reflow/llm"
	"github.com/vertexbuild/reflow/llm/ollama"
)

// --- Types ---

type Query struct {
	IP        string
	Timestamp time.Time
}

type Intel struct {
	IP      string
	Sources []SourceReport
}

type SourceReport struct {
	Source     string
	Verdict   string // "malicious", "suspicious", "clean", "unknown"
	Confidence float64
	Details   string
	Tags      []string
}

type Assessment struct {
	IP         string
	Verdict    string
	Confidence float64
	Summary    string
	Sources    []SourceReport
	Actions    []string
}

// --- Intelligence Source Nodes ---
// Each source independently evaluates the IP and settles its findings
// as hints. In a real system these would be API calls to threat feeds.

// AbuseDB simulates querying an abuse/blocklist database.
type AbuseDB struct{}

func (AbuseDB) Resolve(_ context.Context, in reflow.Envelope[Query]) (reflow.Envelope[Query], error) {
	return in, nil
}

func (AbuseDB) Act(_ context.Context, in reflow.Envelope[Query]) (reflow.Envelope[Intel], error) {
	// Simulate lookup with realistic latency.
	time.Sleep(time.Duration(50+rand.IntN(100)) * time.Millisecond)

	report := SourceReport{
		Source:     "AbuseDB",
		Verdict:    "malicious",
		Confidence: 0.88,
		Details:    "IP found in 3 blocklists. Last reported 2 days ago for SSH brute force.",
		Tags:       []string{"brute-force", "ssh", "blocklisted"},
	}

	out := reflow.Envelope[Intel]{
		Value: Intel{IP: in.Value.IP, Sources: []SourceReport{report}},
		Meta:  in.Meta,
	}
	out = out.WithHint("source.abusedb",
		fmt.Sprintf("verdict=%s confidence=%.2f", report.Verdict, report.Confidence),
		report.Details)
	for _, tag := range report.Tags {
		out = out.WithHint("tag."+tag, "AbuseDB", "")
	}

	fmt.Printf("[AbuseDB]     verdict=%-11s confidence=%.2f  (%s)\n",
		report.Verdict, report.Confidence, report.Details)
	return out, nil
}

func (AbuseDB) Settle(_ context.Context, _ reflow.Envelope[Query], out reflow.Envelope[Intel], actErr error) (reflow.Envelope[Intel], bool, error) {
	if actErr != nil {
		out = out.WithHint("source.abusedb.error", actErr.Error(), "")
		return out, true, nil // don't fail pipeline if one source is down
	}
	return out, true, nil
}

// GeoReputation simulates a geolocation + reputation service.
type GeoReputation struct{}

func (GeoReputation) Resolve(_ context.Context, in reflow.Envelope[Query]) (reflow.Envelope[Query], error) {
	return in, nil
}

func (GeoReputation) Act(_ context.Context, in reflow.Envelope[Query]) (reflow.Envelope[Intel], error) {
	time.Sleep(time.Duration(50+rand.IntN(100)) * time.Millisecond)

	report := SourceReport{
		Source:     "GeoReputation",
		Verdict:    "suspicious",
		Confidence: 0.72,
		Details:    "IP geolocated to known hosting provider in region with elevated threat activity. ASN associated with 12 prior incidents.",
		Tags:       []string{"hosting-provider", "elevated-region", "repeat-asn"},
	}

	out := reflow.Envelope[Intel]{
		Value: Intel{IP: in.Value.IP, Sources: []SourceReport{report}},
		Meta:  in.Meta,
	}
	out = out.WithHint("source.georep",
		fmt.Sprintf("verdict=%s confidence=%.2f", report.Verdict, report.Confidence),
		report.Details)
	for _, tag := range report.Tags {
		out = out.WithHint("tag."+tag, "GeoReputation", "")
	}

	fmt.Printf("[GeoRep]      verdict=%-11s confidence=%.2f  (%s)\n",
		report.Verdict, report.Confidence, strings.Split(report.Details, ".")[0])
	return out, nil
}

func (GeoReputation) Settle(_ context.Context, _ reflow.Envelope[Query], out reflow.Envelope[Intel], actErr error) (reflow.Envelope[Intel], bool, error) {
	if actErr != nil {
		out = out.WithHint("source.georep.error", actErr.Error(), "")
		return out, true, nil
	}
	return out, true, nil
}

// BehaviorAnalysis simulates a traffic behavior analysis engine.
type BehaviorAnalysis struct{}

func (BehaviorAnalysis) Resolve(_ context.Context, in reflow.Envelope[Query]) (reflow.Envelope[Query], error) {
	return in, nil
}

func (BehaviorAnalysis) Act(_ context.Context, in reflow.Envelope[Query]) (reflow.Envelope[Intel], error) {
	time.Sleep(time.Duration(50+rand.IntN(100)) * time.Millisecond)

	report := SourceReport{
		Source:     "BehaviorAnalysis",
		Verdict:    "malicious",
		Confidence: 0.94,
		Details:    "Port scanning detected on 47 ports in last 24h. Pattern matches known reconnaissance toolkit.",
		Tags:       []string{"port-scan", "recon", "automated"},
	}

	out := reflow.Envelope[Intel]{
		Value: Intel{IP: in.Value.IP, Sources: []SourceReport{report}},
		Meta:  in.Meta,
	}
	out = out.WithHint("source.behavior",
		fmt.Sprintf("verdict=%s confidence=%.2f", report.Verdict, report.Confidence),
		report.Details)
	for _, tag := range report.Tags {
		out = out.WithHint("tag."+tag, "BehaviorAnalysis", "")
	}

	fmt.Printf("[Behavior]    verdict=%-11s confidence=%.2f  (%s)\n",
		report.Verdict, report.Confidence, strings.Split(report.Details, ".")[0])
	return out, nil
}

func (BehaviorAnalysis) Settle(_ context.Context, _ reflow.Envelope[Query], out reflow.Envelope[Intel], actErr error) (reflow.Envelope[Intel], bool, error) {
	if actErr != nil {
		out = out.WithHint("source.behavior.error", actErr.Error(), "")
		return out, true, nil
	}
	return out, true, nil
}

// --- Enrich: concurrent fan-out to multiple sources ---

// Enrich queries multiple intelligence sources concurrently and merges
// their results into a single Intel envelope with accumulated hints.
type Enrich struct {
	Sources []reflow.Node[Query, Intel]
}

func (Enrich) Resolve(_ context.Context, in reflow.Envelope[Query]) (reflow.Envelope[Query], error) {
	return in, nil
}

func (e Enrich) Act(ctx context.Context, in reflow.Envelope[Query]) (reflow.Envelope[Intel], error) {
	results := make([]reflow.Envelope[Intel], len(e.Sources))
	errs := make([]error, len(e.Sources))

	done := make(chan int, len(e.Sources))
	for i, src := range e.Sources {
		go func(i int, src reflow.Node[Query, Intel]) {
			results[i], errs[i] = reflow.Run(ctx, src, in)
			done <- i
		}(i, src)
	}
	for range e.Sources {
		<-done
	}

	for _, err := range errs {
		if err != nil {
			return reflow.Envelope[Intel]{Meta: in.Meta}, err
		}
	}

	return mergeIntel(results), nil
}

func (Enrich) Settle(_ context.Context, _ reflow.Envelope[Query], out reflow.Envelope[Intel], actErr error) (reflow.Envelope[Intel], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// --- Synthesis Node ---

// SynthesizeAssessment uses an LLM to produce a final threat assessment
// from the merged intelligence. It reads all the settled hints to build
// a focused prompt.
type SynthesizeAssessment struct {
	LLM llm.Provider
}

func (SynthesizeAssessment) Resolve(_ context.Context, in reflow.Envelope[Intel]) (reflow.Envelope[Intel], error) {
	return in, nil
}

func (s SynthesizeAssessment) Act(ctx context.Context, in reflow.Envelope[Intel]) (reflow.Envelope[Assessment], error) {
	out := reflow.Envelope[Assessment]{Meta: in.Meta}

	// Build structured context from hints.
	var sourceLines []string
	for _, src := range in.Value.Sources {
		sourceLines = append(sourceLines, fmt.Sprintf(
			"- %s: verdict=%s confidence=%.2f — %s (tags: %s)",
			src.Source, src.Verdict, src.Confidence, src.Details, strings.Join(src.Tags, ", "),
		))
	}

	// Collect all tags for the prompt.
	tagSet := make(map[string]bool)
	for _, h := range in.Meta.Hints {
		if strings.HasPrefix(h.Code, "tag.") {
			tagSet[strings.TrimPrefix(h.Code, "tag.")] = true
		}
	}
	var tags []string
	for t := range tagSet {
		tags = append(tags, t)
	}

	prompt := fmt.Sprintf(`IP: %s
Sources queried: %d

Intelligence reports:
%s

Observed tags: %s`,
		in.Value.IP,
		len(in.Value.Sources),
		strings.Join(sourceLines, "\n"),
		strings.Join(tags, ", "),
	)

	resp, err := s.LLM.Complete(ctx, llm.Messages{
		System: `You are a threat intelligence analyst. Given intelligence from multiple sources about an IP address, produce a threat assessment in JSON format:
{"verdict": "<malicious|suspicious|clean>", "confidence": <0.0-1.0>, "summary": "<2-3 sentence assessment>", "actions": ["<recommended action>", ...]}
Weight higher-confidence sources more heavily. If sources disagree, explain why.
Respond with ONLY valid JSON, no markdown.`,
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return out, fmt.Errorf("llm: %w", err)
	}

	content := extractJSON(resp.Content)
	var result struct {
		Verdict    string   `json:"verdict"`
		Confidence float64  `json:"confidence"`
		Summary    string   `json:"summary"`
		Actions    []string `json:"actions"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return out, fmt.Errorf("parse llm response: %w (raw: %s)", err, resp.Content)
	}

	out.Value = Assessment{
		IP:         in.Value.IP,
		Verdict:    result.Verdict,
		Confidence: result.Confidence,
		Summary:    result.Summary,
		Sources:    in.Value.Sources,
		Actions:    result.Actions,
	}
	return out, nil
}

func (SynthesizeAssessment) Settle(_ context.Context, _ reflow.Envelope[Intel], out reflow.Envelope[Assessment], actErr error) (reflow.Envelope[Assessment], bool, error) {
	if actErr != nil {
		out = out.WithHint("synthesis.error", actErr.Error(), "")
		return out, false, nil
	}
	valid := map[string]bool{"malicious": true, "suspicious": true, "clean": true}
	if !valid[out.Value.Verdict] {
		out = out.WithHint("synthesis.invalid_verdict",
			fmt.Sprintf("got %q, need: malicious/suspicious/clean", out.Value.Verdict), "")
		return out, false, nil
	}
	if out.Value.Summary == "" {
		out = out.WithHint("synthesis.empty", "LLM produced empty summary", "")
		return out, false, nil
	}
	return out, true, nil
}

// --- Main ---

func main() {
	provider := ollama.New("llama3.2")

	query := Query{
		IP:        "198.51.100.47",
		Timestamp: time.Now(),
	}

	// Fan out to three intelligence sources, merge results.
	enrich := Enrich{
		Sources: []reflow.Node[Query, Intel]{
			AbuseDB{},
			GeoReputation{},
			BehaviorAnalysis{},
		},
	}

	// Synthesize with LLM, retry up to 3 times for format enforcement.
	synthesize := reflow.WithRetry(SynthesizeAssessment{LLM: provider}, 3)

	// Query → [fan-out enrich] → merge → synthesize
	pipeline := reflow.Chain[Query, Intel, Assessment](enrich, synthesize)

	fmt.Printf("Threat Intel: %s\n", query.IP)
	fmt.Println(strings.Repeat("=", 50))
	fmt.Println()
	fmt.Println("Querying sources concurrently...")

	out, err := reflow.Run(context.Background(), pipeline, reflow.NewEnvelope(query))
	if err != nil {
		log.Fatalf("pipeline error: %v", err)
	}

	a := out.Value

	fmt.Println()
	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("VERDICT:    %s\n", strings.ToUpper(a.Verdict))
	fmt.Printf("CONFIDENCE: %.0f%%\n", a.Confidence*100)
	fmt.Println()
	fmt.Println("Assessment:")
	fmt.Printf("  %s\n", a.Summary)

	if len(a.Actions) > 0 {
		fmt.Println()
		fmt.Println("Recommended Actions:")
		for _, action := range a.Actions {
			fmt.Printf("  - %s\n", action)
		}
	}

	fmt.Println()
	fmt.Printf("Sources: %d queried | Hints: %d accumulated | Trace: %d steps\n",
		len(a.Sources), len(out.Meta.Hints), len(out.Meta.Trace))
}

// mergeIntel combines intelligence from concurrent source queries.
// It merges all source reports and all hints into a single envelope.
func mergeIntel(results []reflow.Envelope[Intel]) reflow.Envelope[Intel] {
	merged := Intel{}
	var meta reflow.Meta
	meta.Tags = make(map[string]string)

	for _, r := range results {
		if merged.IP == "" {
			merged.IP = r.Value.IP
		}
		merged.Sources = append(merged.Sources, r.Value.Sources...)
		meta.Hints = append(meta.Hints, r.Meta.Hints...)
		meta.Trace = append(meta.Trace, r.Meta.Trace...)
		for k, v := range r.Meta.Tags {
			meta.Tags[k] = v
		}
	}

	return reflow.Envelope[Intel]{Value: merged, Meta: meta}
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
