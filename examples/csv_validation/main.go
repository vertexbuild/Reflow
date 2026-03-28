// Example: CSV → Structured Records with Confidence
//
// Pipeline: parse → validate → route (high → accept, low → normalize)
//
// A validator scores each record's confidence. High-confidence records
// pass through. Low-confidence records get hints about what's suspect,
// and a normalizer uses those hints to clean the data.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/vertexbuild/reflow"
)

type Record struct {
	Name   string
	Code   string
	Amount string
	Status string
}

type Scored struct {
	Record     Record
	Confidence float64
}

type Records = []Record
type ScoredRecords = []Scored

// ParseCSV splits raw CSV into records.
type ParseCSV struct{}

func (ParseCSV) Resolve(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
	return in, nil
}

func (ParseCSV) Act(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[Records], error) {
	lines := strings.Split(strings.TrimSpace(in.Value), "\n")
	var records Records
	for _, line := range lines[1:] {
		fields := strings.Split(line, ",")
		if len(fields) != 4 {
			continue
		}
		records = append(records, Record{
			Name:   strings.TrimSpace(fields[0]),
			Code:   strings.TrimSpace(fields[1]),
			Amount: strings.TrimSpace(fields[2]),
			Status: strings.TrimSpace(fields[3]),
		})
	}
	return reflow.Envelope[Records]{Value: records, Meta: in.Meta}, nil
}

func (ParseCSV) Settle(_ context.Context, _ reflow.Envelope[string], out reflow.Envelope[Records], actErr error) (reflow.Envelope[Records], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// ValidateRecords scores each record's confidence and leaves hints
// about suspect fields.
type ValidateRecords struct{}

func (ValidateRecords) Resolve(_ context.Context, in reflow.Envelope[Records]) (reflow.Envelope[Records], error) {
	return in, nil
}

func (ValidateRecords) Act(_ context.Context, in reflow.Envelope[Records]) (reflow.Envelope[ScoredRecords], error) {
	var scored ScoredRecords
	out := reflow.Envelope[ScoredRecords]{Meta: in.Meta}

	for i, r := range in.Value {
		conf := 1.0
		tag := fmt.Sprintf("record[%d]", i)

		if r.Name == "" {
			conf -= 0.4
			out = out.WithHint("field.missing", tag+": name is empty", "")
		}
		if r.Amount == "" {
			conf -= 0.3
			out = out.WithHint("field.missing", tag+": amount is empty", "")
		}
		if !isValidCPT(r.Code) {
			conf -= 0.3
			out = out.WithHint("field.suspect", fmt.Sprintf("%s: code %q invalid", tag, r.Code), "")
		}
		if conf < 0 {
			conf = 0
		}
		scored = append(scored, Scored{Record: r, Confidence: conf})
	}

	out.Value = scored
	return out, nil
}

func (ValidateRecords) Settle(_ context.Context, _ reflow.Envelope[Records], out reflow.Envelope[ScoredRecords], actErr error) (reflow.Envelope[ScoredRecords], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// RouteByConfidence accepts high-confidence records and normalizes
// low-confidence ones using the settled hints.
type RouteByConfidence struct {
	Threshold float64
}

func (RouteByConfidence) Resolve(_ context.Context, in reflow.Envelope[ScoredRecords]) (reflow.Envelope[ScoredRecords], error) {
	return in, nil
}

func (r RouteByConfidence) Act(_ context.Context, in reflow.Envelope[ScoredRecords]) (reflow.Envelope[ScoredRecords], error) {
	var accepted, needsWork ScoredRecords
	for _, s := range in.Value {
		if s.Confidence >= r.Threshold {
			accepted = append(accepted, s)
		} else {
			needsWork = append(needsWork, s)
		}
	}

	fmt.Printf("[route] %d accepted, %d need normalization\n", len(accepted), len(needsWork))

	for i := range needsWork {
		rec := &needsWork[i].Record
		if rec.Name == "" {
			rec.Name = "UNKNOWN"
		}
		if rec.Amount == "" {
			rec.Amount = "0.00"
		}
		if !isValidCPT(rec.Code) {
			rec.Code = "REVIEW:" + rec.Code
		}
		needsWork[i].Confidence = 0.6
	}

	all := append(accepted, needsWork...)
	return reflow.Envelope[ScoredRecords]{Value: all, Meta: in.Meta}, nil
}

func (RouteByConfidence) Settle(_ context.Context, _ reflow.Envelope[ScoredRecords], out reflow.Envelope[ScoredRecords], actErr error) (reflow.Envelope[ScoredRecords], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

func main() {
	csv := `Name,Code,Amount,Status
Jane Doe,99213,150.00,paid
,99214,200.00,pending
Bob Smith,XXXXX,,denied
Alice Park,99215,75.50,paid`

	parse := ParseCSV{}
	validate := ValidateRecords{}
	route := RouteByConfidence{Threshold: 0.8}

	pipeline := reflow.Chain[string, Records, ScoredRecords](
		parse,
		reflow.Chain[Records, ScoredRecords, ScoredRecords](validate, route),
	)

	fmt.Println("Input CSV:")
	fmt.Println(csv)
	fmt.Println()

	out, err := reflow.Run(context.Background(), pipeline, reflow.NewEnvelope(csv))
	if err != nil {
		log.Fatalf("pipeline error: %v", err)
	}

	fmt.Println()
	fmt.Println("Results:")
	for _, s := range out.Value {
		fmt.Printf("  %-12s %-14s %-8s %-7s (%.2f)\n",
			s.Record.Name, s.Record.Code, s.Record.Amount, s.Record.Status, s.Confidence)
	}

	fmt.Println()
	fmt.Println("Hints:")
	for _, h := range out.Meta.Hints {
		fmt.Printf("  [%s] %s\n", h.Code, h.Message)
	}
}

func isValidCPT(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
