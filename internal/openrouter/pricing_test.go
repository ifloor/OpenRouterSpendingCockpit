package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeModelID(t *testing.T) {
	cases := map[string]string{
		"deepseek/deepseek-v4-flash":         "deepseek/deepseek-v4-flash",
		"~deepseek/deepseek-v4-flash-latest": "deepseek/deepseek-v4-flash-latest",
		"openai/gpt-4o:free":                 "openai/gpt-4o",
		"openai/gpt-4o:nitro":                "openai/gpt-4o",
		"  ~google/gemini-3.5-flash  ":       "google/gemini-3.5-flash",
	}
	for in, want := range cases {
		if got := NormalizeModelID(in); got != want {
			t.Errorf("NormalizeModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePrice(t *testing.T) {
	if got := parsePrice("0.00000095"); got != 0.00000095 {
		t.Errorf("parsePrice decimal = %v", got)
	}
	if got := parsePrice(""); got != 0 {
		t.Errorf("parsePrice empty = %v", got)
	}
	if got := parsePrice("bad"); got != 0 {
		t.Errorf("parsePrice bad = %v", got)
	}
}

func TestListModelsFallbacks(t *testing.T) {
	// Simulate the fallback rules applied in ListModels.
	p := ModelPricing{Prompt: 1, Completion: 2}
	if p.Cached != 0 {
		t.Errorf("Cached should default 0")
	}
	if p.Cached == 0 {
		p.Cached = p.Prompt
	}
	if p.Reasoning == 0 {
		p.Reasoning = p.Completion
	}
	if p.Cached != 1 || p.Reasoning != 2 {
		t.Errorf("fallbacks not applied: %+v", p)
	}
}

func TestModelsItemUnmarshalAllFields(t *testing.T) {
	// Every field of modelsItem must be populated from the /models payload;
	// unknown or null-valued fields must never break unmarshaling.
	var env modelsResponse
	if err := json.Unmarshal([]byte(`{"data":[{`+
		`"id":"meta/muse-spark-1.2-contributor",`+
		`"canonical_slug":"meta/muse-spark-1.2-contributor-20260805",`+
		`"hugging_face_id":null,`+
		`"name":"Meta: Muse Spark 1.2 Contributor",`+
		`"created":1787336476,`+
		`"description":"desc",`+
		`"context_length":1048576,`+
		`"architecture":{"modality":"text+image->text","input_modalities":["text","image"],"output_modalities":["text"],"tokenizer":"Other","instruct_type":null},`+
		`"pricing":{"prompt":"0.0000001","completion":"0.0000002","web_search":"0.0025","input_cache_read":"0.000000002","internal_reasoning":"0.0000003"},`+
		`"top_provider":{"context_length":1048576,"max_completion_tokens":null,"is_moderated":true},`+
		`"per_request_limits":null,`+
		`"supported_parameters":["max_tokens","temperature"],`+
		`"default_parameters":{},`+
		`"supported_voices":null,`+
		`"knowledge_cutoff":null,`+
		`"expiration_date":null,`+
		`"links":{"details":"/api/v1/models/meta/muse-spark-1.2-contributor-20260805/endpoints"},`+
		`"reasoning":{"mandatory":true,"supported_efforts":["xhigh","high"],"default_effort":"medium"}`+
		`}]}`), &env); err != nil {
		t.Fatalf("unmarshal modelsResponse: %v", err)
	}
	if len(env.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(env.Data))
	}
	it := env.Data[0]
	if it.ID != "meta/muse-spark-1.2-contributor" {
		t.Errorf("ID = %q", it.ID)
	}
	if it.CanonicalSlug != "meta/muse-spark-1.2-contributor-20260805" {
		t.Errorf("CanonicalSlug = %q", it.CanonicalSlug)
	}
	if it.Name != "Meta: Muse Spark 1.2 Contributor" {
		t.Errorf("Name = %q", it.Name)
	}
	if it.Created != 1787336476 {
		t.Errorf("Created = %d", it.Created)
	}
	if it.ContextLength != 1048576 {
		t.Errorf("ContextLength = %d", it.ContextLength)
	}
	if it.Architecture == nil || len(it.Architecture.InputModalities) != 2 {
		t.Fatalf("Architecture not populated: %+v", it.Architecture)
	}
	if it.TopProvider == nil || !it.TopProvider.IsModerated {
		t.Errorf("TopProvider not populated: %+v", it.TopProvider)
	}
	if len(it.SupportedParameters) != 2 {
		t.Errorf("SupportedParameters = %v", it.SupportedParameters)
	}
	if it.Links == nil || it.Links.Details == "" {
		t.Errorf("Links not populated: %+v", it.Links)
	}
	if it.Reasoning == nil || !it.Reasoning.Mandatory || it.Reasoning.DefaultEffort != "medium" {
		t.Errorf("Reasoning not populated: %+v", it.Reasoning)
	}
	// json.RawMessage fields hold the raw JSON "null" for a JSON null, and the
	// nullable scalars/pointers stay nil.
	if string(it.PerRequestLimits) != "null" || string(it.SupportedVoices) != "null" {
		t.Errorf("null RawMessage fields should hold JSON null, got per_request_limits=%v supported_voices=%v",
			it.PerRequestLimits, it.SupportedVoices)
	}
	if it.HuggingFaceID != nil || it.KnowledgeCutoff != nil || it.ExpirationDate != nil {
		t.Errorf("null-valued pointer fields should stay nil: %+v", it)
	}
	// The web_search price is part of the payload too now.
	if it.Pricing.WebSearch != "0.0025" {
		t.Errorf("Pricing.WebSearch = %q", it.Pricing.WebSearch)
	}
	if it.Pricing.InternalReasoning != "0.0000003" {
		t.Errorf("Pricing.InternalReasoning = %q", it.Pricing.InternalReasoning)
	}
}

func TestModelCatalogLookupCanonicalSlug(t *testing.T) {
	cat := &ModelCatalog{
		byModel: map[string]ModelPricing{
			"meta/muse-spark-1.2-contributor": {Prompt: 1, Completion: 2},
		},
		aliases: map[string]string{
			"meta/muse-spark-1.2-contributor-20260805": "meta/muse-spark-1.2-contributor",
		},
	}
	// A callback referencing either the stable id or the dated canonical slug
	// must resolve to the same price and canonical key.
	for _, ref := range []string{
		"meta/muse-spark-1.2-contributor",
		"meta/muse-spark-1.2-contributor-20260805",
	} {
		p, canonical, ok := cat.Lookup(ref)
		if !ok {
			t.Fatalf("Lookup(%q) not found", ref)
		}
		if p.Prompt != 1 || p.Completion != 2 {
			t.Errorf("Lookup(%q) = %+v", ref, p)
		}
		if canonical != "meta/muse-spark-1.2-contributor" {
			t.Errorf("Lookup(%q) canonical = %q, want stable base id", ref, canonical)
		}
	}
}

// TestListModelsIndexesCanonicalSlug verifies that a model can be looked up by
// its dated canonical_slug as well as by its stable base id.
func TestListModelsIndexesCanonicalSlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{
			"id":"meta/muse-spark-1.2-contributor",
			"canonical_slug":"meta/muse-spark-1.2-contributor-20260805",
			"name":"Meta: Muse Spark 1.2 Contributor",
			"pricing":{"prompt":"0.0000001","completion":"0.0000002","input_cache_read":"0.000000002"}
		}]}`))
	}))
	defer srv.Close()

	cat, err := NewClientWithBaseURL(srv.URL, "sk-test").ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if cat.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", cat.Len())
	}
	// The dated canonical slug must resolve to the same price as the base id.
	p, canonical, ok := cat.Lookup("meta/muse-spark-1.2-contributor-20260805")
	if !ok {
		t.Fatal("lookup by canonical_slug failed")
	}
	if canonical != "meta/muse-spark-1.2-contributor" {
		t.Errorf("canonical = %q, want stable base id", canonical)
	}
	if p.Prompt != 0.0000001 {
		t.Errorf("Prompt = %v", p.Prompt)
	}
	// The base id spelling must still resolve to the same price.
	if p2, _, _ := cat.Lookup("meta/muse-spark-1.2-contributor"); p2 != p {
		t.Errorf("id lookup = %+v, want %+v", p2, p)
	}
}
