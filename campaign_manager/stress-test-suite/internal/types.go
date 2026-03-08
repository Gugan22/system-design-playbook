// Package internal contains shared types, mock infrastructure, and test
// harness utilities used across all stress-test suites.
package internal

import (
	"sync/atomic"
	"time"
)

// MessageID is a unique identifier for a single send event.
type MessageID string

// TenantID identifies an isolated tenant in the multi-tenant platform.
type TenantID string

// Provider is an external delivery provider (FCM, APNs, SES, etc.).
type Provider string

const (
	ProviderFCM         Provider = "fcm"
	ProviderAPNs        Provider = "apns"
	ProviderSES         Provider = "ses"
	ProviderSendGrid    Provider = "sendgrid"
	ProviderMailgun     Provider = "mailgun"
	ProviderTwilio      Provider = "twilio"
)

// Channel classifies the message delivery channel.
type Channel string

const (
	ChannelPush  Channel = "push"
	ChannelEmail Channel = "email"
	ChannelSMS   Channel = "sms"
)

// Lane identifies which Kafka lane the message belongs to.
type Lane string

const (
	LaneGold   Lane = "gold"   // 2FA / transactional — never starved
	LaneSilver Lane = "silver" // bulk campaigns
)

// Message is the unit of work flowing through the platform.
type Message struct {
	ID         MessageID
	TenantID   TenantID
	Provider   Provider
	Channel    Channel
	Lane       Lane
	Payload    []byte
	EnqueuedAt time.Time
	// IdempotencyKey is client-generated before the first send attempt.
	IdempotencyKey string
}

// SendResult records the outcome of a single send attempt.
type SendResult struct {
	MessageID  MessageID
	TenantID   TenantID
	Provider   Provider
	Sent       bool   // false = duplicate detected or suppressed
	FailOpen   bool   // true = dedup tier was down, sent anyway
	DuplicateOf MessageID
	Latency    time.Duration
	Error      error
}

// Counters is a thread-safe set of atomic metrics for test assertions.
type Counters struct {
	Sent           atomic.Int64
	Blocked        atomic.Int64
	Duplicates     atomic.Int64
	FailOpen       atomic.Int64
	CBOpen         atomic.Int64 // circuit breaker open events
	Rejected503    atomic.Int64 // WAC 503s issued
	SuppressHit    atomic.Int64 // suppression blocked a send
}

func (c *Counters) Reset() {
	c.Sent.Store(0)
	c.Blocked.Store(0)
	c.Duplicates.Store(0)
	c.FailOpen.Store(0)
	c.CBOpen.Store(0)
	c.Rejected503.Store(0)
	c.SuppressHit.Store(0)
}
