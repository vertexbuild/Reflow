// Example: Log Line Classification
//
// Pipeline: detect → analyze
//
// A detector classifies log lines with simple rules. Lines it recognizes
// get a category. Unknown lines get hints about partial signals, and only
// those are sent to a more expensive analysis step.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/vertexbuild/reflow"
)

type Entry struct {
	Line     string
	Category string
	Detail   string
}

type Entries = []Entry

// DetectPatterns classifies log lines using cheap rules. Unknown lines
// get partial signal hints settled into the envelope.
type DetectPatterns struct{}

func (DetectPatterns) Resolve(_ context.Context, in reflow.Envelope[[]string]) (reflow.Envelope[[]string], error) {
	return in, nil
}

func (DetectPatterns) Act(_ context.Context, in reflow.Envelope[[]string]) (reflow.Envelope[Entries], error) {
	var entries Entries
	out := reflow.Envelope[Entries]{Meta: in.Meta}

	for i, line := range in.Value {
		entry := Entry{Line: line}
		lower := strings.ToLower(line)

		switch {
		case strings.Contains(lower, "timed out"):
			entry.Category = "timeout"
			entry.Detail = "connection timeout detected"
		case strings.Contains(lower, "memory usage"):
			entry.Category = "resource"
			entry.Detail = "memory pressure warning"
		case strings.Contains(lower, "deployment") || strings.Contains(lower, "rolling out"):
			entry.Category = "deploy"
			entry.Detail = "deployment event"
		default:
			entry.Category = "unknown"
			out = out.WithHint("pattern.unknown", fmt.Sprintf("line %d: no known pattern", i), line)
			if strings.Contains(lower, "error") {
				out = out.WithHint("signal.error_level", fmt.Sprintf("line %d: ERROR level", i), line)
			}
			if strings.Contains(lower, "eof") {
				out = out.WithHint("signal.eof", fmt.Sprintf("line %d: EOF indicator", i), line)
			}
			if strings.Contains(lower, "ora-") {
				out = out.WithHint("signal.oracle_error", fmt.Sprintf("line %d: Oracle error code", i), line)
			}
		}
		entries = append(entries, entry)
	}

	out.Value = entries
	return out, nil
}

func (DetectPatterns) Settle(_ context.Context, _ reflow.Envelope[[]string], out reflow.Envelope[Entries], actErr error) (reflow.Envelope[Entries], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// AnalyzeUnknowns reads hints from upstream to classify entries
// that the detector couldn't handle.
type AnalyzeUnknowns struct{}

func (AnalyzeUnknowns) Resolve(_ context.Context, in reflow.Envelope[Entries]) (reflow.Envelope[Entries], error) {
	unknowns := 0
	for _, e := range in.Value {
		if e.Category == "unknown" {
			unknowns++
		}
	}
	if unknowns > 0 {
		fmt.Printf("[analyze] %d/%d entries need analysis\n", unknowns, len(in.Value))
		fmt.Println("[analyze] Hints from upstream:")
		for _, h := range in.Meta.Hints {
			fmt.Printf("  [%s] %s\n", h.Code, h.Message)
		}
	}
	return in, nil
}

func (AnalyzeUnknowns) Act(_ context.Context, in reflow.Envelope[Entries]) (reflow.Envelope[Entries], error) {
	entries := make(Entries, len(in.Value))
	copy(entries, in.Value)

	oracleHints := in.HintsByCode("signal.oracle_error")
	eofHints := in.HintsByCode("signal.eof")

	for i := range entries {
		if entries[i].Category != "unknown" {
			continue
		}
		for _, h := range oracleHints {
			if h.Span == entries[i].Line {
				entries[i].Category = "database"
				entries[i].Detail = "Oracle database error — table missing"
				fmt.Printf("[analyze] line %d: classified via oracle hint\n", i)
			}
		}
		for _, h := range eofHints {
			if h.Span == entries[i].Line {
				entries[i].Category = "connection"
				entries[i].Detail = "unexpected connection termination during TLS"
				fmt.Printf("[analyze] line %d: classified via EOF hint\n", i)
			}
		}
		if entries[i].Category == "unknown" {
			entries[i].Detail = "unrecognized — needs manual review"
		}
	}

	return reflow.Envelope[Entries]{Value: entries, Meta: in.Meta}, nil
}

func (AnalyzeUnknowns) Settle(_ context.Context, _ reflow.Envelope[Entries], out reflow.Envelope[Entries], actErr error) (reflow.Envelope[Entries], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

func main() {
	lines := []string{
		"2024-01-15 10:23:45 ERROR connection timed out after 30s to db-primary",
		"2024-01-15 10:23:46 WARN memory usage at 89% on worker-3",
		"2024-01-15 10:23:47 ERROR unexpected EOF during TLS handshake with upstream-api",
		"2024-01-15 10:23:48 ERROR ORA-00942: table or view does not exist",
		"2024-01-15 10:23:49 INFO deployment v2.4.1 rolling out to zone-b",
	}

	detect := DetectPatterns{}
	analyze := AnalyzeUnknowns{}

	pipeline := reflow.Chain[[]string, Entries, Entries](detect, analyze)

	fmt.Println("Input log lines:")
	for _, l := range lines {
		fmt.Println(" ", l)
	}
	fmt.Println()

	out, err := reflow.Run(context.Background(), pipeline, reflow.NewEnvelope(lines))
	if err != nil {
		log.Fatalf("pipeline error: %v", err)
	}

	fmt.Println()
	fmt.Println("Results:")
	for i, e := range out.Value {
		fmt.Printf("  [%d] %-12s %s\n", i, e.Category, e.Detail)
	}
}
