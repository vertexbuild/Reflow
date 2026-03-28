// Example: Malformed JSON Repair
//
// Pipeline: parse → repair → validate
//
// The parse node fails, settles useful hints about what went wrong,
// and the repair node reads those hints to apply a targeted fix.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/vertexbuild/reflow"
)

type JSON = map[string]any

// ParseJSON attempts to parse JSON. On failure it settles hints about
// what went wrong instead of returning an error.
type ParseJSON struct{}

func (ParseJSON) Resolve(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
	return in, nil
}

func (ParseJSON) Act(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[JSON], error) {
	var v JSON
	err := json.Unmarshal([]byte(in.Value), &v)
	return reflow.Envelope[JSON]{Value: v, Meta: in.Meta}, err
}

func (ParseJSON) Settle(_ context.Context, in reflow.Envelope[string], out reflow.Envelope[JSON], actErr error) (reflow.Envelope[JSON], bool, error) {
	if actErr == nil {
		return out, true, nil
	}
	errMsg := actErr.Error()
	out = out.WithHint("json.malformed", errMsg, "")
	if strings.Contains(errMsg, "invalid character '}'") {
		out = out.WithHint("json.likely_cause", "trailing comma before closing brace", "")
	}
	out = out.WithTag("original_input", in.Value)
	return out, true, nil
}

// RepairJSON reads hints from upstream and applies targeted fixes.
type RepairJSON struct{}

func (RepairJSON) Resolve(_ context.Context, in reflow.Envelope[JSON]) (reflow.Envelope[JSON], error) {
	if hints := in.HintsByCode("json.malformed"); len(hints) > 0 {
		fmt.Println("[repair] Received hints from upstream:")
		for _, h := range in.Meta.Hints {
			fmt.Printf("  %s: %s\n", h.Code, h.Message)
		}
	}
	return in, nil
}

func (RepairJSON) Act(_ context.Context, in reflow.Envelope[JSON]) (reflow.Envelope[JSON], error) {
	if in.Value != nil {
		return in, nil
	}
	original := in.Meta.Tags["original_input"]
	if original == "" {
		return in, fmt.Errorf("no original input to repair")
	}
	repaired := original
	for _, cause := range in.HintsByCode("json.likely_cause") {
		if strings.Contains(cause.Message, "trailing comma") {
			fmt.Println("[repair] Applying fix: removing trailing comma")
			repaired = removeTrailingCommas(repaired)
		}
	}
	var v JSON
	if err := json.Unmarshal([]byte(repaired), &v); err != nil {
		return in, fmt.Errorf("repair failed: %w", err)
	}
	return reflow.Envelope[JSON]{Value: v, Meta: in.Meta}, nil
}

func (RepairJSON) Settle(_ context.Context, _ reflow.Envelope[JSON], out reflow.Envelope[JSON], actErr error) (reflow.Envelope[JSON], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

// ValidateJSON checks for required fields.
type ValidateJSON struct {
	Required []string
}

func (ValidateJSON) Resolve(_ context.Context, in reflow.Envelope[JSON]) (reflow.Envelope[JSON], error) {
	return in, nil
}

func (v ValidateJSON) Act(_ context.Context, in reflow.Envelope[JSON]) (reflow.Envelope[JSON], error) {
	var missing []string
	for _, key := range v.Required {
		if _, ok := in.Value[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return in.WithHint("validation.missing_fields",
			fmt.Sprintf("missing: %s", strings.Join(missing, ", ")), ""), nil
	}
	return in.WithHint("validation.ok", "all required fields present", ""), nil
}

func (ValidateJSON) Settle(_ context.Context, _ reflow.Envelope[JSON], out reflow.Envelope[JSON], actErr error) (reflow.Envelope[JSON], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	return out, true, nil
}

func main() {
	input := `{"patient": "Jane Doe", "code": "99213", "amount": 150.00, }`

	parse := ParseJSON{}
	repair := RepairJSON{}
	validate := ValidateJSON{Required: []string{"patient", "code", "amount"}}

	pipeline := reflow.Chain[string, JSON, JSON](
		parse,
		reflow.Chain[JSON, JSON, JSON](repair, validate),
	)

	fmt.Println("Input:", input)
	fmt.Println()

	out, err := reflow.Run(context.Background(), pipeline, reflow.NewEnvelope(input))
	if err != nil {
		log.Fatalf("pipeline error: %v", err)
	}

	fmt.Println()
	fmt.Println("Output:", out.Value)
	fmt.Println()
	fmt.Println("Hints accumulated:")
	for _, h := range out.Meta.Hints {
		fmt.Printf("  [%s] %s\n", h.Code, h.Message)
	}
}

func removeTrailingCommas(s string) string {
	result := []byte(s)
	for i := 0; i < len(result); i++ {
		if result[i] == ',' {
			j := i + 1
			for j < len(result) && (result[j] == ' ' || result[j] == '\n' || result[j] == '\t' || result[j] == '\r') {
				j++
			}
			if j < len(result) && (result[j] == '}' || result[j] == ']') {
				result = append(result[:i], result[i+1:]...)
				i--
			}
		}
	}
	return string(result)
}
