package collector

import (
	"context"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/igor/openrouter-costwatch/internal/openrouter"
	"github.com/igor/openrouter-costwatch/internal/store"
)

// Collector polls the OpenRouter API on a ticker and feeds the store.
type Collector struct {
	client   *openrouter.Client
	store    *store.Store
	interval time.Duration

	metrics           []string
	dimModel          string
	dimProvider       string
	dimGeneration     string
	providerSupported bool

	// throttle enrichment per tick
	maxEnrichPerTick int

	// price catalog, lazily fetched from /models on first use. Owned by the
	// collector's poll goroutine together with missLogged, so no lock needed.
	catalog    *openrouter.ModelCatalog
	missLogged map[string]bool
}

// New builds a collector. metricsNames and dimensions are ignored here and
// discovered from /analytics/meta at boot (see Discover).
func New(client *openrouter.Client, st *store.Store, interval time.Duration) *Collector {
	return &Collector{
		client:           client,
		store:            st,
		interval:         interval,
		dimModel:         "model",
		dimProvider:      "provider",
		dimGeneration:    "generation_id",
		maxEnrichPerTick: 5,
		missLogged:       map[string]bool{},
	}
} // Discover calls /analytics/meta, validates configured names and records a
// summary in the store. Called once before the poll loop.
func (c *Collector) Discover(ctx context.Context) error {
	meta, err := c.client.GetMeta(ctx)
	if err != nil {
		c.store.SetMeta(false, err.Error())
		return err
	}

	defaults := []string{"total_usage", "request_count", "tokens_total", "tokens_prompt", "tokens_completion", "reasoning_tokens"}
	have := map[string]bool{}
	for _, m := range meta.Metrics {
		have[m] = true
	}
	c.metrics = nil
	for _, m := range defaults {
		if have[m] {
			c.metrics = append(c.metrics, m)
		}
	}
	if len(c.metrics) == 0 {
		c.metrics = meta.Metrics
	}

	dimHave := map[string]bool{}
	for _, d := range meta.Dimensions {
		dimHave[d] = true
	}
	if !dimHave[c.dimModel] {
		c.dimModel = "model"
	}
	c.providerSupported = dimHave[c.dimProvider]

	summary := "metrics: [" + join(c.metrics) + "]; dims: [" + join(meta.Dimensions) +
		"]; granularities: [" + join(meta.Granularities) + "]; ops: [" + join(meta.Operators) + "]"
	c.store.SetMeta(true, summary)
	log.Printf("analytics meta OK: %s", summary)
	return nil
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// Run blocks, polling on the configured interval.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// Do an immediate first poll.
	c.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.poll(ctx)
		}
	}
}

func (c *Collector) poll(ctx context.Context) {
	// 1. Balance
	if bal, err := c.client.GetCredits(ctx); err == nil {
		c.store.SetBalance(store.Balance{
			Bought:    bal.TotalCredits,
			Used:      bal.TotalUsage,
			Remaining: bal.TotalCredits - bal.TotalUsage,
		})
	} else {
		c.recordError("credits", err)
	}

	// 2. Aggregate
	c.pollAggregate(ctx)

	// 3. Drilldown
	c.pollDrilldown(ctx)

	// 4. Bump store version for SSE clients
	c.store.Tick()
}

func (c *Collector) recordError(kind string, err error) {
	if apiErr, ok := err.(*openrouter.APIError); ok {
		c.store.SetError(kind + ": HTTP " + strconv.Itoa(apiErr.StatusCode))
		return
	}
	c.store.SetError(kind + ": " + err.Error())
}

func (c *Collector) lastMinutes() (string, string) {
	now := time.Now().UTC()
	end := now.Add(2 * time.Minute)
	start := now.Add(-5 * time.Minute)
	return start.Format(time.RFC3339), end.Format(time.RFC3339)
}

func (c *Collector) pollAggregate(ctx context.Context) {
	dims := []string{c.dimModel}
	if c.providerSupported {
		dims = append(dims, c.dimProvider)
	}
	start, end := c.lastMinutes()
	q := &openrouter.AnalyticsQuery{
		Metrics:     c.metrics,
		Dimensions:  dims,
		Granularity: "minute",
		TimeRange:   &openrouter.TimeRange{Start: start, End: end},
	}
	resp, err := c.client.QueryAnalytics(ctx, q)
	if err != nil {
		c.recordError("analytics(agg)", err)
		return
	}

	c.store.SetWarning(resp.Truncated())
	c.store.SetCachedAt("aggregate", resp.CachedAtTime())
	c.store.SetError("")

	rows := make([]store.AggRow, 0, len(resp.Data.Rows))
	for _, row := range resp.Data.Rows {
		_, minute, ok := openrouter.RowTimeKey(row)
		if !ok {
			continue
		}
		model := strOf(row, c.dimModel)
		provider := ""
		if c.providerSupported {
			provider = strOf(row, c.dimProvider)
		}
		rows = append(rows, store.AggRow{
			Minute:   minute,
			Model:    model,
			Provider: provider,
			Requests: int64(openrouter.RowValue(row, "request_count")),
			Cost:     openrouter.RowValue(row, "total_usage"),
			Tokens:   int64(openrouter.RowValue(row, "tokens_total")),
		})
	}
	c.store.UpdateAggregate(rows)

	// Register the price for each (model, provider) actually used in this
	// window, so the price table fills in from real usage (not the whole
	// catalog). A model may appear multiple times, once per provider.
	seen := map[string]bool{}
	for _, r := range rows {
		key := r.Model + "\x00" + r.Provider
		if r.Model == "" || seen[key] {
			continue
		}
		seen[key] = true
		c.pricingFor(ctx, r.Model, r.Provider)
	}

	c.updateRecent(rows)
}

func strOf(row map[string]any, key string) string {
	if v, ok := row[key]; ok {
		if s, isStr := v.(string); isStr {
			return s
		}
		return ""
	}
	return ""
}

func (c *Collector) updateRecent(rows []store.AggRow) {
	// Last-5-min rows are already scoped; sort by cost desc, cap in store.
	hits := make([]store.RecentHit, len(rows))
	for i, r := range rows {
		hits[i] = store.RecentHit{
			Minute: r.Minute, Model: r.Model, Provider: r.Provider,
			Requests: r.Requests, Cost: r.Cost, Tokens: r.Tokens,
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].Cost > hits[b].Cost })
	c.store.SetRecent(hits)
}

func (c *Collector) pollDrilldown(ctx context.Context) {
	start, end := c.lastMinutes()
	q := &openrouter.AnalyticsQuery{
		Metrics:    c.metrics,
		Dimensions: []string{c.dimGeneration},
		TimeRange:  &openrouter.TimeRange{Start: start, End: end},
	}
	resp, err := c.client.QueryAnalytics(ctx, q)
	if err != nil {
		c.recordError("analytics(gen)", err)
		return
	}

	// Collect candidate generation IDs.
	var ids []string
	seen := map[string]bool{}
	for _, row := range resp.Data.Rows {
		id, ok := row[c.dimGeneration].(string)
		if !ok || id == "" {
			continue
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	c.store.SetCachedAt("generations", resp.CachedAtTime())

	// Enrich a limited number of new IDs per tick.
	enriched := 0
	for _, id := range ids {
		if enriched >= c.maxEnrichPerTick {
			break
		}
		g, err := c.client.GetGeneration(ctx, id)
		if err != nil {
			continue // skip; retry on a later tick
		}
		gi := c.toGenerationInfo(g)
		c.applyPricing(ctx, gi)
		gi.IsNew = !c.store.GenerationSeen(id)
		if c.store.AddGeneration(gi) {
			enriched++
		}
	}
}

// ensureCatalog loads the /models price catalog at most once. Errors are not
// cached, so a transient failure retries on the next poll tick.
func (c *Collector) ensureCatalog(ctx context.Context) error {
	if c.catalog != nil {
		return nil
	}
	cat, err := c.client.ListModels(ctx)
	if err != nil {
		return err
	}
	c.catalog = cat
	log.Printf("pricing: loaded %d model prices from /models", cat.Len())
	return nil
}

// pricingFor resolves the per-token price for a (model, provider) pair,
// loading the catalog on first use. The pair is registered with the store —
// with Found=false when the price could not be resolved — so the price table
// reflects real usage (model x provider) and surfaces unknowns explicitly. ok
// is true only when a real price was found for the provider.
func (c *Collector) pricingFor(ctx context.Context, model, provider string) (store.ModelPrice, bool) {
	if err := c.ensureCatalog(ctx); err != nil {
		log.Printf("pricing: catalog fetch failed: %v", err)
		return store.ModelPrice{}, false
	}
	canonical := c.catalog.Canonical(model)
	if canonical == "" {
		canonical = model
	}
	sp := store.ModelPrice{Model: canonical, Provider: provider}
	if provider == "" {
		// Without a provider there is no reliable way to price the model (the
		// catalog prices per provider); skip rather than record a false miss.
		return store.ModelPrice{}, false
	}
	if p, ok := c.catalog.ProviderPrice(ctx, model, provider); ok {
		sp.Found = true
		sp.Prompt = p.Prompt
		sp.Cached = p.Cached
		sp.Completion = p.Completion
		sp.Reasoning = p.Reasoning
		c.store.SetPrice(sp)
		return sp, true
	}
	// Explicitly surface a missing/unresolvable price for a used model x
	// provider instead of hiding it.
	c.store.SetPrice(sp)
	c.logPricingMiss(model, provider)
	return store.ModelPrice{}, false
}

// logPricingMiss logs (once per model+provider) a lookup that did not match
// any provider price, so unknowns stay visible without spamming every tick.
func (c *Collector) logPricingMiss(model, provider string) {
	k := model + "\x00" + provider
	if c.missLogged[k] {
		return
	}
	c.missLogged[k] = true
	log.Printf("pricing: no price found for %q (provider %q)", model, provider)
}

// applyPricing computes the per-column and total costs for a generation based
// on the per-token price of its model+provider. It prefers the canonical slug
// from the drilldown (/generation model_permaslug) since the generation's
// display `model` is a human label that may not match the catalog directly,
// and prices by the generation's provider.
func (c *Collector) applyPricing(ctx context.Context, gi *store.GenerationInfo) {
	id := gi.ModelPermaSlug
	if id == "" || id == gi.Model {
		id = gi.Model
	}
	p, ok := c.pricingFor(ctx, id, gi.Provider)
	if !ok {
		return
	}
	gi.HasPricing = true
	// The "In" column already excludes cached tokens, so costs match display.
	gi.CostInCached = float64(gi.TokensCached) * p.Cached
	gi.CostIn = float64(gi.TokensPrompt-gi.TokensCached) * p.Prompt
	gi.CostReasoning = float64(gi.TokensReasoning) * p.Reasoning
	gi.CostOut = float64(gi.TokensCompletion) * p.Completion
	gi.CostCalc = gi.CostInCached + gi.CostIn + gi.CostReasoning + gi.CostOut
}

func (c *Collector) toGenerationInfo(g *openrouter.Generation) *store.GenerationInfo {
	currentTime := time.Now().UTC()
	if generationTime := openrouter.ParseGenerationTime(g.CreatedAt); !generationTime.IsZero() {
		currentTime = generationTime
	}
	nativeTokensPrompt := g.NativeTokensPrompt
	if nativeTokensPrompt == 0 && g.TokensPrompt != 0 {
		nativeTokensPrompt = g.TokensPrompt
	}
	nativeTokensCompletion := g.NativeTokensCompletion
	if nativeTokensCompletion == 0 && g.TokensCompletion != 0 {
		nativeTokensCompletion = g.TokensCompletion
	}
	return &store.GenerationInfo{
		ID:               g.ID,
		CreatedAt:        currentTime,
		Model:            g.Model,
		ModelPermaSlug:   g.ModelPermaSlug,
		Provider:         g.ProviderName,
		TokensPrompt:     nativeTokensPrompt,
		TokensCompletion: nativeTokensCompletion - g.NativeTokensReasoning,
		TokensReasoning:  g.NativeTokensReasoning,
		TokensCached:     g.NativeTokensCached,
		Cost:             g.TotalCost,
		Latency:          g.Latency,
		FinishReason:     g.FinishReason,
	}
}
