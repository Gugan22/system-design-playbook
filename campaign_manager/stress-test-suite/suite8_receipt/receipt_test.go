// Package suite8_receipt tests the delivery receipt reconciliation path.
//
// Architecture decision tested:
//   After every send, the platform expects a provider delivery webhook within
//   15 minutes. If no receipt arrives, a sweep job queries the provider API
//   directly. If still unresolved after the sweep → DLQ.
//
// Tests:
//   ST-REC1: Receipt arrives before timeout → status written, no sweep triggered.
//   ST-REC2: Receipt missing at 15min → sweep fires, status resolved.
//   ST-REC3: Receipt missing AND sweep fails → message goes to DLQ (not silently dropped).
//   ST-REC4: Hard bounce receipt → contact added to suppression DB automatically.
package suite8_receipt

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================
// Types
// ============================================================

type ReceiptStatus string

const (
	StatusPending   ReceiptStatus = "pending"
	StatusSent      ReceiptStatus = "sent"
	StatusDelivered ReceiptStatus = "delivered"
	StatusBounced   ReceiptStatus = "bounced"
	StatusFailed    ReceiptStatus = "failed"
	StatusDLQ       ReceiptStatus = "dlq"
)

type Message struct {
	ID         string
	TenantID   string
	SentAt     time.Time
	Status     ReceiptStatus
	ContactID  string
}

// ProviderAPI is a stub for the provider's status query API (used by sweep job).
type ProviderAPI struct {
	// responses maps messageID → status returned by the provider API.
	responses map[string]ReceiptStatus
	mu        sync.RWMutex
	// queryCount tracks how many times the sweep queried the provider.
	queryCount atomic.Int64
}

func NewProviderAPI() *ProviderAPI {
	return &ProviderAPI{responses: make(map[string]ReceiptStatus)}
}

func (p *ProviderAPI) SetResponse(msgID string, status ReceiptStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses[msgID] = status
}

func (p *ProviderAPI) Query(msgID string) (ReceiptStatus, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	p.queryCount.Add(1)
	s, ok := p.responses[msgID]
	return s, ok
}

// ============================================================
// Status DB (Cloud Spanner stub)
// ============================================================

type StatusDB struct {
	mu      sync.RWMutex
	records map[string]ReceiptStatus
	writes  atomic.Int64
}

func NewStatusDB() *StatusDB {
	return &StatusDB{records: make(map[string]ReceiptStatus)}
}

func (db *StatusDB) Write(msgID string, status ReceiptStatus) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.records[msgID] = status
	db.writes.Add(1)
}

func (db *StatusDB) Read(msgID string) (ReceiptStatus, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	s, ok := db.records[msgID]
	return s, ok
}

// ============================================================
// Suppression DB stub (for hard bounce handling)
// ============================================================

type SuppressionDB struct {
	mu          sync.RWMutex
	suppressed  map[string]bool
	autoAdds    atomic.Int64
}

func NewSuppressionDB() *SuppressionDB {
	return &SuppressionDB{suppressed: make(map[string]bool)}
}

func (db *SuppressionDB) Add(contactID string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.suppressed[contactID] = true
	db.autoAdds.Add(1)
}

func (db *SuppressionDB) IsSuppressed(contactID string) bool {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.suppressed[contactID]
}

// ============================================================
// DLQ stub
// ============================================================

type DLQ struct {
	mu    sync.Mutex
	items []string
	depth atomic.Int64
}

func (d *DLQ) Enqueue(msgID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.items = append(d.items, msgID)
	d.depth.Add(1)
}

func (d *DLQ) Contains(msgID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, id := range d.items {
		if id == msgID {
			return true
		}
	}
	return false
}

// ============================================================
// Receipt Reconciler
// ============================================================

// ReconcilerConfig drives the reconciler's timing behaviour.
type ReconcilerConfig struct {
	// ReceiptTimeout: how long to wait for a webhook before triggering sweep.
	// Production: 15 minutes. Tests use shorter values.
	ReceiptTimeout time.Duration
	// SweepInterval: how often the sweep job polls for unresolved messages.
	SweepInterval time.Duration
}

type ReceiptReconciler struct {
	cfg         ReconcilerConfig
	statusDB    *StatusDB
	suppressDB  *SuppressionDB
	providerAPI *ProviderAPI
	dlq         *DLQ

	mu       sync.Mutex
	pending  map[string]*Message // messages awaiting receipt
	stopCh   chan struct{}
	stopOnce sync.Once

	sweepsFired  atomic.Int64
	receiptsHit  atomic.Int64
	dlqEnqueued  atomic.Int64
}

func NewReconciler(cfg ReconcilerConfig, statusDB *StatusDB, suppress *SuppressionDB, api *ProviderAPI, dlq *DLQ) *ReceiptReconciler {
	r := &ReceiptReconciler{
		cfg:         cfg,
		statusDB:    statusDB,
		suppressDB:  suppress,
		providerAPI: api,
		dlq:         dlq,
		pending:     make(map[string]*Message),
		stopCh:      make(chan struct{}),
	}
	go r.sweepLoop()
	return r
}

// Track registers a message as pending receipt.
func (r *ReceiptReconciler) Track(msg *Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msg.Status = StatusPending
	r.pending[msg.ID] = msg
}

// ReceiveWebhook processes an inbound delivery receipt from a provider.
func (r *ReceiptReconciler) ReceiveWebhook(msgID string, status ReceiptStatus) {
	r.mu.Lock()
	msg, ok := r.pending[msgID]
	if ok {
		msg.Status = status
		delete(r.pending, msgID)
	}
	r.mu.Unlock()

	if !ok {
		return // unknown message — ignore
	}

	r.statusDB.Write(msgID, status)
	r.receiptsHit.Add(1)

	// Hard bounce → auto-add to suppression DB
	if status == StatusBounced {
		r.suppressDB.Add(msg.ContactID)
	}
}

// sweepLoop runs on SweepInterval and queries the provider for any message
// that has been pending longer than ReceiptTimeout.
func (r *ReceiptReconciler) sweepLoop() {
	ticker := time.NewTicker(r.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.sweep()
		}
	}
}

func (r *ReceiptReconciler) sweep() {
	now := time.Now()
	r.mu.Lock()
	var timedOut []*Message
	for _, msg := range r.pending {
		if now.Sub(msg.SentAt) >= r.cfg.ReceiptTimeout {
			timedOut = append(timedOut, msg)
		}
	}
	r.mu.Unlock()

	for _, msg := range timedOut {
		r.sweepsFired.Add(1)
		status, found := r.providerAPI.Query(msg.ID)
		if found {
			// Resolved by sweep — write status and remove from pending
			r.mu.Lock()
			delete(r.pending, msg.ID)
			r.mu.Unlock()
			r.statusDB.Write(msg.ID, status)
			if status == StatusBounced {
				r.suppressDB.Add(msg.ContactID)
			}
		} else {
			// Sweep could not resolve — send to DLQ
			r.mu.Lock()
			delete(r.pending, msg.ID)
			r.mu.Unlock()
			r.statusDB.Write(msg.ID, StatusDLQ)
			r.dlq.Enqueue(msg.ID)
			r.dlqEnqueued.Add(1)
		}
	}
}

func (r *ReceiptReconciler) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
}

func (r *ReceiptReconciler) PendingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// ============================================================
// ST-REC1: Receipt Arrives Before Timeout — No Sweep Triggered
// ============================================================

func TestST_REC1_ReceiptBeforeTimeout(t *testing.T) {
	cfg := ReconcilerConfig{
		ReceiptTimeout: 200 * time.Millisecond,
		SweepInterval:  50 * time.Millisecond,
	}
	statusDB := NewStatusDB()
	suppress := NewSuppressionDB()
	api := NewProviderAPI()
	dlq := &DLQ{}
	r := NewReconciler(cfg, statusDB, suppress, api, dlq)
	defer r.Stop()

	msg := &Message{ID: "msg-001", TenantID: "tenant-1", ContactID: "contact-1", SentAt: time.Now()}
	r.Track(msg)

	// Receipt arrives well before the 200ms timeout
	time.Sleep(50 * time.Millisecond)
	r.ReceiveWebhook("msg-001", StatusDelivered)

	time.Sleep(300 * time.Millisecond) // let sweep run — should find nothing to sweep

	status, ok := statusDB.Read("msg-001")
	t.Logf("ST-REC1: status=%s, sweep_fires=%d, provider_queries=%d",
		status, r.sweepsFired.Load(), api.queryCount.Load())

	if !ok {
		t.Errorf("ST-REC1 FAIL: status not written to DB")
	}
	if status != StatusDelivered {
		t.Errorf("ST-REC1 FAIL: expected Delivered, got %s", status)
	}
	if api.queryCount.Load() > 0 {
		t.Errorf("ST-REC1 FAIL: sweep queried provider despite receipt arriving on time (%d queries)", api.queryCount.Load())
	}
	if r.PendingCount() != 0 {
		t.Errorf("ST-REC1 FAIL: message still pending after receipt")
	}
	t.Logf("ST-REC1 PASS: receipt resolved before timeout, no sweep triggered")
}

// ============================================================
// ST-REC2: No Receipt After Timeout — Sweep Fires and Resolves
// ============================================================

func TestST_REC2_SweepResolvesAfterTimeout(t *testing.T) {
	cfg := ReconcilerConfig{
		ReceiptTimeout: 100 * time.Millisecond,
		SweepInterval:  30 * time.Millisecond,
	}
	statusDB := NewStatusDB()
	suppress := NewSuppressionDB()
	api := NewProviderAPI()
	dlq := &DLQ{}
	r := NewReconciler(cfg, statusDB, suppress, api, dlq)
	defer r.Stop()

	// Provider knows the status — it just didn't send a webhook
	api.SetResponse("msg-002", StatusSent)

	msg := &Message{ID: "msg-002", TenantID: "tenant-1", ContactID: "contact-2", SentAt: time.Now()}
	r.Track(msg)

	// Wait longer than timeout — sweep should trigger and resolve
	time.Sleep(250 * time.Millisecond)

	status, ok := statusDB.Read("msg-002")
	t.Logf("ST-REC2: status=%s, sweep_fires=%d, provider_queries=%d",
		status, r.sweepsFired.Load(), api.queryCount.Load())

	if !ok {
		t.Errorf("ST-REC2 FAIL: status not written after sweep")
	}
	if status != StatusSent {
		t.Errorf("ST-REC2 FAIL: expected Sent (from provider query), got %s", status)
	}
	if r.sweepsFired.Load() == 0 {
		t.Errorf("ST-REC2 FAIL: sweep never fired")
	}
	if api.queryCount.Load() == 0 {
		t.Errorf("ST-REC2 FAIL: provider API never queried by sweep")
	}
	if r.PendingCount() != 0 {
		t.Errorf("ST-REC2 FAIL: message still pending after sweep resolved it")
	}
	t.Logf("ST-REC2 PASS: sweep resolved unacknowledged message via provider query")
}

// ============================================================
// ST-REC3: Sweep Cannot Resolve → DLQ (Never Silent Drop)
// ============================================================

func TestST_REC3_UnresolvableGoesToDLQ(t *testing.T) {
	cfg := ReconcilerConfig{
		ReceiptTimeout: 100 * time.Millisecond,
		SweepInterval:  30 * time.Millisecond,
	}
	statusDB := NewStatusDB()
	suppress := NewSuppressionDB()
	api := NewProviderAPI() // no response registered — sweep will fail
	dlq := &DLQ{}
	r := NewReconciler(cfg, statusDB, suppress, api, dlq)
	defer r.Stop()

	msg := &Message{ID: "msg-003", TenantID: "tenant-1", ContactID: "contact-3", SentAt: time.Now()}
	r.Track(msg)

	time.Sleep(300 * time.Millisecond)

	status, ok := statusDB.Read("msg-003")
	inDLQ := dlq.Contains("msg-003")

	t.Logf("ST-REC3: status=%s, in_dlq=%v, sweep_fires=%d", status, inDLQ, r.sweepsFired.Load())

	if !ok {
		t.Errorf("ST-REC3 FAIL: no status record written (silent drop)")
	}
	if status != StatusDLQ {
		t.Errorf("ST-REC3 FAIL: expected DLQ status, got %s", status)
	}
	if !inDLQ {
		t.Errorf("ST-REC3 FAIL: message not enqueued to DLQ (silent drop)")
	}
	if r.PendingCount() != 0 {
		t.Errorf("ST-REC3 FAIL: message still in pending after DLQ enqueue")
	}
	t.Logf("ST-REC3 PASS: unresolvable message goes to DLQ — never silently dropped")
}

// ============================================================
// ST-REC4: Hard Bounce → Auto-Added to Suppression DB
// ============================================================

func TestST_REC4_HardBounceAutoSuppresses(t *testing.T) {
	cfg := ReconcilerConfig{
		ReceiptTimeout: 500 * time.Millisecond,
		SweepInterval:  100 * time.Millisecond,
	}
	statusDB := NewStatusDB()
	suppress := NewSuppressionDB()
	api := NewProviderAPI()
	dlq := &DLQ{}
	r := NewReconciler(cfg, statusDB, suppress, api, dlq)
	defer r.Stop()

	// 5 messages: 2 hard bounces, 3 delivered
	for i := 0; i < 5; i++ {
		msg := &Message{
			ID:        fmt.Sprintf("msg-%03d", i),
			TenantID:  "tenant-1",
			ContactID: fmt.Sprintf("contact-%d", i),
			SentAt:    time.Now(),
		}
		r.Track(msg)
		if i < 2 {
			r.ReceiveWebhook(msg.ID, StatusBounced)
		} else {
			r.ReceiveWebhook(msg.ID, StatusDelivered)
		}
	}

	// Verify suppression
	for i := 0; i < 5; i++ {
		contact := fmt.Sprintf("contact-%d", i)
		suppressed := suppress.IsSuppressed(contact)
		if i < 2 && !suppressed {
			t.Errorf("ST-REC4 FAIL: contact-%d (hard bounce) not auto-suppressed", i)
		}
		if i >= 2 && suppressed {
			t.Errorf("ST-REC4 FAIL: contact-%d (delivered) incorrectly suppressed", i)
		}
	}

	t.Logf("ST-REC4: auto_suppress_adds=%d", suppress.autoAdds.Load())
	if suppress.autoAdds.Load() != 2 {
		t.Errorf("ST-REC4 FAIL: expected 2 auto-suppression adds, got %d", suppress.autoAdds.Load())
	}
	t.Logf("ST-REC4 PASS: hard bounces auto-suppressed (%d), delivered contacts clean", suppress.autoAdds.Load())
}
