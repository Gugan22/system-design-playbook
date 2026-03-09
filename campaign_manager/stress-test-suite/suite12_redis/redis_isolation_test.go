// Package suite12_redis tests that the three Redis clusters fail independently.
//
// Design source: PLATFORM_DESIGN.md §2 (Three Isolated Redis Clusters)
//
// The design splits Redis into three clusters with separate failure domains:
//   - REDIS_DEDUP  — L3 duplicate confirmation store. Fail-open on miss.
//   - REDIS_STATE  — Saga step tracking. Rebuilds from Spanner on failure.
//   - REDIS_RATE   — Token bucket rate limiting. Resets to conservative defaults on failure.
//
// The risk being tested: if the three clusters share any code path, connection
// pool, or state, a failure in REDIS_RATE could cascade into REDIS_DEDUP —
// turning a rate limiter outage into a silent duplicate-send incident.
//
// Tests:
//   ST-REDIS1: REDIS_RATE fails — dedup unaffected, rate falls to conservative defaults.
//   ST-REDIS2: REDIS_STATE fails — dedup and rate unaffected, saga rebuilds from Spanner.
//   ST-REDIS3: REDIS_DEDUP (L3) fails — fail-open fires, rate and saga unaffected.
//   ST-REDIS4: All three fail simultaneously — each policy fires independently, no cross-contamination.
package suite12_redis

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================
// REDIS_DEDUP — L3 confirmed-duplicate store
// ============================================================

var ErrDedupClusterDown = errors.New("REDIS_DEDUP: cluster unavailable")

type RedisDedupCluster struct {
	mu      sync.RWMutex
	records map[string]string
	alive   atomic.Bool

	hits     atomic.Int64
	misses   atomic.Int64
	failOpen atomic.Int64
}

func NewRedisDedupCluster() *RedisDedupCluster {
	c := &RedisDedupCluster{records: make(map[string]string)}
	c.alive.Store(true)
	return c
}

func (c *RedisDedupCluster) Confirm(msgID, originalID string) error {
	if !c.alive.Load() {
		return ErrDedupClusterDown
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records[msgID] = originalID
	return nil
}

// IsDuplicate returns (true, nil) on hit, (false, nil) on miss, (false, err) on failure.
// Callers treat failure as fail-open: allow the send, log the risk.
func (c *RedisDedupCluster) IsDuplicate(msgID string) (bool, error) {
	if !c.alive.Load() {
		c.failOpen.Add(1)
		return false, ErrDedupClusterDown
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.records[msgID]; ok {
		c.hits.Add(1)
		return true, nil
	}
	c.misses.Add(1)
	return false, nil
}

func (c *RedisDedupCluster) SimulateFailure() { c.alive.Store(false) }
func (c *RedisDedupCluster) Restore()         { c.alive.Store(true) }

// ============================================================
// REDIS_STATE — saga step tracking
// On failure: saga orchestrator rebuilds from Spanner (Campaign Status DB).
// ============================================================

var ErrStateClusterDown = errors.New("REDIS_STATE: cluster unavailable")

type SagaStepState struct {
	StepIndex int
	Status    string
	UpdatedAt time.Time
}

type RedisStateCluster struct {
	mu    sync.RWMutex
	state map[string]*SagaStepState
	alive atomic.Bool

	reads  atomic.Int64
	writes atomic.Int64
}

func NewRedisStateCluster() *RedisStateCluster {
	c := &RedisStateCluster{state: make(map[string]*SagaStepState)}
	c.alive.Store(true)
	return c
}

func (c *RedisStateCluster) SetStep(sagaID string, step *SagaStepState) error {
	if !c.alive.Load() {
		return ErrStateClusterDown
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state[sagaID] = step
	c.writes.Add(1)
	return nil
}

func (c *RedisStateCluster) GetStep(sagaID string) (*SagaStepState, error) {
	if !c.alive.Load() {
		return nil, ErrStateClusterDown
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.reads.Add(1)
	return c.state[sagaID], nil
}

func (c *RedisStateCluster) SimulateFailure() { c.alive.Store(false) }
func (c *RedisStateCluster) Restore()         { c.alive.Store(true) }

// SpannerRebuild is the durable fallback for saga step state.
type SpannerRebuild struct {
	mu    sync.RWMutex
	steps map[string]*SagaStepState
}

func NewSpannerRebuild() *SpannerRebuild {
	return &SpannerRebuild{steps: make(map[string]*SagaStepState)}
}

func (s *SpannerRebuild) Checkpoint(sagaID string, step *SagaStepState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps[sagaID] = step
}

func (s *SpannerRebuild) Rebuild(sagaID string) *SagaStepState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.steps[sagaID]
}

// ============================================================
// REDIS_RATE — token bucket rate limiting
// On failure: conservative in-process fallback (10 tokens/sec vs 1000 normal).
// ============================================================

var ErrRateClusterDown = errors.New("REDIS_RATE: cluster unavailable")

const (
	NormalRatePerSec       = 1000.0
	ConservativeRatePerSec = 10.0
)

type RateBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64
	lastRefill time.Time
	mu         sync.Mutex
}

func newRateBucket(ratePerSec float64, burst int) *RateBucket {
	return &RateBucket{
		tokens:     float64(burst),
		maxTokens:  float64(burst),
		refillRate: ratePerSec / 1e9,
		lastRefill: time.Now(),
	}
}

func (b *RateBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += float64(now.Sub(b.lastRefill).Nanoseconds()) * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now
	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}
	return false
}

type RedisRateCluster struct {
	mu      sync.RWMutex
	buckets map[string]*RateBucket
	alive   atomic.Bool

	allowed  atomic.Int64
	rejected atomic.Int64
	fallback atomic.Int64

	conservativeMu      sync.RWMutex
	conservativeBuckets map[string]*RateBucket
}

func NewRedisRateCluster() *RedisRateCluster {
	c := &RedisRateCluster{
		buckets:             make(map[string]*RateBucket),
		conservativeBuckets: make(map[string]*RateBucket),
	}
	c.alive.Store(true)
	return c
}

func (c *RedisRateCluster) Allow(key string) bool {
	if !c.alive.Load() {
		c.fallback.Add(1)
		return c.conservativeAllow(key)
	}
	c.mu.Lock()
	b, ok := c.buckets[key]
	if !ok {
		b = newRateBucket(NormalRatePerSec, 100)
		c.buckets[key] = b
	}
	c.mu.Unlock()
	if b.Allow() {
		c.allowed.Add(1)
		return true
	}
	c.rejected.Add(1)
	return false
}

func (c *RedisRateCluster) conservativeAllow(key string) bool {
	c.conservativeMu.Lock()
	b, ok := c.conservativeBuckets[key]
	if !ok {
		b = newRateBucket(ConservativeRatePerSec, 10)
		c.conservativeBuckets[key] = b
	}
	c.conservativeMu.Unlock()
	return b.Allow()
}

func (c *RedisRateCluster) SimulateFailure() { c.alive.Store(false) }
func (c *RedisRateCluster) Restore()         { c.alive.Store(true) }

// ============================================================
// Platform — wires all three clusters together
// ============================================================

type Platform struct {
	dedup   *RedisDedupCluster
	state   *RedisStateCluster
	rate    *RedisRateCluster
	spanner *SpannerRebuild
}

func NewPlatform() *Platform {
	return &Platform{
		dedup:   NewRedisDedupCluster(),
		state:   NewRedisStateCluster(),
		rate:    NewRedisRateCluster(),
		spanner: NewSpannerRebuild(),
	}
}

// Send returns (sent, dedupFailOpen, rateFallback).
func (p *Platform) Send(tenantID, msgID, providerKey string) (sent, dedupFailOpen, rateFallback bool) {
	rateFallback = !p.rate.alive.Load()
	if !p.rate.Allow(fmt.Sprintf("%s:%s", tenantID, providerKey)) {
		return false, false, rateFallback
	}
	isDup, err := p.dedup.IsDuplicate(msgID)
	if err != nil {
		return true, true, rateFallback
	}
	if isDup {
		return false, false, rateFallback
	}
	_ = p.dedup.Confirm(msgID, msgID+"-original")
	return true, false, rateFallback
}

func (p *Platform) SetSagaStep(sagaID string, step *SagaStepState) {
	// Dual-write: Spanner is the durable source of truth, Redis is the fast cache.
	// Spanner is checkpointed first so that if Redis fails, Spanner is already current.
	p.spanner.Checkpoint(sagaID, step)
	_ = p.state.SetStep(sagaID, step)
}

func (p *Platform) GetSagaStep(sagaID string) *SagaStepState {
	step, err := p.state.GetStep(sagaID)
	if err != nil {
		return p.spanner.Rebuild(sagaID)
	}
	return step
}

// ============================================================
// ST-REDIS1: REDIS_RATE fails — dedup unaffected, conservative fallback applies
// ============================================================

func TestST_REDIS1_RateFailure_DedupUnaffected(t *testing.T) {
	p := NewPlatform()

	const seedCount = 1_000
	for i := 0; i < seedCount; i++ {
		_ = p.dedup.Confirm(fmt.Sprintf("known-dup-%d", i), fmt.Sprintf("orig-%d", i))
	}

	p.rate.SimulateFailure()
	t.Logf("ST-REDIS1: REDIS_RATE DOWN")

	var dedupHits, dedupFail atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < seedCount; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			isDup, err := p.dedup.IsDuplicate(fmt.Sprintf("known-dup-%d", i))
			if err != nil || !isDup {
				dedupFail.Add(1)
			} else {
				dedupHits.Add(1)
			}
		}()
	}
	wg.Wait()

	t.Logf("ST-REDIS1: dedup hits=%d fail=%d", dedupHits.Load(), dedupFail.Load())

	if dedupFail.Load() > 0 {
		t.Errorf("ST-REDIS1 FAIL: %d dedup checks failed — REDIS_RATE failure contaminated REDIS_DEDUP", dedupFail.Load())
	}
	if dedupHits.Load() != seedCount {
		t.Errorf("ST-REDIS1 FAIL: expected %d hits, got %d", seedCount, dedupHits.Load())
	}

	// Rate must use conservative fallback, not error out
	for i := 0; i < 20; i++ {
		_ = p.rate.Allow(fmt.Sprintf("tenant-%d:fcm", i))
	}
	if p.rate.fallback.Load() == 0 {
		t.Errorf("ST-REDIS1 FAIL: rate cluster down but conservative fallback never invoked")
	}
	t.Logf("ST-REDIS1: rate fallback invocations=%d", p.rate.fallback.Load())
	t.Logf("ST-REDIS1 PASS: REDIS_RATE failure isolated — REDIS_DEDUP fully operational")
}

// ============================================================
// ST-REDIS2: REDIS_STATE fails — dedup and rate unaffected, saga rebuilds from Spanner
// ============================================================

func TestST_REDIS2_StateFailure_DedupAndRateUnaffected(t *testing.T) {
	p := NewPlatform()

	const sagaCount = 500
	for i := 0; i < sagaCount; i++ {
		p.SetSagaStep(fmt.Sprintf("saga-%d", i), &SagaStepState{
			StepIndex: 2, Status: "succeeded", UpdatedAt: time.Now(),
		})
	}

	const dedupSeed = 300
	for i := 0; i < dedupSeed; i++ {
		_ = p.dedup.Confirm(fmt.Sprintf("dedup-%d", i), fmt.Sprintf("orig-%d", i))
	}

	p.state.SimulateFailure()
	t.Logf("ST-REDIS2: REDIS_STATE DOWN")

	// Dedup must be unaffected
	var dedupOK, dedupFail atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < dedupSeed; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			isDup, err := p.dedup.IsDuplicate(fmt.Sprintf("dedup-%d", i))
			if err != nil || !isDup {
				dedupFail.Add(1)
			} else {
				dedupOK.Add(1)
			}
		}()
	}
	wg.Wait()

	if dedupFail.Load() > 0 {
		t.Errorf("ST-REDIS2 FAIL: %d dedup checks failed when only REDIS_STATE was down", dedupFail.Load())
	}

	// Rate must be unaffected — no fallback
	fallbackBefore := p.rate.fallback.Load()
	for i := 0; i < 50; i++ {
		_ = p.rate.Allow(fmt.Sprintf("tenant-%d:ses", i))
	}
	if p.rate.fallback.Load() > fallbackBefore {
		t.Errorf("ST-REDIS2 FAIL: rate cluster triggered conservative fallback when only REDIS_STATE was down")
	}

	// Saga state must rebuild from Spanner
	var spannerOK, missing int
	for i := 0; i < sagaCount; i++ {
		step := p.GetSagaStep(fmt.Sprintf("saga-%d", i))
		if step == nil {
			missing++
		} else if step.Status == "succeeded" {
			spannerOK++
		}
	}

	if missing > 0 {
		t.Errorf("ST-REDIS2 FAIL: %d saga steps lost — Spanner rebuild failed", missing)
	}
	if spannerOK != sagaCount {
		t.Errorf("ST-REDIS2 FAIL: expected %d Spanner rebuilds, got %d", sagaCount, spannerOK)
	}

	t.Logf("ST-REDIS2: dedup OK=%d, rate fallback delta=%d, Spanner rebuilds=%d",
		dedupOK.Load(), p.rate.fallback.Load()-fallbackBefore, spannerOK)
	t.Logf("ST-REDIS2 PASS: REDIS_STATE failure isolated — dedup and rate fully operational, saga rebuilt from Spanner")
}

// ============================================================
// ST-REDIS3: REDIS_DEDUP fails — fail-open fires, rate and saga unaffected
// ============================================================

func TestST_REDIS3_DedupFailure_FailOpenRateAndSagaUnaffected(t *testing.T) {
	p := NewPlatform()

	p.SetSagaStep("critical-saga", &SagaStepState{StepIndex: 1, Status: "pending", UpdatedAt: time.Now()})

	p.dedup.SimulateFailure()
	t.Logf("ST-REDIS3: REDIS_DEDUP DOWN")

	const attempts = 1_000
	var failOpen, blocked atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Use unique tenant per message so the rate limiter never throttles —
			// each bucket is fresh. This isolates the dedup fail-open behaviour
			// from rate limiting, which is the point of the test.
			_, isFailOpen, _ := p.Send(fmt.Sprintf("tenant-%d", i), fmt.Sprintf("msg-%d", i), "apns")
			if isFailOpen {
				failOpen.Add(1)
			} else {
				blocked.Add(1)
			}
		}()
	}
	wg.Wait()

	if failOpen.Load() == 0 {
		t.Errorf("ST-REDIS3 FAIL: no fail-open events while REDIS_DEDUP was down")
	}
	if blocked.Load() > 0 {
		t.Errorf("ST-REDIS3 FAIL: %d sends blocked — should be fail-open, not blocked", blocked.Load())
	}
	if failOpen.Load() != attempts {
		t.Errorf("ST-REDIS3 FAIL: expected %d fail-open sends, got %d", attempts, failOpen.Load())
	}

	// Saga must be unaffected
	step := p.GetSagaStep("critical-saga")
	if step == nil || step.Status != "pending" {
		t.Errorf("ST-REDIS3 FAIL: saga step corrupted when REDIS_DEDUP went down")
	}

	// Rate must be unaffected
	if p.rate.fallback.Load() > 0 {
		t.Errorf("ST-REDIS3 FAIL: rate cluster fell back when only REDIS_DEDUP was down")
	}

	t.Logf("ST-REDIS3: failOpen=%d, blocked=%d, saga=intact, rate=normal", failOpen.Load(), blocked.Load())
	t.Logf("ST-REDIS3 PASS: REDIS_DEDUP failure triggers fail-open only — rate and saga clusters unaffected")
}

// ============================================================
// ST-REDIS4: All three fail simultaneously — independent failure policies, no cross-contamination
// ============================================================

func TestST_REDIS4_AllClustersDown_IndependentPolicies(t *testing.T) {
	p := NewPlatform()

	const sagaCount = 100
	for i := 0; i < sagaCount; i++ {
		p.SetSagaStep(fmt.Sprintf("all-down-saga-%d", i), &SagaStepState{
			StepIndex: 3, Status: "succeeded", UpdatedAt: time.Now(),
		})
	}

	p.dedup.SimulateFailure()
	p.state.SimulateFailure()
	p.rate.SimulateFailure()
	t.Logf("ST-REDIS4: ALL three clusters DOWN simultaneously")

	// Dedup: fail-open — sends proceed (unique tenants so rate never blocks)
	var dedupFailOpen, dedupBlocked atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Unique tenant per goroutine: isolates dedup fail-open from rate throttling.
			// Rate cluster is down → conservative fallback (10/sec). With unique tenants
			// each bucket starts at 10 tokens — all 200 goroutines clear rate, then hit
			// the dedup fail-open path.
			_, isFailOpen, _ := p.Send(fmt.Sprintf("tenant-%d", i), fmt.Sprintf("all-down-msg-%d", i), "mailgun")
			if isFailOpen {
				dedupFailOpen.Add(1)
			} else {
				dedupBlocked.Add(1)
			}
		}()
	}
	wg.Wait()

	// Rate: conservative fallback fires
	rateFallbackBefore := p.rate.fallback.Load()
	for i := 0; i < 50; i++ {
		_ = p.rate.Allow(fmt.Sprintf("tenant-%d:twilio", i))
	}

	// Saga: Spanner rebuild — state not lost
	var spannerOK, missing int
	for i := 0; i < sagaCount; i++ {
		step := p.GetSagaStep(fmt.Sprintf("all-down-saga-%d", i))
		if step == nil {
			missing++
		} else {
			spannerOK++
		}
	}

	t.Logf("ST-REDIS4: dedup failOpen=%d blocked=%d", dedupFailOpen.Load(), dedupBlocked.Load())
	t.Logf("ST-REDIS4: rate fallback new requests=%d", p.rate.fallback.Load()-rateFallbackBefore)
	t.Logf("ST-REDIS4: saga spannerOK=%d missing=%d", spannerOK, missing)

	if dedupBlocked.Load() > 0 {
		t.Errorf("ST-REDIS4 FAIL: %d sends blocked — dedup should fail-open not block", dedupBlocked.Load())
	}
	if dedupFailOpen.Load() == 0 {
		t.Errorf("ST-REDIS4 FAIL: no dedup fail-open events recorded")
	}
	if p.rate.fallback.Load() <= rateFallbackBefore {
		t.Errorf("ST-REDIS4 FAIL: rate conservative fallback not invoked when cluster was down")
	}
	if missing > 0 {
		t.Errorf("ST-REDIS4 FAIL: %d saga steps missing — Spanner rebuild failed", missing)
	}

	// Dedup failOpen counter belongs to dedup only — not inflated by rate or state failures
	if p.dedup.failOpen.Load() == 0 {
		t.Errorf("ST-REDIS4 FAIL: dedup cluster failOpen counter is zero despite being down")
	}
	if p.rate.fallback.Load() == p.dedup.failOpen.Load() {
		// If these are exactly equal it may suggest the same counter was incremented by both
		// This is a soft warning — they could coincidentally match
		t.Logf("ST-REDIS4 WARNING: rate.fallback == dedup.failOpen (%d) — verify counters are independent",
			p.rate.fallback.Load())
	}

	t.Logf("ST-REDIS4 PASS: three simultaneous failures — each cluster applied its own policy with no cross-contamination")
}
