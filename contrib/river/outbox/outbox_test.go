package outbox

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vertexbuild/reflow"
)

// --- Serialization ---

func TestArgsKind(t *testing.T) {
	a := Args{}
	if a.Kind() != "reflow.outbox" {
		t.Fatalf("expected kind 'reflow.outbox', got %q", a.Kind())
	}
}

func TestArgsRoundTrip(t *testing.T) {
	type Claim struct {
		ID     string  `json:"id"`
		Amount float64 `json:"amount"`
	}

	env := reflow.NewEnvelope(Claim{ID: "CLM-001", Amount: 485.00}).
		WithTag("source", "intake").
		WithHint("validation.pending", "needs review", "")

	payload, err := json.Marshal(env.Value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	args := Args{
		Kind_:   "process.claim",
		Payload: payload,
		Meta:    metaToJSON(env.Meta),
	}

	// Serialize to JSON (as River would)
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	// Deserialize (as River would)
	var restored Args
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}

	if restored.Kind_ != "process.claim" {
		t.Fatalf("expected kind 'process.claim', got %q", restored.Kind_)
	}

	// Restore the value
	var claim Claim
	if err := json.Unmarshal(restored.Payload, &claim); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if claim.ID != "CLM-001" || claim.Amount != 485.00 {
		t.Fatalf("unexpected claim: %+v", claim)
	}

	// Restore meta
	meta := metaFromJSON(restored.Meta)
	if meta.Tags["source"] != "intake" {
		t.Fatalf("expected tag 'source'='intake', got %q", meta.Tags["source"])
	}
	if len(meta.Hints) != 1 || meta.Hints[0].Code != "validation.pending" {
		t.Fatalf("unexpected hints: %+v", meta.Hints)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	original := reflow.Meta{
		Hints: []reflow.Hint{
			{Code: "a", Message: "one", Span: "x"},
			{Code: "b", Message: "two", Span: ""},
		},
		Trace: []reflow.Step{
			{Node: "parse", Phase: "resolve", Status: "ok"},
			{Node: "ollama", Phase: "tool", Status: "ok"},
		},
		Tags: map[string]string{"env": "prod", "ver": "1"},
	}

	mj := metaToJSON(original)
	data, err := json.Marshal(mj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored metaJSON
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	meta := metaFromJSON(restored)
	if len(meta.Hints) != 2 {
		t.Fatalf("expected 2 hints, got %d", len(meta.Hints))
	}
	if len(meta.Trace) != 2 {
		t.Fatalf("expected 2 trace steps, got %d", len(meta.Trace))
	}
	if meta.Tags["env"] != "prod" {
		t.Fatalf("expected tag env=prod, got %q", meta.Tags["env"])
	}
}

func TestMetaFromJSONNilTags(t *testing.T) {
	mj := metaJSON{} // no tags
	meta := metaFromJSON(mj)
	if meta.Tags == nil {
		t.Fatal("expected non-nil tags map")
	}
}

// --- Router ---

func TestRouterDispatch(t *testing.T) {
	router := NewRouter()

	var processed string
	Handle(router, "echo", &reflow.Func[string, string]{
		ActFn: func(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
			processed = in.Value
			return in, nil
		},
	})

	payload, _ := json.Marshal("hello")
	meta := reflow.Meta{Tags: map[string]string{"k": "v"}}

	h, ok := router.handlers["echo"]
	if !ok {
		t.Fatal("expected handler for 'echo'")
	}

	err := h(context.Background(), payload, meta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed != "hello" {
		t.Fatalf("expected 'hello', got %q", processed)
	}
}

func TestRouterMultipleKinds(t *testing.T) {
	router := NewRouter()

	var aRan, bRan bool
	Handle(router, "kind-a", &reflow.Func[int, int]{
		ActFn: func(_ context.Context, in reflow.Envelope[int]) (reflow.Envelope[int], error) {
			aRan = true
			return in, nil
		},
	})
	Handle(router, "kind-b", &reflow.Func[int, int]{
		ActFn: func(_ context.Context, in reflow.Envelope[int]) (reflow.Envelope[int], error) {
			bRan = true
			return in, nil
		},
	})

	payload, _ := json.Marshal(42)
	meta := reflow.Meta{Tags: make(map[string]string)}

	router.handlers["kind-a"](context.Background(), payload, meta)
	if !aRan {
		t.Fatal("expected kind-a handler to run")
	}
	if bRan {
		t.Fatal("kind-b should not have run")
	}

	router.handlers["kind-b"](context.Background(), payload, meta)
	if !bRan {
		t.Fatal("expected kind-b handler to run")
	}
}

func TestRouterUnknownKind(t *testing.T) {
	router := NewRouter()
	worker := router.Worker()

	// We can't easily construct a *river.Job[Args] without River internals,
	// so test the router dispatch directly
	_, ok := router.handlers["nonexistent"]
	if ok {
		t.Fatal("should not have handler for 'nonexistent'")
	}

	// Verify the worker type is correct
	if worker.router != router {
		t.Fatal("worker should reference the router")
	}
}

func TestRouterPreservesMeta(t *testing.T) {
	router := NewRouter()

	var receivedTags map[string]string
	var receivedHints []reflow.Hint
	Handle(router, "meta-test", &reflow.Func[string, string]{
		ActFn: func(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
			receivedTags = in.Meta.Tags
			receivedHints = in.Meta.Hints
			return in, nil
		},
	})

	payload, _ := json.Marshal("data")
	meta := reflow.Meta{
		Tags:  map[string]string{"env": "test", "ver": "2"},
		Hints: []reflow.Hint{{Code: "prior", Message: "from upstream", Span: ""}},
	}

	err := router.handlers["meta-test"](context.Background(), payload, meta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedTags["env"] != "test" || receivedTags["ver"] != "2" {
		t.Fatalf("expected tags preserved, got %v", receivedTags)
	}
	if len(receivedHints) != 1 || receivedHints[0].Code != "prior" {
		t.Fatalf("expected hints preserved, got %v", receivedHints)
	}
}
