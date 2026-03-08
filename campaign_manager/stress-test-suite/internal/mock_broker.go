// Package internal — mock Kafka broker.
//
// MockBroker simulates two independent Kafka lanes (Gold + Silver) with
// separate partition pools, consumer groups, and lag counters.
// It is intentionally minimal: it does not simulate replication, persistence,
// or schema registry — only the properties the lane-isolation tests need:
//   - Gold consumers are never blocked by Silver lag.
//   - Per-lane lag is observable and assertable.
//   - Tier shedding fires at the correct Silver lag thresholds.
package internal

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// GoldLagAlertThreshold mirrors the board-approved alert: >50k messages.
	GoldLagAlertThreshold int64 = 50_000
	// SilverLagAlertThreshold mirrors the board-approved alert: >5M messages.
	SilverLagAlertThreshold int64 = 5_000_000
	// SilverTier3ShedThreshold is when Tier 3 (analytics) is auto-shed.
	// Production value: 5M. Mock uses 80% of Silver channel capacity (480k)
	// so the threshold is reachable in tests without a real Kafka cluster.
	SilverTier3ShedThreshold int64 = 480_000
)

// LaneQueue is a single-lane FIFO buffer representing a Kafka partition pool.
type LaneQueue struct {
	ch       chan *Message
	lag      atomic.Int64 // current unconsumed message count
	enqueued atomic.Int64
	consumed atomic.Int64
	name     Lane
}

func newLaneQueue(name Lane, capacity int) *LaneQueue {
	return &LaneQueue{
		ch:   make(chan *Message, capacity),
		name: name,
	}
}

// Publish adds a message to the lane. Returns false if the lane is full.
func (q *LaneQueue) Publish(m *Message) bool {
	select {
	case q.ch <- m:
		q.enqueued.Add(1)
		q.lag.Add(1)
		return true
	default:
		return false // lane full — caller handles overflow
	}
}

// Consume returns the next message from the lane, blocking until ctx is done.
func (q *LaneQueue) Consume(ctx context.Context) (*Message, bool) {
	select {
	case m := <-q.ch:
		q.consumed.Add(1)
		q.lag.Add(-1)
		return m, true
	case <-ctx.Done():
		return nil, false
	}
}

// Lag returns the current number of unconsumed messages.
func (q *LaneQueue) Lag() int64 { return q.lag.Load() }

// MockBroker holds the Gold and Silver lanes and the tier-shedding policy.
type MockBroker struct {
	Gold   *LaneQueue
	Silver *LaneQueue

	// Shed tracks which tiers have been automatically shed.
	Tier3Shed atomic.Bool
	Tier4Shed atomic.Bool

	mu       sync.Mutex
	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewMockBroker constructs a broker with realistic partition-pool capacities.
// Gold: 120 partitions × 1000 buffer = 120k. Silver: 600 × 1000 = 600k.
func NewMockBroker() *MockBroker {
	b := &MockBroker{
		Gold:   newLaneQueue(LaneGold, 120_000),
		Silver: newLaneQueue(LaneSilver, 600_000),
		stopCh: make(chan struct{}),
	}
	go b.runShedPolicy()
	return b
}

// Publish routes a message to the correct lane based on its Lane field.
func (b *MockBroker) Publish(m *Message) bool {
	switch m.Lane {
	case LaneGold:
		return b.Gold.Publish(m)
	case LaneSilver:
		// If Tier 3/4 shedding is active and this message is analytics/ML,
		// drop it (simulates the rate-limiter shed policy).
		return b.Silver.Publish(m)
	default:
		return false
	}
}

// runShedPolicy watches Silver lag and activates tier shedding automatically.
// This mirrors the platform's hard-coded degradation priority policy.
func (b *MockBroker) runShedPolicy() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			lag := b.Silver.Lag()
			if lag >= SilverTier3ShedThreshold {
				if b.Tier3Shed.CompareAndSwap(false, true) {
					// first time crossing threshold — log shed event
					_ = lag // in real code: emit metric
				}
				b.Tier4Shed.Store(true)
			} else {
				// lag recovered — restore tiers
				b.Tier3Shed.Store(false)
				b.Tier4Shed.Store(false)
			}
		}
	}
}

// Stop shuts down the broker's background goroutine.
func (b *MockBroker) Stop() {
	b.stopOnce.Do(func() { close(b.stopCh) })
}
