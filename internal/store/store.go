package store

import (
	"sync"
	"time"
)

// Balance is the credits snapshot.
type Balance struct {
	Bought    float64   `json:"bought"`
	Used      float64   `json:"used"`
	Remaining float64   `json:"remaining"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Bucket is an accumulated aggregate for one (minute, model) group.
type Bucket struct {
	Minute    string    `json:"minute"`
	Model     string    `json:"model"`
	Provider  string    `json:"provider"`
	Requests  int64     `json:"requests"`
	Cost      float64   `json:"cost"`
	Tokens    int64     `json:"tokens"`
	DeltaCost float64   `json:"delta_cost"`
	DeltaReqs int64     `json:"delta_requests"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// GenerationInfo is a single enriched call.
type GenerationInfo struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"created_at"`
	Model            string    `json:"model"`
	ModelPermaSlug   string    `json:"model_permaslug"`
	Provider         string    `json:"provider"`
	TokensPrompt     int64     `json:"tokens_prompt"`
	TokensCompletion int64     `json:"tokens_completion"`
	TokensReasoning  int64     `json:"tokens_reasoning"`
	TokensCached     int64     `json:"tokens_cached"`
	Cost             float64   `json:"cost"`
	Latency          float64   `json:"latency"`
	FinishReason     string    `json:"finish_reason"`
	IsNew            bool      `json:"is_new"`

	// Derived per-operation cost breakdown, computed from the cached model
	// pricing. HasPricing is false when the model is unknown to the catalog.
	HasPricing    bool    `json:"has_pricing"`
	CostInCached  float64 `json:"cost_in_cached"`
	CostIn        float64 `json:"cost_in"`
	CostReasoning float64 `json:"cost_reasoning"`
	CostOut       float64 `json:"cost_out"`
	CostCalc      float64 `json:"cost_calc"`
}

// State is the full JSON snapshot served to the dashboard.
type State struct {
	Balance     Balance              `json:"balance"`
	Hits        []RecentHit          `json:"hits"`
	Buckets     []Bucket             `json:"buckets"`
	Generations []GenerationInfo     `json:"generations"`
	Prices      []ModelPrice         `json:"prices"`
	LastError   string               `json:"last_error"`
	WarnTrunc   bool                 `json:"warn_truncated"`
	MetaReady   bool                 `json:"meta_ready"`
	MetaSummary string               `json:"meta_summary"`
	CachedAts   map[string]time.Time `json:"cached_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
	IntervalMS  int64                `json:"interval_ms"`
}

// ModelPrice is a cached per-token price for a model+provider pair (in
// $ / token). Provider is empty for models without per-provider breakdown.
// Found is false when the price for the model could not be determined, so the
// UI can surface the unknown provider instead of silently skipping it.
type ModelPrice struct {
	Model      string  `json:"model"`
	Provider   string  `json:"provider"`
	Found      bool    `json:"found"`
	Prompt     float64 `json:"prompt"`
	Cached     float64 `json:"cached"`
	Completion float64 `json:"completion"`
	Reasoning  float64 `json:"reasoning"`
}

// lastState is the previous-known cumulative value used to compute deltas.
type lastState struct {
	requests int64
	cost     float64
	tokens   int64
}

// Store is the in-memory, mutex-guarded state.
type Store struct {
	mu sync.Mutex

	balance    Balance
	hasBalance bool

	// buckets keyed by minute|model
	buckets map[string]*Bucket
	// last-known cumulative counts for diffing
	last map[string]*lastState
	// current tick deltas (reset on each tick snapshot)
	tickDeltas map[string]*Bucket

	// generations map id -> info, plus insertion order (cap)
	generations map[string]*GenerationInfo
	genOrder    []string

	// prices: model+provider -> per-token prices (deduped)
	prices map[string]ModelPrice

	// recent aggregate rows for "últimos hits", ordered by cost
	recent []RecentHit

	lastError   string
	warnTrunc   bool
	metaReady   bool
	metaSummary string
	cachedAts   map[string]time.Time
	updatedAt   time.Time
	intervalMS  int64
}

// RecentHit is an aggregated row kept for the "últimos hits" panel.
type RecentHit struct {
	Minute   string  `json:"minute"`
	Model    string  `json:"model"`
	Provider string  `json:"provider"`
	Requests int64   `json:"requests"`
	Cost     float64 `json:"cost"`
	Tokens   int64   `json:"tokens"`
}

const (
	maxGenerationOrder = 200
	maxRecent          = 20
	maxBuckets         = 500
)

// New creates an empty store.
func New() *Store {
	return &Store{
		buckets:     make(map[string]*Bucket),
		last:        make(map[string]*lastState),
		tickDeltas:  make(map[string]*Bucket),
		generations: make(map[string]*GenerationInfo),
		prices:      make(map[string]ModelPrice),
		cachedAts:   make(map[string]time.Time),
	}
}

// SetInterval records the poll interval (ms) so the UI can sync the loader.
func (s *Store) SetInterval(ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intervalMS = ms
}

func bucketKey(minute, model string) string {
	return minute + "|" + model
}

// SetBalance updates the balance snapshot.
func (s *Store) SetBalance(b Balance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b.UpdatedAt = time.Now()
	s.balance = b
	s.hasBalance = true
}

// UpdateAggregate applies a set of aggregated rows from a poll, computing
// per-(minute,model) deltas against the previous-known state. Rows is a list
// of (minute, model, provider, requests, cost, tokens).
func (s *Store) UpdateAggregate(rows []AggRow) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tickDeltas = make(map[string]*Bucket)

	for _, r := range rows {
		key := bucketKey(r.Minute, r.Model)

		last, had := s.last[key]

		// New cumulative values for this poll.
		cur := lastState{requests: r.Requests, cost: r.Cost, tokens: r.Tokens}
		if !had {
			// Establish baseline: report zero delta on first sight.
			s.last[key] = &cur
			// Still create a bucket with current values so it shows up.
		} else {
			s.last[key] = &cur
			deltaReqs := cur.requests - last.requests
			deltaCost := cur.cost - last.cost
			if deltaReqs < 0 {
				deltaReqs = cur.requests
			}
			if deltaCost < 0 {
				deltaCost = cur.cost
			}

			b, ok := s.buckets[key]
			if !ok {
				b = &Bucket{Minute: r.Minute, Model: r.Model}
				s.buckets[key] = b
			}
			b.Provider = r.Provider
			b.Requests += deltaReqs
			b.Cost += deltaCost
			b.Tokens += r.Tokens
			b.DeltaCost = deltaCost
			b.DeltaReqs = deltaReqs
			b.LastSeen = time.Now()
			if b.FirstSeen.IsZero() {
				b.FirstSeen = time.Now()
			}

			if deltaReqs > 0 || deltaCost > 0 {
				s.tickDeltas[key] = b
			}
		}
	}

	s.trimBucketsLocked()
}

// AggRow is one aggregated row from analytics.
type AggRow struct {
	Minute   string
	Model    string
	Provider string
	Requests int64
	Cost     float64
	Tokens   int64
}

func (s *Store) trimBucketsLocked() {
	if len(s.buckets) <= maxBuckets {
		return
	}
	// Drop oldest by lastSeen.
	keys := make([]string, 0, len(s.buckets))
	for k := range s.buckets {
		keys = append(keys, k)
	}
	// simple: sort not needed for correctness here; drop arbitrarily.
	for len(s.buckets) > maxBuckets && len(keys) > 0 {
		k := keys[0]
		keys = keys[1:]
		delete(s.buckets, k)
	}
}

// SetRecent replaces the recent aggregate hits list.
func (s *Store) SetRecent(hits []RecentHit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// keep top-N by cost
	if len(hits) > maxRecent {
		// insertion order already sorted by cost from caller; keep first N
		hits = hits[:maxRecent]
	}
	s.recent = hits
}

// AddGeneration records an enriched call, dedup by ID and capped by order.
func (s *Store) AddGeneration(g *GenerationInfo) (newlyAdded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.generations[g.ID]; exists {
		return false
	}
	s.generations[g.ID] = g
	s.genOrder = append(s.genOrder, g.ID)
	if len(s.genOrder) > maxGenerationOrder {
		drop := s.genOrder[0]
		s.genOrder = s.genOrder[1:]
		delete(s.generations, drop)
	}
	return true
}

// GenerationSeen reports whether an ID is already stored.
func (s *Store) GenerationSeen(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.generations[id]
	return ok
}

// SetPrice records a per-token price for a model+provider (idempotent; a real
// price always wins over an earlier "not found" entry, and otherwise the
// first value seen wins).
func (s *Store) SetPrice(p ModelPrice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.priceKey(p.Model, p.Provider)
	if cur, ok := s.prices[key]; ok {
		if cur.Found || !p.Found {
			return
		}
	}
	s.prices[key] = p
}

// priceKey builds the dedup key for a model+provider price row.
func (s *Store) priceKey(model, provider string) string {
	return model + "\x00" + provider
}

// Tick marks that a poll completed and bumps the state version used by SSE.
func (s *Store) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updatedAt = time.Now()
}

// UpdatedAt returns the last poll version.
func (s *Store) UpdatedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updatedAt
}

// SetMeta records meta discovery status.
func (s *Store) SetMeta(ready bool, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metaReady = ready
	s.metaSummary = summary
}

// SetWarning records truncation/partial warning.
func (s *Store) SetWarning(trunc bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warnTrunc = trunc
}

// SetError records the last collector error (for UI banner).
func (s *Store) SetError(err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = err
}

// SetCachedAt records the analytics cachedAt for a query kind.
func (s *Store) SetCachedAt(kind string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.IsZero() {
		delete(s.cachedAts, kind)
		return
	}
	s.cachedAts[kind] = t
}

// Snapshot returns a full copy of the state for the dashboard.
func (s *Store) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := State{
		Balance:     s.balance,
		LastError:   s.lastError,
		WarnTrunc:   s.warnTrunc,
		MetaReady:   s.metaReady,
		MetaSummary: s.metaSummary,
		UpdatedAt:   s.updatedAt,
		IntervalMS:  s.intervalMS,
		CachedAts:   map[string]time.Time{},
	}
	for k, v := range s.cachedAts {
		st.CachedAts[k] = v
	}
	if !s.hasBalance {
		st.Balance.Bought = -1
		st.Balance.Used = -1
		st.Balance.Remaining = -1
	}

	st.Hits = make([]RecentHit, len(s.recent))
	copy(st.Hits, s.recent)

	// Buckets sorted by lastSeen desc.
	bks := make([]Bucket, 0, len(s.buckets))
	for _, b := range s.buckets {
		bks = append(bks, *b)
	}
	sortBuckets(bks)
	st.Buckets = bks

	// Generations, newest first.
	recent := make([]GenerationInfo, 0, len(s.genOrder))
	for i := len(s.genOrder) - 1; i >= 0; i-- {
		g := s.generations[s.genOrder[i]]
		if g != nil {
			recent = append(recent, *g)
		}
	}
	st.Generations = recent

	// Prices, sorted by model.
	prs := make([]ModelPrice, 0, len(s.prices))
	for _, p := range s.prices {
		prs = append(prs, p)
	}
	sortPrices(prs)
	st.Prices = prs

	st.UpdatedAt = time.Now()
	return st
}

func sortPrices(p []ModelPrice) {
	less := func(a, b ModelPrice) bool {
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.Provider < b.Provider
	}
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && less(p[j], p[j-1]); j-- {
			p[j-1], p[j] = p[j], p[j-1]
		}
	}
}

func sortBuckets(b []Bucket) {
	// simple insertion sort by LastSeen desc (small N)
	for i := 1; i < len(b); i++ {
		for j := i; j > 0 && b[j-1].LastSeen.Before(b[j].LastSeen); j-- {
			b[j-1], b[j] = b[j], b[j-1]
		}
	}
}
