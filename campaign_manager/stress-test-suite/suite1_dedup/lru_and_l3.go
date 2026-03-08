package suite1_dedup

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================
// L1: In-Process LRU Cache (per worker, ~512 MB cap)
// ============================================================
//
// Each delivery worker maintains its own LRU. This absorbs same-worker retries
// at zero network cost (sub-ms). The cap of 100k message IDs is chosen so
// that at ~64 bytes per ID the working set stays under 512 MB.
//
// When the LRU is full, the least-recently-used entry is evicted.
// Eviction does NOT cause a false negative — L2 catches any evicted duplicate.

const (
	L1DefaultCapacity = 100_000 // 100k entries ≈ 6.4 MB per worker
)

type lruEntry struct {
	key  string
	elem *list.Element
}

// L1Cache is a thread-safe LRU. Hit = message is a duplicate. Miss = pass to L2.
type L1Cache struct {
	capacity int
	mu       sync.Mutex
	items    map[string]*lruEntry
	order    *list.List // front = most recent

	hits   atomic.Int64
	misses atomic.Int64
	evicts atomic.Int64
}

func NewL1Cache(capacity int) *L1Cache {
	return &L1Cache{
		capacity: capacity,
		items:    make(map[string]*lruEntry, capacity),
		order:    list.New(),
	}
}

// Test returns true (duplicate) if key is in cache. Always records the access.
func (c *L1Cache) Test(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.order.MoveToFront(e.elem)
		c.hits.Add(1)
		return true
	}
	c.misses.Add(1)
	return false
}

// Add inserts key. If at capacity, evicts the LRU entry first.
func (c *L1Cache) Add(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[key]; ok {
		return // already present
	}
	if len(c.items) >= c.capacity {
		// evict LRU (back of list)
		back := c.order.Back()
		if back != nil {
			c.order.Remove(back)
			delete(c.items, back.Value.(string))
			c.evicts.Add(1)
		}
	}
	elem := c.order.PushFront(key)
	c.items[key] = &lruEntry{key: key, elem: elem}
}

// ============================================================
// L3: Slim Redis Stub (confirmed duplicate hits only)
// ============================================================
//
// In production this is ElastiCache. In tests it is an in-process map.
// L3 stores ONLY confirmed duplicate message IDs — approximately 1% of
// the naïve full-keyspace size (14 GB vs 1.4 TB).
//
// A key reaches L3 only when L1 and L2 both confirm "probably duplicate".
// This means L3 is the definitive record of a confirmed dedup event.

type l3Entry struct {
	originalID string
	confirmedAt time.Time
}

// L3Store is the slim confirmed-duplicates store.
// It can be replaced with a real Redis client by implementing the same interface.
type L3Store struct {
	mu      sync.RWMutex
	entries map[string]*l3Entry
	// alive tracks whether the store is reachable.
	// Setting alive=false simulates a Redis shard failure (ST-D6).
	alive atomic.Bool

	confirms atomic.Int64
	failOpen atomic.Int64
}

func NewL3Store() *L3Store {
	s := &L3Store{entries: make(map[string]*l3Entry)}
	s.alive.Store(true)
	return s
}

// Confirm records a confirmed duplicate in L3.
// Returns (false, nil) if the store is down — fail-open, send proceeds.
func (s *L3Store) Confirm(msgID, originalID string) (recorded bool, err error) {
	if !s.alive.Load() {
		s.failOpen.Add(1)
		return false, nil // fail-open: store down, don't block send
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[msgID] = &l3Entry{originalID: originalID, confirmedAt: time.Now()}
	s.confirms.Add(1)
	return true, nil
}

// IsConfirmedDuplicate checks if msgID was previously confirmed as duplicate.
// Returns (false, nil) if the store is down — fail-open.
func (s *L3Store) IsConfirmedDuplicate(msgID string) (bool, error) {
	if !s.alive.Load() {
		s.failOpen.Add(1)
		return false, nil // fail-open
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.entries[msgID]
	return ok, nil
}

// SimulateFailure takes the store offline. All operations become fail-open.
func (s *L3Store) SimulateFailure() { s.alive.Store(false) }

// Restore brings the store back online.
func (s *L3Store) Restore() { s.alive.Store(true) }

// Size returns the number of confirmed duplicate records stored.
func (s *L3Store) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}
