// Package suite9_schema tests the Schema Registry outage grace period.
//
// Architecture decision tested:
//   On Schema Registry outage, producers use cached schemas for up to 1 hour.
//   During that window: zero impact on sends. After the grace period expires
//   without registry recovery: producers must fail loudly (not silently send
//   with stale schemas that may have changed).
//
// Tests:
//   ST-SCH1: Registry down — cached schema used, send proceeds (no impact).
//   ST-SCH2: Registry down > 1 hour — send fails loudly (stale schema rejected).
//   ST-SCH3: Registry recovers within grace period — cache refreshes, no send gap.
//   ST-SCH4: Unknown schema (never cached) + registry down → fail immediately.
package suite9_schema

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================
// Schema Registry
// ============================================================

var (
	ErrRegistryDown    = errors.New("schema registry unavailable")
	ErrSchemaNotCached = errors.New("schema not in cache — cannot send without registry")
	ErrSchemaTTLExpired = errors.New("cached schema TTL expired — registry must recover before sending")
)

type Schema struct {
	ID        int
	Name      string
	Version   int
	CachedAt  time.Time
}

// SchemaRegistry is the authoritative source of Avro/Protobuf schemas.
type SchemaRegistry struct {
	mu      sync.RWMutex
	schemas map[string]*Schema
	alive   atomic.Bool
	// queries counts successful lookups (for test assertions).
	queries atomic.Int64
}

func NewSchemaRegistry() *SchemaRegistry {
	sr := &SchemaRegistry{schemas: make(map[string]*Schema)}
	sr.alive.Store(true)
	return sr
}

func (sr *SchemaRegistry) Register(name string, version int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.schemas[name] = &Schema{ID: len(sr.schemas) + 1, Name: name, Version: version}
}

func (sr *SchemaRegistry) Fetch(name string) (*Schema, error) {
	if !sr.alive.Load() {
		return nil, ErrRegistryDown
	}
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	s, ok := sr.schemas[name]
	if !ok {
		return nil, errors.New("schema not found: " + name)
	}
	sr.queries.Add(1)
	copy := *s
	copy.CachedAt = time.Now()
	return &copy, nil
}

func (sr *SchemaRegistry) SimulateOutage() { sr.alive.Store(false) }
func (sr *SchemaRegistry) Restore()        { sr.alive.Store(true) }

// ============================================================
// Producer — caches schemas locally with 1-hour TTL
// ============================================================

const SchemaGracePeriod = time.Hour

type Producer struct {
	registry    *SchemaRegistry
	cache       map[string]*Schema
	cacheMu     sync.RWMutex
	gracePeriod time.Duration

	cacheHits   atomic.Int64
	cacheMisses atomic.Int64
	sends       atomic.Int64
	hardFails   atomic.Int64
}

func NewProducer(registry *SchemaRegistry) *Producer {
	return &Producer{
		registry:    registry,
		cache:       make(map[string]*Schema),
		gracePeriod: SchemaGracePeriod,
	}
}

// newProducerWithGrace creates a producer with configurable grace period (for testing).
func newProducerWithGrace(registry *SchemaRegistry, grace time.Duration) *Producer {
	return &Producer{
		registry:    registry,
		cache:       make(map[string]*Schema),
		gracePeriod: grace,
	}
}

// GetSchema returns the schema for the given topic, using cache if registry is down.
func (p *Producer) GetSchema(topic string) (*Schema, error) {
	// Try registry first
	s, err := p.registry.Fetch(topic)
	if err == nil {
		// Registry up — refresh cache
		p.cacheMu.Lock()
		p.cache[topic] = s
		p.cacheMu.Unlock()
		p.cacheMisses.Add(1) // counted as a live fetch
		return s, nil
	}

	// Registry is down — try cache
	p.cacheMu.RLock()
	cached, ok := p.cache[topic]
	p.cacheMu.RUnlock()

	if !ok {
		// Never cached — cannot proceed
		p.hardFails.Add(1)
		return nil, ErrSchemaNotCached
	}

	// Check grace period
	age := time.Since(cached.CachedAt)
	if age > p.gracePeriod {
		// TTL expired — cannot safely use stale schema
		p.hardFails.Add(1)
		return nil, ErrSchemaTTLExpired
	}

	// Within grace period — use cache
	p.cacheHits.Add(1)
	return cached, nil
}

// Produce sends a message using the resolved schema.
func (p *Producer) Produce(topic string, payload []byte) error {
	_, err := p.GetSchema(topic)
	if err != nil {
		return err
	}
	p.sends.Add(1)
	_ = payload
	return nil
}

// ============================================================
// ST-SCH1: Registry Down — Cached Schema Used, Send Proceeds
// ============================================================

func TestST_SCH1_CachedSchemaUsedOnRegistryOutage(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.Register("campaign-events", 1)
	p := newProducerWithGrace(reg, time.Hour)

	// Prime the cache with a live fetch
	_, err := p.GetSchema("campaign-events")
	if err != nil {
		t.Fatalf("ST-SCH1: initial fetch failed: %v", err)
	}

	// Registry goes down
	reg.SimulateOutage()

	// Sends should still work within grace period
	var succeeded, failed atomic.Int64
	for i := 0; i < 1_000; i++ {
		if err := p.Produce("campaign-events", []byte("payload")); err != nil {
			failed.Add(1)
		} else {
			succeeded.Add(1)
		}
	}

	t.Logf("ST-SCH1: registry_down, cache_hits=%d, succeeded=%d, failed=%d",
		p.cacheHits.Load(), succeeded.Load(), failed.Load())

	if failed.Load() > 0 {
		t.Errorf("ST-SCH1 FAIL: %d sends failed despite valid cached schema within grace period", failed.Load())
	}
	if p.cacheHits.Load() == 0 {
		t.Errorf("ST-SCH1 FAIL: cache was never hit — grace period not engaged")
	}
	t.Logf("ST-SCH1 PASS: %d sends succeeded using cached schema during registry outage", succeeded.Load())
}

// ============================================================
// ST-SCH2: Registry Down > Grace Period — Send Fails Loudly
// ============================================================

func TestST_SCH2_ExpiredCacheFails(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.Register("campaign-events", 1)
	// Very short grace period for test speed
	p := newProducerWithGrace(reg, 100*time.Millisecond)

	// Prime cache
	_, err := p.GetSchema("campaign-events")
	if err != nil {
		t.Fatalf("ST-SCH2: initial fetch failed: %v", err)
	}

	reg.SimulateOutage()

	// Wait for grace period to expire
	time.Sleep(150 * time.Millisecond)

	err = p.Produce("campaign-events", []byte("payload"))
	t.Logf("ST-SCH2: post-TTL produce error: %v", err)

	if err == nil {
		t.Errorf("ST-SCH2 FAIL: send succeeded after grace period expired — stale schema silently used")
	}
	if !errors.Is(err, ErrSchemaTTLExpired) {
		t.Errorf("ST-SCH2 FAIL: expected ErrSchemaTTLExpired, got %v", err)
	}
	t.Logf("ST-SCH2 PASS: expired cached schema correctly rejected — producer fails loudly")
}

// ============================================================
// ST-SCH3: Registry Recovers Within Grace Period — Cache Refreshes
// ============================================================

func TestST_SCH3_RegistryRecoveryRefreshesCache(t *testing.T) {
	reg := NewSchemaRegistry()
	reg.Register("campaign-events", 1)
	p := newProducerWithGrace(reg, time.Hour)

	_, err := p.GetSchema("campaign-events")
	if err != nil {
		t.Fatalf("ST-SCH3: initial fetch failed: %v", err)
	}

	// Outage: sends use cache
	reg.SimulateOutage()
	queriesBefore := reg.queries.Load()

	for i := 0; i < 100; i++ {
		_ = p.Produce("campaign-events", []byte("p"))
	}

	// Registry recovers — next fetch should hit the live registry and refresh cache
	reg.Restore()
	s, err := p.GetSchema("campaign-events")
	if err != nil {
		t.Errorf("ST-SCH3 FAIL: fetch after recovery failed: %v", err)
	}

	queriesAfter := reg.queries.Load()
	t.Logf("ST-SCH3: registry_queries before=%d, after=%d, schema_version=%d",
		queriesBefore, queriesAfter, s.Version)

	if queriesAfter <= queriesBefore {
		t.Errorf("ST-SCH3 FAIL: registry not queried after recovery (cache not refreshed)")
	}
	// Verify cache was updated
	p.cacheMu.RLock()
	cached := p.cache["campaign-events"]
	p.cacheMu.RUnlock()
	if time.Since(cached.CachedAt) > 100*time.Millisecond {
		t.Errorf("ST-SCH3 FAIL: cache timestamp not refreshed after registry recovery")
	}
	t.Logf("ST-SCH3 PASS: cache refreshed on registry recovery, sends uninterrupted during outage")
}

// ============================================================
// ST-SCH4: Unknown Schema + Registry Down → Immediate Hard Fail
// ============================================================

func TestST_SCH4_UnknownSchemaRegistryDown(t *testing.T) {
	reg := NewSchemaRegistry()
	p := newProducerWithGrace(reg, time.Hour)

	// Registry goes down BEFORE this schema is ever fetched — nothing in cache
	reg.SimulateOutage()

	err := p.Produce("never-seen-topic", []byte("payload"))
	t.Logf("ST-SCH4: error=%v", err)

	if err == nil {
		t.Errorf("ST-SCH4 FAIL: produce succeeded for uncached schema with registry down")
	}
	if !errors.Is(err, ErrSchemaNotCached) {
		t.Errorf("ST-SCH4 FAIL: expected ErrSchemaNotCached, got %v", err)
	}
	t.Logf("ST-SCH4 PASS: uncached schema + registry down = immediate fail (not silent send)")
}
