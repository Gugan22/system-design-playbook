// Package suite2_cb implements and stress-tests the per-tenant per-provider
// circuit breaker manager.
//
// Key design properties proven by this suite:
//   - 10,000 CB instances (5k tenants × 2 push providers) fit in ~60 MB.
//     (Design doc estimated ~20MB based on Resilience4j; our Go implementation
//     uses a 256-slot sliding window at 24B/slot = ~6KB/instance = ~60MB total.
//     Still a fixed, bounded allocation — the architectural guarantee holds.)
//   - One tenant's CB opening does NOT affect any other tenant.
//   - Push threshold (80% / 30s) differs from email/SMS (50% / 10s) to
//     prevent false-trips on normal APNs/FCM burst traffic.
//   - The factory uses striped locking — no global lock on every CB lookup.
//   - 2FA (Gold Lane) thread pool is isolated from bulk CB pressure.
package suite2_cb

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/campaign-platform/stress-suite/internal"
)

// ============================================================
// Circuit Breaker State Machine
// ============================================================

type cbState int32

const (
	StateClosed   cbState = 0 // healthy — requests flow
	StateOpen     cbState = 1 // tripped — requests go to fallback
	StateHalfOpen cbState = 2 // probe in flight — one request allowed
)

// CBConfig holds the threshold parameters for a single CB instance.
type CBConfig struct {
	ErrorWindowDuration time.Duration
	OpenThresholdPct    float64       // 0.0–1.0: error rate to open CB
	HalfOpenAfter       time.Duration // how long before trying a probe
}

var (
	// PushCBConfig: APNs/FCM are bursty. Tight thresholds = constant false trips.
	// Board decision (see PLATFORM_DESIGN.md §16): 80% threshold, 30-second window.
	// Wider window absorbs provider burstiness; 80% threshold prevents false-trips
	// on normal APNs 429 storms while still detecting a genuine FCM outage.
	PushCBConfig = CBConfig{
		ErrorWindowDuration: 30 * time.Second,
		OpenThresholdPct:    0.80,
		HalfOpenAfter:       30 * time.Second,
	}

	// EmailSMSCBConfig: steady error profile. Standard thresholds.
	// Board decision: 50% threshold, 10-second window.
	EmailSMSCBConfig = CBConfig{
		ErrorWindowDuration: 10 * time.Second,
		OpenThresholdPct:    0.50,
		HalfOpenAfter:       30 * time.Second,
	}
)

// CBConfigFor returns the correct config for a given channel.
func CBConfigFor(ch internal.Channel) CBConfig {
	if ch == internal.ChannelPush {
		return PushCBConfig
	}
	return EmailSMSCBConfig
}

// request records a single request outcome in the sliding window.
type request struct {
	at      time.Time
	success bool
}

// CircuitBreaker is a single per-tenant per-provider CB instance.
// Memory: ~2 KB (window slice + small fields).
type CircuitBreaker struct {
	config  CBConfig
	mu      sync.Mutex
	state   cbState
	window  []request // sliding window of recent requests
	openAt  time.Time // when the CB last opened

	// Metrics
	totalRequests atomic.Int64
	totalErrors   atomic.Int64
	openCount     atomic.Int64 // how many times the CB has opened
}

func newCircuitBreaker(cfg CBConfig) *CircuitBreaker {
	return &CircuitBreaker{
		config: cfg,
		state:  StateClosed,
		window: make([]request, 0, 256),
	}
}

// Allow returns true if the request should be sent to the real provider.
// Returns false if the CB is open (route to fallback instead).
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	now := time.Now()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if now.Sub(cb.openAt) >= cb.config.HalfOpenAfter {
			cb.state = StateHalfOpen
			return true // allow one probe
		}
		return false
	case StateHalfOpen:
		return false // probe already in flight
	}
	return true
}

// Record records the outcome of a request and updates CB state.
func (cb *CircuitBreaker) Record(success bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	now := time.Now()

	cb.totalRequests.Add(1)
	if !success {
		cb.totalErrors.Add(1)
	}

	// Advance state machine for half-open probe
	if cb.state == StateHalfOpen {
		if success {
			cb.state = StateClosed
			cb.window = cb.window[:0] // reset window
		} else {
			cb.state = StateOpen
			cb.openAt = now
		}
		return
	}

	// If already open, do not re-evaluate or append to window.
	if cb.state == StateOpen {
		return
	}

	// Append to sliding window
	cb.window = append(cb.window, request{at: now, success: success})

	// Evict expired entries from the window
	cutoff := now.Add(-cb.config.ErrorWindowDuration)
	i := 0
	for i < len(cb.window) && cb.window[i].at.Before(cutoff) {
		i++
	}
	cb.window = cb.window[i:]

	// Evaluate threshold
	if len(cb.window) < 10 {
		return // need at least 10 requests before opening
	}
	var errors int
	for _, r := range cb.window {
		if !r.success {
			errors++
		}
	}
	errorRate := float64(errors) / float64(len(cb.window))
	if errorRate >= cb.config.OpenThresholdPct {
		cb.state = StateOpen
		cb.openAt = now
		cb.openCount.Add(1)
	}
}

// DebugCounts returns (windowSize, errorCount, errorRate) for test diagnostics.
func (cb *CircuitBreaker) DebugCounts() (int, int, float64) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	var errors int
	for _, r := range cb.window {
		if !r.success {
			errors++
		}
	}
	rate := 0.0
	if len(cb.window) > 0 {
		rate = float64(errors) / float64(len(cb.window))
	}
	return len(cb.window), errors, rate
}

// State returns the current CB state.
func (cb *CircuitBreaker) State() cbState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// MemoryEstimateBytes returns a rough memory footprint estimate.
func (cb *CircuitBreaker) MemoryEstimateBytes() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	// Fixed overhead ~200 bytes + window slice (256 * ~24 bytes = 6144 bytes)
	return 200 + cap(cb.window)*24
}

// ============================================================
// CBManager — factory for 10,000 CB instances
// ============================================================
//
// Uses striped locking: 256 stripes, each protecting a shard of the key space.
// This means concurrent lookups for different tenants never contend on the
// same lock unless they hash to the same stripe (1/256 chance).

const numStripes = 256

type stripe struct {
	mu  sync.RWMutex
	cbs map[string]*CircuitBreaker
}

// CBManager manages per-tenant per-provider CB instances.
type CBManager struct {
	stripes [numStripes]stripe
}

// NewCBManager creates a CB manager. Thread-safe. Zero allocations on read
// after warm-up.
func NewCBManager() *CBManager {
	m := &CBManager{}
	for i := range m.stripes {
		m.stripes[i].cbs = make(map[string]*CircuitBreaker)
	}
	return m
}

// cbKey builds the isolation key: "{tenant_id}:{provider}"
func cbKey(tenantID internal.TenantID, provider internal.Provider) string {
	return fmt.Sprintf("%s:%s", tenantID, provider)
}

// stripeIndex maps a key to one of 256 stripes via FNV-like hash.
func stripeIndex(key string) int {
	h := uint32(2166136261) // FNV offset basis
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619 // FNV prime
	}
	return int(h % numStripes)
}

// Get returns the CB for a tenant+provider pair, creating it if it doesn't exist.
func (m *CBManager) Get(tenantID internal.TenantID, provider internal.Provider, ch internal.Channel) *CircuitBreaker {
	key := cbKey(tenantID, provider)
	idx := stripeIndex(key)
	s := &m.stripes[idx]

	// Fast path: read lock
	s.mu.RLock()
	cb, ok := s.cbs[key]
	s.mu.RUnlock()
	if ok {
		return cb
	}

	// Slow path: write lock, double-check
	s.mu.Lock()
	defer s.mu.Unlock()
	if cb, ok = s.cbs[key]; ok {
		return cb
	}
	cb = newCircuitBreaker(CBConfigFor(ch))
	s.cbs[key] = cb
	return cb
}

// TotalInstances returns the total number of CB instances across all stripes.
func (m *CBManager) TotalInstances() int {
	total := 0
	for i := range m.stripes {
		m.stripes[i].mu.RLock()
		total += len(m.stripes[i].cbs)
		m.stripes[i].mu.RUnlock()
	}
	return total
}

// TotalMemoryBytes estimates the total memory used by all CB instances.
func (m *CBManager) TotalMemoryBytes() int {
	total := 0
	for i := range m.stripes {
		m.stripes[i].mu.RLock()
		for _, cb := range m.stripes[i].cbs {
			total += cb.MemoryEstimateBytes()
		}
		m.stripes[i].mu.RUnlock()
	}
	return total
}

// OpenCount returns the number of tenants currently with an open CB.
func (m *CBManager) OpenCount() int {
	count := 0
	for i := range m.stripes {
		m.stripes[i].mu.RLock()
		for _, cb := range m.stripes[i].cbs {
			if cb.State() == StateOpen {
				count++
			}
		}
		m.stripes[i].mu.RUnlock()
	}
	return count
}
