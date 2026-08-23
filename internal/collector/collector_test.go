package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/igor/openrouter-costwatch/internal/openrouter"
	"github.com/igor/openrouter-costwatch/internal/store"
)

type fakeAPI struct{}

const aggRow = `[{
  "date__minute": "2026-08-11T00:00:00Z",
  "model": "DeepSeek: DeepSeek V4 Flash",
  "provider": "DeepInfra",
  "request_count": 2,
  "total_usage": 0.000001,
  "tokens_total": 400
}]`

const drillRow = `[{
  "created_at__minute": "2026-08-11T00:00:00Z",
  "generation_id": "gen-1",
  "request_count": 1,
  "total_usage": 0.000001
}]`

const metaBody = `{"data":{"metrics":[{"name":"total_usage"},{"name":"request_count"},{"name":"tokens_total"},{"name":"tokens_prompt"},{"name":"tokens_completion"},{"name":"reasoning_tokens"}],"dimensions":["model","provider","generation_id"],"granularities":["minute"],"operators":["eq"]}}`

const creditsBody = `{"data":{"total_credits":10.0,"total_usage":1.0}}`

const modelsBody = `{"data":[{
  "id":"deepseek/deepseek-v4-flash",
  "name":"DeepSeek: DeepSeek V4 Flash",
  "pricing":{"prompt":"0.00000014","completion":"0.00000028","input_cache_read":"0.000000028"}
}]}`

// modelEndpointsBody is the per-provider pricing for the model used in the
// test, so the provider of the generation (DeepInfra) has a resolvable price.
const modelEndpointsBody = `{"data":{
  "id":"deepseek/deepseek-v4-flash",
  "endpoints":[
    {"provider_name":"DeepInfra","pricing":{"prompt":"0.00000014","completion":"0.00000028","input_cache_read":"0.000000028"}}
  ]
}}`

const genBody = `{"data":{
  "id":"gen-1",
  "provider_name":"DeepInfra",
  "model":"DeepSeek: DeepSeek V4 Flash",
  "model_permaslug":"deepseek/deepseek-v4-flash",
  "native_tokens_prompt":300,
  "native_tokens_completion":100,
  "native_tokens_reasoning":20,
  "native_tokens_cached":50,
  "total_cost":0.000001,
  "created_at":"2026-08-11T00:00:00Z"
}}`

func (fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(r.URL.Path, "/credits"):
		_, _ = w.Write([]byte(creditsBody))
	case strings.HasPrefix(r.URL.Path, "/analytics/meta"):
		_, _ = w.Write([]byte(metaBody))
	case strings.HasPrefix(r.URL.Path, "/analytics/query"):
		var body struct {
			Dimensions []string `json:"dimensions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Dimensions) > 0 && body.Dimensions[0] == "generation_id" {
			_, _ = w.Write([]byte(`{"data":{"data":` + drillRow + `,"cachedAt":0,"metadata":{}}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"data":` + aggRow + `,"cachedAt":0,"metadata":{}}}`))
		}
	case strings.HasPrefix(r.URL.Path, "/generation"):
		_, _ = w.Write([]byte(genBody))
	case strings.HasPrefix(r.URL.Path, "/models/") && strings.HasSuffix(r.URL.Path, "/endpoints"):
		_, _ = w.Write([]byte(modelEndpointsBody))
	case strings.HasPrefix(r.URL.Path, "/models"):
		_, _ = w.Write([]byte(modelsBody))
	default:
		http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
	}
}

func TestPricingFlowsToSnapshot(t *testing.T) {
	srv := httptest.NewServer(fakeAPI{})
	defer srv.Close()

	c := openrouter.NewClientWithBaseURL(srv.URL, "sk-test")
	st := store.New()
	col := New(c, st, 5*time.Second)

	ctx := context.Background()
	if err := col.Discover(ctx); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	col.poll(ctx)

	snap := st.Snapshot()
	if len(snap.Prices) == 0 {
		// the store is queried directly; get the enriched generation cost too
		t.Fatalf("expected >=1 price entry in snapshot, got %d (generations=%d, errors=%q)",
			len(snap.Prices), len(snap.Generations), snap.LastError)
	}
	var found *store.ModelPrice
	for i := range snap.Prices {
		if snap.Prices[i].Model == "deepseek/deepseek-v4-flash" {
			found = &snap.Prices[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("model deepseek/deepseek-v4-flash not found in prices: %+v", snap.Prices)
	}
	if found.Prompt == 0 || found.Completion == 0 || found.Cached == 0 {
		t.Fatalf("prices not parsed correctly: %+v", found)
	}

	// Sanity: the enriched generation should also carry computed costs.
	if len(snap.Generations) > 0 {
		g := snap.Generations[0]
		if !g.HasPricing {
			t.Fatalf("expected generation HasPricing=true: %+v", g)
		}
		if g.CostCalc <= 0 {
			t.Fatalf("expected CostCalc>0, got %v", g.CostCalc)
		}
	}
}
