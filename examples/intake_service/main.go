// Example: Intake Service — routes customer requests through an intake
// graph. Deterministic triage picks a department, a switch selects the
// department node, and each department uses tool calls for resolution.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vertexbuild/reflow"
)

type Request struct {
	ID         string `json:"id"`
	CustomerID string `json:"customer_id"`
	Subject    string `json:"subject"`
	Body       string `json:"body"`
}

type Triage struct {
	Request    Request
	Department string
	Priority   string
	Confidence float64
}

type Resolution struct {
	ID         string  `json:"id"`
	Department string  `json:"department"`
	Priority   string  `json:"priority"`
	Path       string  `json:"path"`
	Confidence float64 `json:"confidence"`
	Result     string  `json:"result"`
}

type HintView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Span    string `json:"span,omitempty"`
}

type TraceSummary struct {
	Steps   int      `json:"steps"`
	Retries int      `json:"retries"`
	Tools   []string `json:"tools,omitempty"`
}

type ResponseView struct {
	Resolution Resolution `json:"resolution"`
	Hints      []HintView `json:"hints"`
	Trace      TraceSummary `json:"trace"`
}

type BillingRecord struct{ InvoiceID, Status string }
type IncidentStatus struct{ OpenIncident bool; Message string }
type AccountRecord struct{ ResetAvailable bool; Owner string }

func main() {
	billing := BillingDepartment{Ledger: BillingLedger{}}
	technical := TechnicalDepartment{Status: IncidentCatalog{}}
	account := AccountDepartment{Directory: AccountDirectory{}}
	escalation := reflow.WithRetry(EscalationDepartment{}, 3)

	intake := buildIntakeGraph(billing, technical, account, escalation)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("POST /intake", func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		req = normalizeRequest(req)

		out, err := reflow.Run(r.Context(), intake, reflow.NewEnvelope(req))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, toResponse(out))
	})

	addr := os.Getenv("REFLOW_ADDR")
	if addr == "" { addr = ":8080" }
	fmt.Printf("Reflow intake service listening on %s\n  POST /intake\n  GET  /healthz\n", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func buildIntakeGraph(
	billing reflow.Node[Triage, Resolution],
	technical reflow.Node[Triage, Resolution],
	account reflow.Node[Triage, Resolution],
	escalation reflow.Node[Triage, Resolution],
) reflow.Node[Request, Resolution] {
	return reflow.Compose("intake",
		func(ctx context.Context, s *reflow.Steps, in reflow.Envelope[Request]) reflow.Envelope[Resolution] {
			triaged := reflow.Do(s, ctx, TriageRequest{}, in)
			switch triaged.Value.Department {
			case "billing":
				return reflow.Do(s, ctx, billing, triaged)
			case "technical":
				return reflow.Do(s, ctx, technical, triaged)
			case "account":
				return reflow.Do(s, ctx, account, triaged)
			default:
				return reflow.Do(s, ctx, escalation, triaged)
			}
		},
	)
}

type TriageRequest struct{}

func (TriageRequest) Resolve(_ context.Context, in reflow.Envelope[Request]) (reflow.Envelope[Request], error) {
	return in, nil
}

func (TriageRequest) Act(_ context.Context, in reflow.Envelope[Request]) (reflow.Envelope[Triage], error) {
	triage := Triage{
		Request: in.Value, Department: routeDepartment(in.Value),
		Priority: routePriority(in.Value), Confidence: scoreConfidence(in.Value),
	}
	out := reflow.Map(in, triage).WithHint("triage.route",
		fmt.Sprintf("%s -> %s", in.Value.ID, triage.Department),
		fmt.Sprintf("priority=%s confidence=%.2f", triage.Priority, triage.Confidence),
	)
	if triage.Department == "escalation" {
		out = out.WithHint("triage.ambiguous", "request needs escalation review", in.Value.ID)
	}
	return out, nil
}

func (TriageRequest) Settle(_ context.Context, _ reflow.Envelope[Request], out reflow.Envelope[Triage], actErr error) (reflow.Envelope[Triage], bool, error) {
	return out, actErr == nil, actErr
}

type BillingLedger struct{}

func (BillingLedger) Name() string { return "billing.ledger" }
func (BillingLedger) Call(_ context.Context, t Triage) (BillingRecord, error) {
	text := strings.ToLower(t.Request.Subject + " " + t.Request.Body)
	switch {
	case strings.Contains(text, "refund") || strings.Contains(text, "duplicate"):
		return BillingRecord{"INV-duplicate", "duplicate-charge"}, nil
	case strings.Contains(text, "overdue"):
		return BillingRecord{"INV-overdue", "payment-failed"}, nil
	default:
		return BillingRecord{"INV-standard", "current"}, nil
	}
}

type BillingDepartment struct{ Ledger BillingLedger }

func (d BillingDepartment) Resolve(_ context.Context, in reflow.Envelope[Triage]) (reflow.Envelope[Triage], error) {
	return in, nil
}

func (d BillingDepartment) Act(ctx context.Context, in reflow.Envelope[Triage]) (reflow.Envelope[Resolution], error) {
	record, step, err := reflow.Use(ctx, d.Ledger, in.Value)
	out := reflow.Map(in, Resolution{
		ID:         in.Value.Request.ID,
		Department: "billing",
		Priority:   in.Value.Priority,
		Path:       "billing.fast",
		Confidence: in.Value.Confidence,
	}).WithStep(step)
	if err != nil { return out, err }
	switch record.Status {
	case "duplicate-charge":
		out.Value.Result = fmt.Sprintf("issue refund workflow for %s and reply with duplicate-charge confirmation", record.InvoiceID)
	case "payment-failed":
		out.Value.Result = fmt.Sprintf("send payment recovery instructions for %s", record.InvoiceID)
	default:
		out.Value.Result = fmt.Sprintf("reply with billing summary for %s", record.InvoiceID)
	}
	out = out.WithHint("billing.result", record.Status, record.InvoiceID)
	return out, nil
}

func (d BillingDepartment) Settle(_ context.Context, _ reflow.Envelope[Triage], out reflow.Envelope[Resolution], actErr error) (reflow.Envelope[Resolution], bool, error) {
	return out, actErr == nil, actErr
}

type IncidentCatalog struct{}

func (IncidentCatalog) Name() string { return "technical.status" }
func (IncidentCatalog) Call(_ context.Context, t Triage) (IncidentStatus, error) {
	text := strings.ToLower(t.Request.Subject + " " + t.Request.Body)
	switch {
	case strings.Contains(text, "outage") || strings.Contains(text, "down"):
		return IncidentStatus{true, "known incident INC-2041 is already open"}, nil
	case strings.Contains(text, "502"):
		return IncidentStatus{true, "recent deploy rollback is in progress"}, nil
	default:
		return IncidentStatus{false, "no matching incident, request further diagnostics"}, nil
	}
}

type TechnicalDepartment struct{ Status IncidentCatalog }

func (d TechnicalDepartment) Resolve(_ context.Context, in reflow.Envelope[Triage]) (reflow.Envelope[Triage], error) {
	return in, nil
}

func (d TechnicalDepartment) Act(ctx context.Context, in reflow.Envelope[Triage]) (reflow.Envelope[Resolution], error) {
	status, step, err := reflow.Use(ctx, d.Status, in.Value)
	out := reflow.Map(in, Resolution{
		ID:         in.Value.Request.ID,
		Department: "technical",
		Priority:   in.Value.Priority,
		Path:       "technical.fast",
		Confidence: in.Value.Confidence,
	}).WithStep(step)
	if err != nil { return out, err }
	if status.OpenIncident {
		out.Value.Result = "link the customer to the active incident and attach mitigation status"
	} else {
		out.Value.Result = "collect request IDs, timestamps, and last deployment context"
	}
	out = out.WithHint("technical.result", status.Message, in.Value.Request.ID)
	return out, nil
}

func (d TechnicalDepartment) Settle(_ context.Context, _ reflow.Envelope[Triage], out reflow.Envelope[Resolution], actErr error) (reflow.Envelope[Resolution], bool, error) {
	return out, actErr == nil, actErr
}

type AccountDirectory struct{}

func (AccountDirectory) Name() string { return "account.directory" }
func (AccountDirectory) Call(_ context.Context, t Triage) (AccountRecord, error) {
	text := strings.ToLower(t.Request.Subject + " " + t.Request.Body)
	r := AccountRecord{ResetAvailable: true, Owner: "workspace-admin"}
	if strings.Contains(text, "owner") { r.Owner = "finance-admin" }
	if strings.Contains(text, "locked") { r.ResetAvailable = false }
	return r, nil
}

type AccountDepartment struct{ Directory AccountDirectory }

func (d AccountDepartment) Resolve(_ context.Context, in reflow.Envelope[Triage]) (reflow.Envelope[Triage], error) {
	return in, nil
}

func (d AccountDepartment) Act(ctx context.Context, in reflow.Envelope[Triage]) (reflow.Envelope[Resolution], error) {
	record, step, err := reflow.Use(ctx, d.Directory, in.Value)
	out := reflow.Map(in, Resolution{
		ID:         in.Value.Request.ID,
		Department: "account",
		Priority:   in.Value.Priority,
		Path:       "account.fast",
		Confidence: in.Value.Confidence,
	}).WithStep(step)
	if err != nil { return out, err }
	if record.ResetAvailable {
		out.Value.Result = fmt.Sprintf("issue reset flow and confirm owner %s can complete the change", record.Owner)
	} else {
		out.Value.Result = fmt.Sprintf("account is locked; route owner %s to verification flow", record.Owner)
	}
	out = out.WithHint("account.result", record.Owner, in.Value.Request.ID)
	return out, nil
}

func (d AccountDepartment) Settle(_ context.Context, _ reflow.Envelope[Triage], out reflow.Envelope[Resolution], actErr error) (reflow.Envelope[Resolution], bool, error) {
	return out, actErr == nil, actErr
}

type EscalationDepartment struct{}

func (EscalationDepartment) Resolve(_ context.Context, in reflow.Envelope[Triage]) (reflow.Envelope[Triage], error) {
	return in, nil
}

func (EscalationDepartment) Act(ctx context.Context, in reflow.Envelope[Triage]) (reflow.Envelope[Resolution], error) {
	attempt, step, err := reflow.Invoke(ctx, "escalation.review", func(context.Context) (Resolution, error) {
		resolution := Resolution{
			ID:         in.Value.Request.ID,
			Department: "escalation",
			Priority:   in.Value.Priority,
			Path:       "escalation.review",
			Confidence: in.Value.Confidence,
			Result:     "collect a clearer summary before handing off to a specialist",
		}
		if len(in.HintsByCode("escalation.need_owner")) > 0 {
			resolution.Result = "assign to operations lead and summarize the conflicting signals for manual review"
		}
		if len(in.HintsByCode("escalation.need_customer_context")) > 0 {
			resolution.Result += "; include customer impact and latest failing action"
		}
		return resolution, nil
	})

	return reflow.Map(in, attempt).WithStep(step), err
}

func (EscalationDepartment) Settle(_ context.Context, _ reflow.Envelope[Triage], out reflow.Envelope[Resolution], actErr error) (reflow.Envelope[Resolution], bool, error) {
	if actErr != nil {
		return out, false, actErr
	}
	if len(out.HintsByCode("escalation.need_owner")) == 0 {
		out = out.WithHint("escalation.need_owner", "assign a human owner for escalated requests", out.Value.ID)
		return out, false, nil
	}
	if len(out.HintsByCode("escalation.need_customer_context")) == 0 {
		out = out.WithHint("escalation.need_customer_context", "include customer impact in the handoff", out.Value.ID)
		return out, false, nil
	}
	out.Value.Confidence = 0.95
	return out, true, nil
}

func normalizeRequest(r Request) Request {
	r.ID, r.CustomerID = strings.TrimSpace(r.ID), strings.TrimSpace(r.CustomerID)
	r.Subject, r.Body = strings.TrimSpace(r.Subject), strings.TrimSpace(r.Body)
	if r.ID == "" {
		r.ID = fmt.Sprintf("REQ-%d", time.Now().UnixNano())
	}
	return r
}

func routeDepartment(req Request) string {
	text := strings.ToLower(req.Subject + " " + req.Body)
	switch {
	case strings.Contains(text, "invoice") || strings.Contains(text, "refund") || strings.Contains(text, "charged") || strings.Contains(text, "billing"):
		return "billing"
	case strings.Contains(text, "password") || strings.Contains(text, "login") || strings.Contains(text, "owner"):
		return "account"
	case strings.Contains(text, "outage") || strings.Contains(text, "502") || strings.Contains(text, "api") || strings.Contains(text, "down"):
		return "technical"
	default:
		return "escalation"
	}
}

func routePriority(req Request) string {
	text := strings.ToLower(req.Subject + " " + req.Body)
	if strings.Contains(text, "urgent") || strings.Contains(text, "p1") || strings.Contains(text, "outage") || strings.Contains(text, "down") {
		return "urgent"
	}
	return "standard"
}

func scoreConfidence(req Request) float64 {
	s := 1.0
	if strings.TrimSpace(req.CustomerID) == "" { s -= 0.20 }
	if strings.TrimSpace(req.Subject) == "" { s -= 0.20 }
	if len(strings.Fields(req.Body)) < 6 { s -= 0.15 }
	if routeDepartment(req) == "escalation" { s -= 0.25 }
	if s < 0 { return 0 }
	return s
}

func toResponse(env reflow.Envelope[Resolution]) ResponseView {
	var (
		hints   = make([]HintView, 0, len(env.Meta.Hints))
		retries int
		tools   []string
		seen    = map[string]bool{}
	)
	for _, h := range env.Meta.Hints {
		hints = append(hints, HintView{Code: h.Code, Message: h.Message, Span: h.Span})
	}
	for _, s := range env.Meta.Trace {
		if s.Phase == "settle" && s.Status == "retry" { retries++ }
		if s.Phase == "tool" && s.Node != "" && !seen[s.Node] { seen[s.Node] = true; tools = append(tools, s.Node) }
	}
	return ResponseView{
		Resolution: env.Value, Hints: hints,
		Trace: TraceSummary{Steps: len(env.Meta.Trace), Retries: retries, Tools: tools},
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}
