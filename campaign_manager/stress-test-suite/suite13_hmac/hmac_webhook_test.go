// Package suite13_hmac tests HMAC-SHA256 webhook authentication for unsubscribe events.
//
// Design source: PLATFORM_DESIGN.md §13 (Unsubscribe Webhook Requires HMAC-SHA256 Verification)
//
// The attack vector: the unsubscribe webhook URL is called by providers when
// a contact unsubscribes. With no authentication, anyone who discovers the URL
// can POST a forged webhook that silently suppresses thousands of contacts.
// Those contacts never receive messages again — no alert, no audit trail.
//
// The defense:
//   - HMAC-SHA256 signature in X-Provider-Signature header
//   - Per-provider signing keys (a Twilio key cannot verify an SES event)
//   - Idempotency key per event — prevents replay attacks
//   - IP allowlist per provider (not tested here — infrastructure layer)
//
// Tests:
//   ST-HMAC1: Valid signature — webhook accepted, contact suppressed.
//   ST-HMAC2: Invalid signature — webhook rejected with 403, contact not suppressed.
//   ST-HMAC3: Replay attack — same idempotency key submitted twice, second rejected.
//   ST-HMAC4: Unknown provider — rejected because no HMAC key exists to verify against.
//   ST-HMAC5: Signature tampering after valid construction — any byte change in payload
//             invalidates the signature (proves HMAC covers the full payload).
//   ST-HMAC6: Concurrent forged webhooks — none suppress contacts, all rejected.
package suite13_hmac

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================
// Provider key store — per-provider HMAC signing keys
// ============================================================

var (
	ErrUnknownProvider  = errors.New("webhook: unknown provider — no HMAC key registered")
	ErrInvalidSignature = errors.New("webhook: HMAC-SHA256 signature mismatch")
	ErrReplayDetected   = errors.New("webhook: idempotency key already processed")
	ErrMissingSignature = errors.New("webhook: X-Provider-Signature header missing")
)

// ProviderKeyStore holds per-provider HMAC signing secrets.
// In production these are fetched from a secrets manager at startup.
type ProviderKeyStore struct {
	mu   sync.RWMutex
	keys map[string][]byte // provider name → HMAC secret
}

func NewProviderKeyStore() *ProviderKeyStore {
	return &ProviderKeyStore{keys: make(map[string][]byte)}
}

func (ks *ProviderKeyStore) Register(provider string, secret []byte) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.keys[provider] = secret
}

func (ks *ProviderKeyStore) Get(provider string) ([]byte, bool) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	k, ok := ks.keys[provider]
	return k, ok
}

// ============================================================
// Idempotency store — prevents replay attacks
// ============================================================

type WebhookIdempotencyStore struct {
	mu      sync.RWMutex
	seen    map[string]time.Time // idempotency key → first-seen time
	ttl     time.Duration
	replays atomic.Int64
}

func NewWebhookIdempotencyStore(ttl time.Duration) *WebhookIdempotencyStore {
	return &WebhookIdempotencyStore{
		seen: make(map[string]time.Time),
		ttl:  ttl,
	}
}

// CheckAndRecord returns ErrReplayDetected if the key was already processed within TTL.
func (s *WebhookIdempotencyStore) CheckAndRecord(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.seen[key]; ok {
		if time.Since(t) < s.ttl {
			s.replays.Add(1)
			return ErrReplayDetected
		}
		delete(s.seen, key) // TTL expired — treat as new
	}
	s.seen[key] = time.Now()
	return nil
}

// ============================================================
// Suppression DB stub (same contract as suite5)
// ============================================================

type SuppressionStore struct {
	mu         sync.RWMutex
	suppressed map[string]time.Time
	adds       atomic.Int64
}

func NewSuppressionStore() *SuppressionStore {
	return &SuppressionStore{suppressed: make(map[string]time.Time)}
}

func (s *SuppressionStore) Suppress(contactID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suppressed[contactID] = time.Now()
	s.adds.Add(1)
}

func (s *SuppressionStore) IsSuppressed(contactID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.suppressed[contactID]
	return ok
}

// ============================================================
// Webhook event
// ============================================================

type UnsubscribeWebhook struct {
	Provider       string
	ContactID      string
	IdempotencyKey string
	Timestamp      int64
	Signature      string // hex-encoded HMAC-SHA256 of canonical payload
}

// canonicalPayload builds the exact byte sequence that was signed.
// Provider signs: "{provider}:{contact_id}:{idempotency_key}:{timestamp}"
// Any deviation — even whitespace — invalidates the signature.
func canonicalPayload(w *UnsubscribeWebhook) []byte {
	return []byte(fmt.Sprintf("%s:%s:%s:%d", w.Provider, w.ContactID, w.IdempotencyKey, w.Timestamp))
}

// SignWebhook computes the HMAC-SHA256 signature for a webhook using the provider's secret.
func SignWebhook(w *UnsubscribeWebhook, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(canonicalPayload(w))
	return hex.EncodeToString(mac.Sum(nil))
}

// ============================================================
// WebhookProcessor — the authentication + suppression pipeline
// ============================================================

type ProcessResult struct {
	Accepted        bool
	ContactSuppressed bool
	Err             error
}

type WebhookProcessor struct {
	keys      *ProviderKeyStore
	idem      *WebhookIdempotencyStore
	suppress  *SuppressionStore

	accepted atomic.Int64
	rejected atomic.Int64
}

func NewWebhookProcessor(keys *ProviderKeyStore, idem *WebhookIdempotencyStore, suppress *SuppressionStore) *WebhookProcessor {
	return &WebhookProcessor{keys: keys, idem: idem, suppress: suppress}
}

func (p *WebhookProcessor) Process(w *UnsubscribeWebhook) ProcessResult {
	// Step 1: known provider?
	secret, ok := p.keys.Get(w.Provider)
	if !ok {
		p.rejected.Add(1)
		return ProcessResult{Err: ErrUnknownProvider}
	}

	// Step 2: signature present?
	if w.Signature == "" {
		p.rejected.Add(1)
		return ProcessResult{Err: ErrMissingSignature}
	}

	// Step 3: verify HMAC-SHA256
	expected := SignWebhook(w, secret)
	if !hmac.Equal([]byte(w.Signature), []byte(expected)) {
		p.rejected.Add(1)
		return ProcessResult{Err: ErrInvalidSignature}
	}

	// Step 4: idempotency check
	if err := p.idem.CheckAndRecord(w.IdempotencyKey); err != nil {
		p.rejected.Add(1)
		return ProcessResult{Err: ErrReplayDetected}
	}

	// Step 5: suppress the contact
	p.suppress.Suppress(w.ContactID)
	p.accepted.Add(1)
	return ProcessResult{Accepted: true, ContactSuppressed: true}
}

// ============================================================
// Test helpers
// ============================================================

func newTestProcessor() (*WebhookProcessor, *ProviderKeyStore) {
	keys := NewProviderKeyStore()
	keys.Register("twilio", []byte("twilio-secret-key-abc123"))
	keys.Register("sendgrid", []byte("sendgrid-secret-key-xyz789"))
	keys.Register("ses", []byte("ses-secret-key-def456"))
	idem := NewWebhookIdempotencyStore(24 * time.Hour)
	suppress := NewSuppressionStore()
	return NewWebhookProcessor(keys, idem, suppress), keys
}

func buildWebhook(provider, contactID, idemKey string, keys *ProviderKeyStore) *UnsubscribeWebhook {
	w := &UnsubscribeWebhook{
		Provider:       provider,
		ContactID:      contactID,
		IdempotencyKey: idemKey,
		Timestamp:      time.Now().Unix(),
	}
	secret, _ := keys.Get(provider)
	w.Signature = SignWebhook(w, secret)
	return w
}

// ============================================================
// ST-HMAC1: Valid signature — webhook accepted, contact suppressed
// ============================================================

func TestST_HMAC1_ValidSignature_WebhookAccepted(t *testing.T) {
	proc, keys := newTestProcessor()

	w := buildWebhook("twilio", "user-123@example.com", "idem-key-001", keys)

	result := proc.Process(w)

	t.Logf("ST-HMAC1: accepted=%v suppressed=%v err=%v", result.Accepted, result.ContactSuppressed, result.Err)

	if !result.Accepted {
		t.Errorf("ST-HMAC1 FAIL: valid webhook rejected (err=%v)", result.Err)
	}
	if !result.ContactSuppressed {
		t.Errorf("ST-HMAC1 FAIL: contact not suppressed after valid webhook")
	}
	if !proc.suppress.IsSuppressed("user-123@example.com") {
		t.Errorf("ST-HMAC1 FAIL: contact missing from suppression store")
	}
	if proc.accepted.Load() != 1 {
		t.Errorf("ST-HMAC1 FAIL: expected 1 accepted, got %d", proc.accepted.Load())
	}
	t.Logf("ST-HMAC1 PASS: valid HMAC-SHA256 webhook accepted, contact suppressed")
}

// ============================================================
// ST-HMAC2: Invalid signature — 403, contact not suppressed
// ============================================================

func TestST_HMAC2_InvalidSignature_WebhookRejected(t *testing.T) {
	proc, keys := newTestProcessor()

	// Build a valid webhook then corrupt the signature
	w := buildWebhook("twilio", "victim@example.com", "idem-key-002", keys)
	w.Signature = "deadbeef" + w.Signature[8:] // corrupt first 4 bytes of hex

	result := proc.Process(w)

	t.Logf("ST-HMAC2: accepted=%v err=%v", result.Accepted, result.Err)

	if result.Accepted {
		t.Errorf("ST-HMAC2 FAIL: webhook with invalid signature was accepted")
	}
	if result.Err != ErrInvalidSignature {
		t.Errorf("ST-HMAC2 FAIL: expected ErrInvalidSignature, got %v", result.Err)
	}
	if proc.suppress.IsSuppressed("victim@example.com") {
		t.Errorf("ST-HMAC2 FAIL: contact was suppressed despite invalid signature — forged webhook succeeded")
	}
	if proc.rejected.Load() == 0 {
		t.Errorf("ST-HMAC2 FAIL: rejected counter not incremented")
	}
	t.Logf("ST-HMAC2 PASS: invalid signature rejected — contact NOT suppressed")
}

// ============================================================
// ST-HMAC3: Replay attack — second submission of same idempotency key rejected
// ============================================================

func TestST_HMAC3_ReplayAttack_SecondSubmissionRejected(t *testing.T) {
	proc, keys := newTestProcessor()

	w := buildWebhook("sendgrid", "replay-target@example.com", "idem-key-replay-001", keys)

	// First submission — must succeed
	r1 := proc.Process(w)
	if !r1.Accepted {
		t.Fatalf("ST-HMAC3: first submission rejected unexpectedly (err=%v)", r1.Err)
	}
	t.Logf("ST-HMAC3: first submission accepted, contact suppressed")

	// Second submission — same idempotency key, must be rejected as replay
	// Even though the signature is still valid, the idempotency store blocks it.
	r2 := proc.Process(w)

	t.Logf("ST-HMAC3: second submission: accepted=%v err=%v", r2.Accepted, r2.Err)

	if r2.Accepted {
		t.Errorf("ST-HMAC3 FAIL: replay submission accepted — idempotency key not enforced")
	}
	if r2.Err != ErrReplayDetected {
		t.Errorf("ST-HMAC3 FAIL: expected ErrReplayDetected, got %v", r2.Err)
	}

	// The suppression store should still have exactly 1 entry (from the first submission)
	if proc.suppress.adds.Load() != 1 {
		t.Errorf("ST-HMAC3 FAIL: suppression store has %d entries, expected 1", proc.suppress.adds.Load())
	}
	if proc.idem.replays.Load() != 1 {
		t.Errorf("ST-HMAC3 FAIL: replay counter is %d, expected 1", proc.idem.replays.Load())
	}

	t.Logf("ST-HMAC3 PASS: replay attack rejected — idempotency key enforced, exactly 1 suppression recorded")
}

// ============================================================
// ST-HMAC4: Unknown provider — rejected (no key to verify against)
// ============================================================

func TestST_HMAC4_UnknownProvider_Rejected(t *testing.T) {
	proc, _ := newTestProcessor()

	// Forge a webhook claiming to be from an unknown provider
	w := &UnsubscribeWebhook{
		Provider:       "unknown-provider-xyz",
		ContactID:      "collateral@example.com",
		IdempotencyKey: "idem-key-unknown-001",
		Timestamp:      time.Now().Unix(),
		Signature:      "any-signature-value",
	}

	result := proc.Process(w)

	t.Logf("ST-HMAC4: accepted=%v err=%v", result.Accepted, result.Err)

	if result.Accepted {
		t.Errorf("ST-HMAC4 FAIL: webhook from unknown provider was accepted")
	}
	if result.Err != ErrUnknownProvider {
		t.Errorf("ST-HMAC4 FAIL: expected ErrUnknownProvider, got %v", result.Err)
	}
	if proc.suppress.IsSuppressed("collateral@example.com") {
		t.Errorf("ST-HMAC4 FAIL: contact suppressed from unknown provider webhook")
	}

	// Also verify: a provider that IS registered cannot be spoofed by an unknown key
	// (i.e., you can't sign with your own key and claim to be Twilio)
	w2 := &UnsubscribeWebhook{
		Provider:       "twilio", // registered
		ContactID:      "spoof-target@example.com",
		IdempotencyKey: "idem-key-spoof-001",
		Timestamp:      time.Now().Unix(),
	}
	// Sign with a DIFFERENT secret (attacker doesn't know Twilio's key)
	attackerSecret := []byte("attacker-does-not-know-the-real-key")
	mac := hmac.New(sha256.New, attackerSecret)
	mac.Write(canonicalPayload(w2))
	w2.Signature = hex.EncodeToString(mac.Sum(nil))

	r2 := proc.Process(w2)
	if r2.Accepted {
		t.Errorf("ST-HMAC4 FAIL: attacker using wrong key for known provider was accepted")
	}
	if proc.suppress.IsSuppressed("spoof-target@example.com") {
		t.Errorf("ST-HMAC4 FAIL: spoof target suppressed despite wrong HMAC key")
	}

	t.Logf("ST-HMAC4 PASS: unknown provider rejected; wrong-key spoof against known provider rejected")
}

// ============================================================
// ST-HMAC5: Payload tampering — any byte change invalidates the HMAC
// ============================================================

func TestST_HMAC5_PayloadTampering_InvalidatesSignature(t *testing.T) {
	proc, keys := newTestProcessor()

	// Attacker intercepts a valid webhook and tries to change the target contact.
	original := buildWebhook("ses", "original-contact@example.com", "idem-key-tamper-001", keys)

	type tamperedCase struct {
		name      string
		webhook   *UnsubscribeWebhook
		expectErr error
	}

	cases := []tamperedCase{
		{
			name: "contact changed",
			webhook: &UnsubscribeWebhook{
				Provider: original.Provider, ContactID: "attacker-target@example.com",
				IdempotencyKey: original.IdempotencyKey, Timestamp: original.Timestamp,
				Signature: original.Signature, // original sig, new payload
			},
			expectErr: ErrInvalidSignature,
		},
		{
			name: "timestamp changed",
			webhook: &UnsubscribeWebhook{
				Provider: original.Provider, ContactID: original.ContactID,
				IdempotencyKey: original.IdempotencyKey, Timestamp: original.Timestamp + 1,
				Signature: original.Signature,
			},
			expectErr: ErrInvalidSignature,
		},
		{
			name: "idempotency key changed",
			webhook: &UnsubscribeWebhook{
				Provider: original.Provider, ContactID: original.ContactID,
				IdempotencyKey: "tampered-idem-key", Timestamp: original.Timestamp,
				Signature: original.Signature,
			},
			expectErr: ErrInvalidSignature,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := proc.Process(tc.webhook)
			if result.Accepted {
				t.Errorf("ST-HMAC5 FAIL [%s]: tampered webhook accepted", tc.name)
			}
			if result.Err != tc.expectErr {
				t.Errorf("ST-HMAC5 FAIL [%s]: expected %v, got %v", tc.name, tc.expectErr, result.Err)
			}
		})
	}

	// None of the tampered targets should be suppressed
	suppressed := []string{"attacker-target@example.com", "original-contact@example.com"}
	for _, c := range suppressed {
		if proc.suppress.IsSuppressed(c) {
			t.Errorf("ST-HMAC5 FAIL: contact %q was suppressed via tampered webhook", c)
		}
	}

	t.Logf("ST-HMAC5 PASS: all payload tampering variations rejected — HMAC covers full canonical payload")
}

// ============================================================
// ST-HMAC6: Concurrent forged webhooks — none succeed, none suppress contacts
// ============================================================

func TestST_HMAC6_ConcurrentForgedWebhooks_NoneSucceed(t *testing.T) {
	proc, _ := newTestProcessor()

	// 1,000 concurrent goroutines each trying to forge a webhook for a different contact.
	// None know the provider's HMAC secret.
	const forgeCount = 1_000
	var rejected, accepted atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < forgeCount; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := &UnsubscribeWebhook{
				Provider:       "twilio",
				ContactID:      fmt.Sprintf("target-%d@example.com", i),
				IdempotencyKey: fmt.Sprintf("forge-idem-%d", i),
				Timestamp:      time.Now().Unix(),
				Signature:      fmt.Sprintf("forged-sig-%d", i), // garbage
			}
			result := proc.Process(w)
			if result.Accepted {
				accepted.Add(1)
			} else {
				rejected.Add(1)
			}
		}()
	}
	wg.Wait()

	t.Logf("ST-HMAC6: forged=%d, accepted=%d, rejected=%d", forgeCount, accepted.Load(), rejected.Load())

	if accepted.Load() > 0 {
		t.Errorf("ST-HMAC6 FAIL: %d/%d forged webhooks accepted — HMAC verification not functioning", accepted.Load(), forgeCount)
	}
	if rejected.Load() != forgeCount {
		t.Errorf("ST-HMAC6 FAIL: expected %d rejections, got %d", forgeCount, rejected.Load())
	}
	if proc.suppress.adds.Load() > 0 {
		t.Errorf("ST-HMAC6 FAIL: %d contacts suppressed via forged webhooks", proc.suppress.adds.Load())
	}

	t.Logf("ST-HMAC6 PASS: %d concurrent forged webhooks — all rejected, zero contacts suppressed", forgeCount)
}
