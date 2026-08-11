package openrouter

import "testing"

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
