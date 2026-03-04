# Campaign Platform — Project Overview

> **Disclaimer:** This platform and all associated documentation, architecture diagrams, and design decisions are entirely hypothetical. No part of this project is based on, derived from, or affiliated with any real company, product, or organisation. This was designed as a personal exercise in systems thinking and large-scale architecture — an attempt to reason carefully about how enterprise-grade infrastructure is structured, what trade-offs it involves, and how real engineering constraints shape technical decisions at scale. The scenarios, numbers, and design choices reflect that intent. They are constructs for learning and exploration, not specifications for a real system.

---

## What This Is

A multi-tenant campaign management platform built to operate at trillion-scale data points. It handles the full lifecycle of customer communications across every major channel — email, SMS, WhatsApp, push notifications, and social media — for any number of enterprise tenants simultaneously, without any one tenant affecting another.

The platform is not a simple message queue with a front end. It is a deeply engineered delivery infrastructure designed to meet the reliability expectations of mission-critical communications: 2FA codes, transactional alerts, and time-sensitive campaign sends at a combined throughput exceeding 700,000 writes per second, with a 99.999% uptime target and a 15-minute recovery objective in the event of a full regional failure.

---

## The Problem It Solves

Enterprise marketing and communications teams need a single platform to:

- Build and manage audience segments across millions of contacts
- Orchestrate multi-step, multi-channel campaigns with branching logic
- Send time-sensitive transactional messages (2FA, account alerts) alongside bulk marketing sends without contention
- Track delivery outcomes in real time and feed those signals back into audience intelligence
- Stay compliant with GDPR, CAN-SPAM, and regional opt-out regulations automatically
- Operate across dozens of enterprise tenants with complete data isolation between them

Existing solutions fail at extreme scale because they treat all traffic equally, use a single data store for everything, and have no defined behaviour when components degrade. This platform is designed from the ground up for the assumption that things will fail, and that graceful degradation is not an afterthought — it is a first-class architectural requirement.

---

## Who It Serves

**Enterprise tenants** — large organisations running marketing campaigns, transactional notifications, and customer engagement programs. Each tenant operates in full isolation: their data, their rate limits, their campaign state, and their delivery receipts are never mixed with another tenant's.

**Campaign managers** — the operators within each tenant organisation who build segments, author templates, schedule sends, and monitor delivery outcomes through the authoring UI.

**Developer integrations** — external systems that push events into the platform via the API, consuming the Developer API with versioned, stable contracts and documented deprecation timelines.

**End recipients** — the contacts in each tenant's audience who receive emails, SMS messages, push notifications, and social communications. Their preferences, suppression status, and consent records are treated as the most important data in the system.

---

## Core Capabilities

### Multi-Channel Delivery

The platform delivers across six channel types through a tiered provider model. Every channel has a primary provider, a secondary fallback, and in the case of email and SMS, a tertiary fallback. Provider failover is automatic, governed by a Provider Budget Manager that enforces per-tenant rate quotas during mass failover so that one tenant cannot consume the entire fallback capacity.

| Channel | Primary | Secondary | Tertiary |
|---|---|---|---|
| Email | AWS SES | SendGrid | Mailgun |
| SMS | Twilio | Vonage | MessageBird |
| WhatsApp | Twilio | Vonage | — |
| Push (Android) | FCM | — | — |
| Push (iOS) | APNs | — | — |
| Social | Meta Graph API | LinkedIn API | — |

### Priority Traffic Separation

Not all messages are equal. A 2FA code that fails to arrive locks a user out of their account. A bulk marketing campaign that arrives three minutes late is inconsequential. The platform separates these into distinct traffic lanes with separate infrastructure:

**Gold Lane** — for 2FA, account alerts, and transactional messages. 120 Kafka partitions, dedicated consumer cluster, bypasses the standard rate limiter, never shed under load. Guaranteed delivery or SRE is paged.

**Silver Lane** — for bulk campaign sends. 600 Kafka partitions, HPA-autoscaled consumers (10 to 500 pods), subject to backpressure and load shedding. Delivery is durable but not instant.

### Audience Intelligence

The platform maintains a live feedback loop between delivery outcomes and audience segmentation. Engagement signals — opens, clicks, bounces, delivery confirmations — flow from the delivery workers through Flink stream processing into ClickHouse, and from there back into the CRM's segment scoring. This means audience segments improve automatically over time without manual intervention.

### Compliance by Default

Compliance is not a feature that can be toggled off. Every component in the delivery path checks the suppression list before a message is sent. Hard bounces are registered automatically. GDPR erasure requests propagate to every storage tier — hot analytics, warm datalake, cold archive, suppression database — within defined SLA windows (30 days for hot and warm tiers, 90 days for cold). Opt-out webhooks from providers are verified with HMAC-SHA256 before being processed. The platform is designed so that a contact who has unsubscribed cannot receive a message, not because a developer remembered to check a flag, but because the architecture makes it structurally impossible to bypass.

---

## Expected Application Features

These are the features a user of this platform would actually interact with, derived directly from what the architecture is built to support.

### Campaign Builder
- Drag-and-drop UI for building multi-step, multi-channel campaigns
- Template authoring with versioning — you can roll back to a previous template version
- Audience segment selection at the time of campaign creation, with live contact count previews
- Send-window configuration per campaign — set timezone-aware delivery windows so messages arrive at the right local time
- Campaign scheduling with staggered burst control, so a large send doesn't hammer provider rate limits all at once
- Campaign status tracking in real time: draft → running → done / failed, with delivery counts updating live

### Audience Segmentation
- Build segments based on contact attributes, engagement history, and behavioural signals
- Segments automatically update over time as the ML feedback loop refines engagement scores
- Manual segment overrides for compliance-driven exclusions
- Suppression list management — view, import, and audit who is suppressed and why

### Multi-Channel Message Delivery
- Send campaigns across email, SMS, WhatsApp, push notifications (iOS and Android), and social platforms from a single interface
- Per-channel configuration: DKIM signing for email, number pool management for SMS, OAuth token management for social
- Automatic provider failover — if the primary provider is unavailable, the platform switches to the secondary without any user action required
- Delivery receipt tracking per message: sent, delivered, bounced, failed

### Transactional and 2FA Messaging
- Dedicated high-priority sending path for time-critical messages like 2FA codes and account alerts
- These messages are completely isolated from bulk campaign traffic and are never delayed by a marketing send in progress
- Latency target: p99 under 50ms from send trigger to provider handoff

### Delivery Analytics Dashboard
- Live delivery stats during an active campaign send — not a refresh-on-demand report, but a real-time feed
- Per-campaign breakdown: total sent, delivered, bounced, open rate, click rate
- Historical analytics for completed campaigns with trend views
- Provider-level delivery health visibility — see if a specific provider is underperforming

### Compliance and Opt-Out Management
- One-click unsubscribe handling — opt-outs received from providers are processed automatically and immediately
- GDPR right-to-erasure requests: submit a contact for deletion and the platform propagates the deletion across every data store within the defined SLA
- Hard bounce auto-suppression — a contact that hard bounces is automatically added to the suppression list
- Audit log of all suppression events, erasure requests, and compliance actions

### API and Developer Integration
- REST API with versioned endpoints (/v1, /v2) for external systems to push campaign triggers and contact events
- Webhook ingress for receiving delivery status callbacks from external systems
- Stable API contracts with deprecation notices and sunset date headers — breaking changes are announced well in advance
- Developer documentation with migration guides per major version

### Tenant Administration
- Full data isolation per tenant — no tenant can see or affect another tenant's data, rate limits, or campaign state
- Per-tenant quota management — admins can view and adjust send rate limits
- Role-based access control within a tenant — campaign managers, read-only analysts, and administrators have different permission levels
- Tenant onboarding and offboarding with full data erasure on offboard

### Observability for Platform Operators
- Live Kafka consumer lag dashboards per lane
- Circuit breaker state visibility per delivery provider
- DLQ depth monitoring with alerts and manual replay controls
- End-to-end trace for any individual message — from API ingress through Kafka through delivery worker to provider receipt

---

## Scale Targets

| Metric | Target |
|---|---|
| Uptime | 99.999% (≤ 5.26 minutes downtime per year) |
| Recovery Time Objective | 15 minutes (full region failover) |
| Recovery Point Objective | 1 hour (maximum data loss window) |
| CRM write throughput | 200,000 TPS (32-shard Citus cluster) |
| Event store write throughput | 500,000+ TPS (Google Bigtable) |
| Analytics ingest | 2,000,000 rows per second (ClickHouse, 8 shards) |
| Gold Lane p99 latency | < 50ms |
| Identity lookup p99 latency | < 50ms |
| Total Kafka partitions | 720 (Gold 120 + Silver 600) |
| Delivery worker autoscaling | 10 to 500 pods per channel |
| GDPR erasure SLA (hot/warm) | 30 days |
| GDPR erasure SLA (cold) | 90 days |

---

## Cloud Footprint

The platform runs across three cloud providers and a global edge network, chosen for the specific strengths of each:

**Cloudflare** handles all global edge traffic — CDN, DDoS absorption at L3/L4 and L7, WAF, bot management — at 200+ points of presence worldwide. Traffic is absorbed and filtered before it reaches any origin infrastructure.

**AWS** (us-east-1 primary, us-west-2 standby) runs the ingress and execution layers — EKS delivery clusters, ElastiCache Redis state clusters, S3 data overflow and Iceberg datalake, Glacier cold archive, Route53 failover orchestration, and all audit and compliance tooling.

**GCP** (us-central1 primary, europe-west4 replica) owns the data layer — Citus/AlloyDB as the CRM, Bigtable as the high-frequency event store, Memorystore for identity caching, Cloud Spanner for campaign status, Dataflow/Flink for stream analytics, ClickHouse Cloud for hot tier analytics, and Vertex AI for ML audience scoring.

**Azure** contributes Purview for data governance and PII lineage, Entra ID for employee identity and privileged access management, and the compliance reporting layer.

**Confluent Cloud** runs the event bus across both AWS and GCP — Gold and Silver Kafka clusters, Schema Registry, and MirrorMaker 2 cross-region replication — with Amazon MSK as a cold standby activated only if Confluent becomes unavailable.

---

## Operational Model

The platform is owned and operated by six teams, each responsible for a defined layer:

| Team | Owned Layer |
|---|---|
| Infra / NetSec | Global edge, CDN, DDoS, TLS, service mesh |
| Platform / Infra | Ingress, identity, rate limiting, suppression |
| Data Platform | CRM, event store, schema, CDC, PII, erasure |
| Messaging Infra | Kafka clusters, DLQ, replay, overflow |
| Delivery Engineering | Execution engine, workers, circuit breakers, receipts |
| Analytics Engineering | Flink, ClickHouse, storage tiers, ML pipeline |
| SRE | Observability, alerting, DR drills, runbooks |

Every alert links to a runbook. Every runbook is owned by a named team. Every failure mode that has been identified has a documented recovery procedure. The DR Drill Program validates the full failover path quarterly, with chaos injection monthly and a full game day bi-annually.

---

## What Makes This Different

Most campaign platforms are built for the 99th percentile of load and patched when they break. This platform is designed for the scenario where a provider fails, a region goes offline, a schema migration runs during peak traffic, and three tenants trigger mass unsubscribes simultaneously — all at once.

The degradation behaviour is explicit and contractual: 2FA and transactional messages are never shed. Campaign sends are shed under load before analytics are affected. ML feedback signals are shed before campaign sends. Every component knows what it yields first, and the system as a whole degrades predictably rather than collapsing.
