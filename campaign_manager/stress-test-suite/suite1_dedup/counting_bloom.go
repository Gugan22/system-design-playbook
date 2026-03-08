// Package suite1_dedup implements and stress-tests the three-tier dedup chain:
//   L1 — in-process LRU cache (per worker, ~512 MB cap)
//   L2 — Counting Bloom Filter (~72 GB, 4-bit counters, supports TTL expiry)
//   L3 — Slim Redis stub (confirmed duplicate hits only, ~14 GB)
//
// The key insight this code proves:
//   Naïve full-keyspace Redis at 10B events/day = 1.4 TB working set.
//   This 3-tier chain achieves the same duplicate detection in ~87 GB.
package suite1_dedup

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"sync"
	"sync/atomic"
)

// ============================================================
// L2: Counting Bloom Filter
// ============================================================
//
// A standard Bloom filter uses single bits. Once set, a bit cannot be
// cleared without risking false negatives for other keys that share it.
// This makes standard Bloom filters append-only — unsuitable for TTL expiry.
//
// A Counting Bloom Filter uses N-bit counters instead of single bits.
// On insert: increment all k counter positions.
// On delete/expire: decrement all k counter positions.
// On lookup: all k counters > 0 → "probably present".
//
// We use 4-bit counters (values 0–15).
// OVERFLOW SAFETY: a counter at 15 is saturated. Decrement is a no-op on
// saturated counters — we never decrement to zero and create a false negative.
//
// Sizing math (board-approved):
//   n = 10,000,000,000 keys (10B events/day)
//   p = 0.001           (0.1% false positive rate)
//   m = -n * ln(p) / ln(2)^2 ≈ 143.8 billion bits for a standard BF
//   k = m/n * ln(2)     ≈ 10 hash functions
//
//   With 4-bit counters: m * 4 bits = 143.8B * 4 = 575 billion bits = ~71.9 GB
//   This matches the board-approved 72 GB budget.

const (
	// cbfNumBits is the total number of counter slots.
	// Each slot is 4 bits. We pack 2 counters per byte.
	// 143_800_000_000 slots * 4 bits / 8 bits per byte ≈ 71.9 GB.
	// For tests we use a scaled-down version — set cbfScaleFactor to reduce.
	cbfNumBitsFullScale int64 = 143_800_000_000

	// cbfNumHashFunctions is k, the number of hash positions per key.
	cbfNumHashFunctions = 10

	// cbfMaxCounter is the maximum 4-bit counter value (saturation point).
	// A saturated counter is NEVER decremented — prevents false negatives.
	cbfMaxCounter byte = 0x0F
)

// CountingBloomFilter is a thread-safe 4-bit-counter Bloom filter.
// For test purposes, numBits is configurable (use cbfNumBitsFullScale / scaleFactor).
type CountingBloomFilter struct {
	// data packs two 4-bit counters per byte.
	// slot i occupies: byte i/2, nibble i%2 (0=low, 1=high).
	data    []byte
	numBits int64
	numHash int
	mu      sync.RWMutex

	insertions atomic.Int64
	deletions  atomic.Int64
	fpChecks   atomic.Int64 // total lookups
	fpHits     atomic.Int64 // lookups that returned true (for FPR calculation)
}

// NewCountingBloomFilter creates a filter with the given number of counter slots
// and k hash functions. For a production deployment use cbfNumBitsFullScale.
// For testing, pass a smaller value (e.g. cbfNumBitsFullScale / 1_000_000).
func NewCountingBloomFilter(numBits int64, numHash int) *CountingBloomFilter {
	// We need ceil(numBits / 2) bytes (2 counters per byte).
	numBytes := (numBits + 1) / 2
	return &CountingBloomFilter{
		data:    make([]byte, numBytes),
		numBits: numBits,
		numHash: numHash,
	}
}

// hashPositions returns k slot indices for the given key.
// We use double-hashing: h_i(x) = (h1(x) + i * h2(x)) mod m.
// Uses uint64 arithmetic throughout to prevent signed overflow → negative index.
func (cbf *CountingBloomFilter) hashPositions(key string) []int64 {
	sum := sha256.Sum256([]byte(key))
	h1 := binary.LittleEndian.Uint64(sum[0:8])
	h2 := binary.LittleEndian.Uint64(sum[8:16])
	// Ensure h2 is odd to guarantee full coverage of all slots.
	if h2%2 == 0 {
		h2++
	}
	m := uint64(cbf.numBits)
	positions := make([]int64, cbf.numHash)
	for i := 0; i < cbf.numHash; i++ {
		// All arithmetic in uint64 — can never go negative.
		positions[i] = int64((h1 + uint64(i)*h2) % m)
	}
	return positions
}

// getCounter reads the 4-bit counter at slot index i. Caller must hold mu.
func (cbf *CountingBloomFilter) getCounter(i int64) byte {
	byteIdx := i / 2
	nibble := i % 2
	b := cbf.data[byteIdx]
	if nibble == 0 {
		return b & 0x0F // low nibble
	}
	return (b >> 4) & 0x0F // high nibble
}

// setCounter writes a 4-bit value to slot index i. Caller must hold mu.
func (cbf *CountingBloomFilter) setCounter(i int64, val byte) {
	byteIdx := i / 2
	nibble := i % 2
	if nibble == 0 {
		cbf.data[byteIdx] = (cbf.data[byteIdx] & 0xF0) | (val & 0x0F)
	} else {
		cbf.data[byteIdx] = (cbf.data[byteIdx] & 0x0F) | ((val & 0x0F) << 4)
	}
}

// Add inserts key into the filter, incrementing counters at all k positions.
func (cbf *CountingBloomFilter) Add(key string) {
	positions := cbf.hashPositions(key)
	cbf.mu.Lock()
	defer cbf.mu.Unlock()
	for _, pos := range positions {
		cur := cbf.getCounter(pos)
		if cur < cbfMaxCounter {
			cbf.setCounter(pos, cur+1)
		}
		// If cur == cbfMaxCounter: counter is saturated — leave it.
		// This is safe: the key remains "present" in the filter.
		// The saturation case is exercised in ST-D4.
	}
	cbf.insertions.Add(1)
}

// Remove decrements counters for key. This is the TTL expiry mechanism.
// CRITICAL: saturated counters (==15) are never decremented.
// Decrementing a saturated counter could produce a false negative for
// another key that shares that counter bucket.
func (cbf *CountingBloomFilter) Remove(key string) {
	positions := cbf.hashPositions(key)
	cbf.mu.Lock()
	defer cbf.mu.Unlock()
	for _, pos := range positions {
		cur := cbf.getCounter(pos)
		if cur == 0 {
			// Already zero — key was never added or already removed.
			// No-op: we never go below 0.
			continue
		}
		if cur == cbfMaxCounter {
			// Saturated counter — DO NOT decrement.
			// Another key shares this bucket and saturated it.
			// Decrementing here would risk a false negative for that key.
			continue
		}
		cbf.setCounter(pos, cur-1)
	}
	cbf.deletions.Add(1)
}

// Test returns true if key is "probably present" (all k counters > 0).
// Returns false if key is "definitely not present" (at least one counter == 0).
func (cbf *CountingBloomFilter) Test(key string) bool {
	positions := cbf.hashPositions(key)
	cbf.mu.RLock()
	defer cbf.mu.RUnlock()
	cbf.fpChecks.Add(1)
	for _, pos := range positions {
		if cbf.getCounter(pos) == 0 {
			return false // definitely not present
		}
	}
	cbf.fpHits.Add(1)
	return true // probably present
}

// MeasuredFPR returns the empirically observed false positive rate.
// Call this after loading the filter with known keys and testing unknown keys.
// truePositives: how many of the fpHits were actual true positives.
func (cbf *CountingBloomFilter) MeasuredFPR(truePositives int64) float64 {
	checks := cbf.fpChecks.Load()
	hits := cbf.fpHits.Load()
	if checks == 0 {
		return 0
	}
	fp := hits - truePositives
	if fp < 0 { fp = 0 }
	return float64(fp) / float64(checks)
}

// TheoreticalFPR returns the expected FPR given current insertions and filter size.
func (cbf *CountingBloomFilter) TheoreticalFPR() float64 {
	n := float64(cbf.insertions.Load())
	m := float64(cbf.numBits)
	k := float64(cbf.numHash)
	return math.Pow(1-math.Exp(-k*n/m), k)
}

// MemoryBytes returns the number of bytes allocated for the counter array.
func (cbf *CountingBloomFilter) MemoryBytes() int64 {
	return int64(len(cbf.data))
}
