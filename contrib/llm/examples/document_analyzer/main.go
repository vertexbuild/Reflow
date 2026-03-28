// Example: Document Analyzer
//
// Pipeline: extract → validate → summarize
//
// A multi-stage pipeline that extracts structured fields from a raw
// document, validates them with confidence scoring, and uses an LLM
// to summarize only what couldn't be resolved deterministically.
//
// This demonstrates:
//   - Deterministic nodes doing cheap work first
//   - Settle hints flowing between stages
//   - LLM used only where synthesis or ambiguity requires it
//   - WithRetry on the LLM node for format enforcement
//
// Run with: go run ./examples/document_analyzer/
// Requires: Ollama running with llama3.2 pulled.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/vertexbuild/reflow"
	"github.com/vertexbuild/reflow/llm"
	"github.com/vertexbuild/reflow/llm/ollama"
)

// --- Types ---

// Document is the raw input.
type Document struct {
	Source string
	Text   string
}

// Fields are the structured data extracted from a document.
type Fields struct {
	PatientName string
	DOB         string
	CPTCodes    []string
	DxCodes     []string
	Provider    string
	TotalCharge string
	PayerName   string
	ClaimDate   string
	Raw         map[string]string // catch-all for unstructured extractions
}

// ValidatedFields adds confidence and flags to extracted fields.
type ValidatedFields struct {
	Fields     Fields
	Confidence float64
	Missing    []string
	Suspect    []string
}

// AnalysisReport is the final output.
type AnalysisReport struct {
	Fields     Fields
	Confidence float64
	Summary    string
	Flags      []string
}

// --- Nodes ---

// ExtractFields pulls structured data from raw text using regex and
// pattern matching. No LLM needed — just deterministic extraction.
type ExtractFields struct{}

func (ExtractFields) Resolve(_ context.Context, in reflow.Envelope[Document]) (reflow.Envelope[Document], error) {
	return in, nil
}

func (ExtractFields) Act(_ context.Context, in reflow.Envelope[Document]) (reflow.Envelope[Fields], error) {
	text := in.Value.Text
	out := reflow.Envelope[Fields]{Meta: in.Meta}
	f := Fields{Raw: make(map[string]string)}

	// Patient name: look for "Patient:" or "Name:" patterns.
	f.PatientName = extractPattern(text, `(?i)(?:patient|name)\s*:\s*([A-Za-z][A-Za-z ,.'-]{1,40})`)
	if f.PatientName == "" {
		out = out.WithHint("extract.missing", "could not find patient name", "field:patient_name")
	}

	// DOB
	f.DOB = extractPattern(text, `(?i)(?:dob|date of birth|birth date)\s*:\s*([\d/.-]+)`)
	if f.DOB == "" {
		out = out.WithHint("extract.missing", "could not find date of birth", "field:dob")
	}

	// CPT codes: 5-digit procedure codes.
	cptRe := regexp.MustCompile(`\b(9\d{4})\b`)
	for _, match := range cptRe.FindAllString(text, -1) {
		f.CPTCodes = append(f.CPTCodes, match)
	}
	if len(f.CPTCodes) == 0 {
		out = out.WithHint("extract.missing", "no CPT codes found", "field:cpt_codes")
	}

	// Dx codes: ICD-10 format (letter + digits + optional dot + digits).
	dxRe := regexp.MustCompile(`\b([A-Z]\d{2}(?:\.\d{1,4})?)\b`)
	for _, match := range dxRe.FindAllString(text, -1) {
		f.DxCodes = append(f.DxCodes, match)
	}

	// Provider
	f.Provider = extractPattern(text, `(?i)(?:provider|physician|doctor|dr\.?)\s*:\s*([A-Za-z][A-Za-z ,.'-]{1,40})`)

	// Total charge
	f.TotalCharge = extractPattern(text, `(?i)(?:total|charge|amount)\s*:\s*\$?([\d,]+\.?\d*)`)

	// Payer
	f.PayerName = extractPattern(text, `(?i)(?:payer|insurance|carrier)\s*:\s*([A-Za-z][A-Za-z &.'-]{1,50})`)

	// Claim date
	f.ClaimDate = extractPattern(text, `(?i)(?:claim date|date of service|dos)\s*:\s*([\d/.-]+)`)

	out.Value = f
	out = out.WithTag("source", in.Value.Source)
	return out, nil
}

func (ExtractFields) Settle(_ context.Context, _ reflow.Envelope[Document], out reflow.Envelope[Fields], actErr error) (reflow.Envelope[Fields], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// ValidateExtraction checks completeness and flags suspect values.
type ValidateExtraction struct{}

func (ValidateExtraction) Resolve(_ context.Context, in reflow.Envelope[Fields]) (reflow.Envelope[Fields], error) {
	return in, nil
}

func (ValidateExtraction) Act(_ context.Context, in reflow.Envelope[Fields]) (reflow.Envelope[ValidatedFields], error) {
	f := in.Value
	out := reflow.Envelope[ValidatedFields]{Meta: in.Meta}
	v := ValidatedFields{Fields: f, Confidence: 1.0}

	// Check required fields.
	if f.PatientName == "" {
		v.Missing = append(v.Missing, "patient_name")
		v.Confidence -= 0.2
	}
	if f.DOB == "" {
		v.Missing = append(v.Missing, "dob")
		v.Confidence -= 0.1
	}
	if len(f.CPTCodes) == 0 {
		v.Missing = append(v.Missing, "cpt_codes")
		v.Confidence -= 0.25
	}
	if f.TotalCharge == "" {
		v.Missing = append(v.Missing, "total_charge")
		v.Confidence -= 0.15
	}
	if f.PayerName == "" {
		v.Missing = append(v.Missing, "payer_name")
		v.Confidence -= 0.1
	}

	// Flag suspect values.
	if f.TotalCharge != "" {
		// Suspiciously high charge.
		if strings.Contains(f.TotalCharge, ",") || len(f.TotalCharge) > 7 {
			v.Suspect = append(v.Suspect, "total_charge looks unusually high")
			out = out.WithHint("validate.suspect", "total charge may be unusually high: "+f.TotalCharge, "field:total_charge")
			v.Confidence -= 0.1
		}
	}

	if len(v.Missing) > 0 {
		out = out.WithHint("validate.incomplete",
			fmt.Sprintf("%d required fields missing: %s", len(v.Missing), strings.Join(v.Missing, ", ")),
			"")
	}

	if v.Confidence < 0 {
		v.Confidence = 0
	}

	out.Value = v
	return out, nil
}

func (ValidateExtraction) Settle(_ context.Context, _ reflow.Envelope[Fields], out reflow.Envelope[ValidatedFields], actErr error) (reflow.Envelope[ValidatedFields], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// Summarize uses an LLM to produce a human-readable analysis,
// incorporating the structured fields and any flags from validation.
type Summarize struct {
	LLM llm.Provider
}

func (Summarize) Resolve(_ context.Context, in reflow.Envelope[ValidatedFields]) (reflow.Envelope[ValidatedFields], error) {
	return in, nil
}

func (s Summarize) Act(ctx context.Context, in reflow.Envelope[ValidatedFields]) (reflow.Envelope[AnalysisReport], error) {
	v := in.Value
	out := reflow.Envelope[AnalysisReport]{Meta: in.Meta}

	// Build a focused prompt using the extracted fields and hints.
	var context_lines []string
	context_lines = append(context_lines, fmt.Sprintf("Patient: %s", valueOr(v.Fields.PatientName, "unknown")))
	context_lines = append(context_lines, fmt.Sprintf("DOB: %s", valueOr(v.Fields.DOB, "unknown")))
	context_lines = append(context_lines, fmt.Sprintf("Provider: %s", valueOr(v.Fields.Provider, "unknown")))
	context_lines = append(context_lines, fmt.Sprintf("Payer: %s", valueOr(v.Fields.PayerName, "unknown")))
	context_lines = append(context_lines, fmt.Sprintf("Claim Date: %s", valueOr(v.Fields.ClaimDate, "unknown")))
	context_lines = append(context_lines, fmt.Sprintf("CPT Codes: %s", strings.Join(v.Fields.CPTCodes, ", ")))
	context_lines = append(context_lines, fmt.Sprintf("Dx Codes: %s", strings.Join(v.Fields.DxCodes, ", ")))
	context_lines = append(context_lines, fmt.Sprintf("Total Charge: $%s", valueOr(v.Fields.TotalCharge, "unknown")))
	context_lines = append(context_lines, fmt.Sprintf("Confidence: %.0f%%", v.Confidence*100))

	if len(v.Missing) > 0 {
		context_lines = append(context_lines, fmt.Sprintf("Missing fields: %s", strings.Join(v.Missing, ", ")))
	}
	if len(v.Suspect) > 0 {
		context_lines = append(context_lines, fmt.Sprintf("Suspect values: %s", strings.Join(v.Suspect, "; ")))
	}

	// Include upstream hints for additional context.
	var hintLines []string
	for _, h := range in.Meta.Hints {
		hintLines = append(hintLines, fmt.Sprintf("- [%s] %s", h.Code, h.Message))
	}
	if len(hintLines) > 0 {
		context_lines = append(context_lines, "\nPipeline observations:")
		context_lines = append(context_lines, hintLines...)
	}

	resp, err := s.LLM.Complete(ctx, llm.Messages{
		System: `You are a medical billing analyst. Given extracted claim fields and validation notes, produce a brief analysis in JSON format:
{"summary": "<2-3 sentence analysis>", "flags": ["<actionable flag>", ...]}
Focus on: completeness, potential issues, and recommended actions.
Respond with ONLY valid JSON, no markdown.`,
		Messages: []llm.Message{
			{Role: "user", Content: strings.Join(context_lines, "\n")},
		},
	})
	if err != nil {
		return out, fmt.Errorf("llm: %w", err)
	}

	// Parse the LLM response.
	content := extractJSON(resp.Content)
	var analysis struct {
		Summary string   `json:"summary"`
		Flags   []string `json:"flags"`
	}
	if err := json.Unmarshal([]byte(content), &analysis); err != nil {
		return out, fmt.Errorf("parse llm response: %w (raw: %s)", err, resp.Content)
	}

	out.Value = AnalysisReport{
		Fields:     v.Fields,
		Confidence: v.Confidence,
		Summary:    analysis.Summary,
		Flags:      analysis.Flags,
	}
	return out, nil
}

func (s Summarize) Settle(_ context.Context, _ reflow.Envelope[ValidatedFields], out reflow.Envelope[AnalysisReport], actErr error) (reflow.Envelope[AnalysisReport], bool, error) {
	if actErr != nil {
		out = out.WithHint("llm.error", actErr.Error(), "")
		return out, false, nil
	}
	if out.Value.Summary == "" {
		out = out.WithHint("llm.empty", "LLM produced empty summary", "")
		return out, false, nil
	}
	return out, true, nil
}

// --- Main ---

func main() {
	provider := ollama.New("llama3.2")

	doc := Document{
		Source: "claim-2024-0847.txt",
		Text: `
MEDICAL CLAIM FORM
===================
Patient: Maria Santos
Date of Birth: 03/15/1982
Provider: Dr. James Wilson, MD

Date of Service: 01/20/2024
Payer: Blue Cross Blue Shield

Procedure Codes:
  99214 - Office visit, established patient (moderate complexity)
  93000 - Electrocardiogram, routine

Diagnosis Codes:
  I10   - Essential hypertension
  R00.0 - Tachycardia, unspecified

Total Charge: $485.00

Notes: Patient presented with elevated BP (158/95) and palpitations.
ECG showed sinus tachycardia. Follow-up in 2 weeks. May need
24-hour Holter monitor if symptoms persist.
`,
	}

	extract := ExtractFields{}
	validate := ValidateExtraction{}
	summarize := reflow.WithRetry(Summarize{LLM: provider}, 3)

	validateAndSummarize := reflow.Chain[Fields, ValidatedFields, AnalysisReport](validate, summarize)
	pipeline := reflow.Chain[Document, Fields, AnalysisReport](extract, validateAndSummarize)

	fmt.Printf("Analyzing: %s\n", doc.Source)
	fmt.Println(strings.Repeat("=", 50))

	out, err := reflow.Run(context.Background(), pipeline, reflow.NewEnvelope(doc))
	if err != nil {
		log.Fatalf("pipeline error: %v", err)
	}

	report := out.Value

	fmt.Println()
	fmt.Println("Extracted Fields:")
	fmt.Printf("  Patient:      %s\n", report.Fields.PatientName)
	fmt.Printf("  DOB:          %s\n", report.Fields.DOB)
	fmt.Printf("  Provider:     %s\n", report.Fields.Provider)
	fmt.Printf("  Payer:        %s\n", report.Fields.PayerName)
	fmt.Printf("  Claim Date:   %s\n", report.Fields.ClaimDate)
	fmt.Printf("  CPT Codes:    %s\n", strings.Join(report.Fields.CPTCodes, ", "))
	fmt.Printf("  Dx Codes:     %s\n", strings.Join(report.Fields.DxCodes, ", "))
	fmt.Printf("  Total Charge: $%s\n", report.Fields.TotalCharge)

	fmt.Println()
	fmt.Printf("Confidence: %.0f%%\n", report.Confidence*100)

	fmt.Println()
	fmt.Println("LLM Analysis:")
	fmt.Printf("  %s\n", report.Summary)

	if len(report.Flags) > 0 {
		fmt.Println()
		fmt.Println("Flags:")
		for _, f := range report.Flags {
			fmt.Printf("  - %s\n", f)
		}
	}

	fmt.Println()
	fmt.Println("Pipeline Hints:")
	for _, h := range out.Meta.Hints {
		fmt.Printf("  [%s] %s\n", h.Code, h.Message)
	}

	fmt.Println()
	fmt.Printf("Trace: %d steps\n", len(out.Meta.Trace))
}

// --- Helpers ---

func extractPattern(text, pattern string) string {
	re := regexp.MustCompile(pattern)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
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
