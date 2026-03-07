# Platform Architecture
## Multi-Tenant Campaign & Communications Platform

> **Who this is for:** Anyone joining the team — engineering, product, or ops. No background knowledge required. This document explains what the system does, how it's organised, every major decision that shaped it, and why those decisions were made.

---

## Table of Contents

1. [What This Platform Does](#1-what-this-platform-does)
2. [The Seven-Layer Structure](#2-the-seven-layer-structure)
3. [Layer 0 — Security & Policy](#3-layer-0--security--policy)
4. [Layer 1 — Ingress & Identity](#4-layer-1--ingress--identity)
5. [Layer 2 — Data Layer](#5-layer-2--data-layer)
6. [Layer 3 — The Event Bus](#6-layer-3--the-event-bus)
7. [Layer 4 — The Execution Engine](#7-layer-4--the-execution-engine)
8. [Layer 5 — Analytics & Intelligence](#8-layer-5--analytics--intelligence)
9. [Layer 6 — Observability & Disaster Recovery](#9-layer-6--observability--disaster-recovery)
10. [All Critical Design Decisions](#10-all-critical-design-decisions)
11. [How a Message Travels End-to-End](#11-how-a-message-travels-end-to-end)
12. [How the System Handles Failure](#12-how-the-system-handles-failure)
13. [Scale Targets at a Glance](#13-scale-targets-at-a-glance)

---

## 1. What This Platform Does

This platform lets enterprise customers — called **tenants** — send messages to millions of their own users across every major channel: email, SMS, WhatsApp, push notifications, and social media.

Think of it as a shared messaging infrastructure that thousands of businesses rent simultaneously, each fully isolated from the others.

The platform handles **two fundamentally different kinds of traffic** at the same time:

| Traffic Type | Real Example | Why It Matters |
|---|---|---|
| **Transactional / 2FA** | "Your login code is 482916. It expires in 5 minutes." | A delayed 2FA code = user locked out of their account. Must arrive in seconds. |
| **Campaign / Marketing** | "Your summer sale starts tomorrow — shop now." | Can wait a few minutes. Volume is enormous. |

The most important rule in the entire system: **a bulk marketing campaign can never delay a 2FA code.** This is enforced in hardware (separate Kafka clusters), not just in configuration.

**Key numbers:**

| Metric | Target |
|---|---|
| Uptime | 99.999% — less than 6 minutes downtime per year |
| CRM write throughput | 200,000 writes/second |
| Event store write throughput | 500,000+ writes/second |
| Analytics ingest rate | 2,000,000 rows/second |
| Recovery time after full region failure | 15 minutes |

---

## 2. The Seven-Layer Structure

The platform is divided into seven layers. Each layer has one job and one team that owns it. If something breaks, you know exactly which team to call.

```
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 0 — Security, Policy & Compliance                        │
│  What: DDoS protection, WAF, opt-out handling, degradation rules│
│  Owner: Infra / NetSec                                          │
├─────────────────────────────────────────────────────────────────┤
│  LAYER 1 — Ingress & Identity                                   │
│  What: Auth, rate limiting, suppression, tenant routing         │
│  Owner: Platform / Infra                                        │
├─────────────────────────────────────────────────────────────────┤
│  LAYER 2 — Data Layer                                           │
│  What: CRM, event store, PII protection, GDPR erasure           │
│  Owner: Data-Platform                                           │
├─────────────────────────────────────────────────────────────────┤
│  LAYER 3 — The Event Bus                                        │
│  What: Kafka (Gold + Silver lanes), DLQ, overflow handling      │
│  Owner: Messaging-Infra                                         │
├─────────────────────────────────────────────────────────────────┤
│  LAYER 4 — The Execution Engine                                 │
│  What: Workers that send messages to email/SMS/push providers   │
│  Owner: Delivery-Eng                                            │
├─────────────────────────────────────────────────────────────────┤
│  LAYER 5 — Analytics & Intelligence                             │
│  What: Flink stream processing, storage tiers, ML feedback      │
│  Owner: Analytics-Eng                                           │
├─────────────────────────────────────────────────────────────────┤
│  LAYER 6 — Observability & DR                                   │
│  What: Monitoring, alerting, runbooks, regional failover        │
│  Owner: SRE                                                     │
└─────────────────────────────────────────────────────────────────┘
```

The platform spans multiple clouds — each chosen for what it does best:

- **Cloudflare** — global edge, DDoS protection, WAF, TLS
- **AWS** — ingress servers, Kubernetes workers, Redis clusters, S3, Glacier
- **GCP** — CRM database, event store, campaign API, stream processing, analytics
- **Azure** — employee identity (Entra ID), data governance (Purview)
- **Confluent Cloud** — managed Kafka (runs across both AWS and GCP)

---

## 3. Layer 0 — Security & Policy

**One-line summary:** The rules that apply to the entire platform, regardless of tenant or channel.

### Global Edge Network

All internet traffic enters through **Cloudflare Enterprise** before reaching any of our servers.

Cloudflare handles:
- **DDoS absorption** — blocks L3/L4 network floods and L7 application attacks (HTTP floods, Slowloris, credential stuffing)
- **Web Application Firewall (WAF)** — enforces OWASP Top 10 rules, geo-blocking, per-IP rate limits, payload size enforcement
- **TLS 1.3 termination** — every connection is encrypted from the user's device to our systems

After Cloudflare, traffic passes through an AWS NLB (Network Load Balancer) in TCP passthrough mode, then to AWS API Gateway for payload validation.

> **Why Cloudflare only — no AWS Shield?**
>
> AWS Shield and Cloudflare Enterprise protect against the same threats. Running both means every single request passes through two separate security systems — doubling per-request cost, adding latency, and providing zero additional protection. CF Enterprise is the sole DDoS/WAF provider. One layer, done correctly.

### Service Mesh — mTLS East-West

Inside the platform, every service-to-service call is secured using **mutual TLS (mTLS)** via Istio/Linkerd. Each service proves its identity before another service will talk to it.

Important distinction: mTLS is **east-west only** (between internal services). Cloudflare handles the external (north-south) TLS. The AWS NLB passes traffic as pure TCP — no second TLS termination, no redundant handshake on every request.

### Degradation Priority Policy

When the platform is under load and can't serve all traffic, it sheds load in a fixed, hard-coded priority order. Nobody decides this at 3am — the system decides automatically.

| Priority | Traffic | Rule |
|---|---|---|
| **Tier 1** | 2FA and transactional | **Never shed.** If this can't be served, wake up SRE immediately. |
| **Tier 2** | Campaign sends | Shed only after Tiers 3 and 4 are already shed. |
| **Tier 3** | Analytics pipeline | Shed before campaign sends slow down. Dashboards go stale — acceptable. |
| **Tier 4** | ML feedback signals | Shed freely. Slightly stale audience scores are a minor inconvenience. |

The rate limiter watches Kafka Silver Lane consumer lag as its signal. When lag exceeds 5 million messages, it automatically starts shedding Tier 3, then 4.

> **Why hard-code priority instead of letting engineers decide during an incident?**
>
> Engineers making judgment calls under stress at 3am make inconsistent decisions. Hard-coding priority makes the degradation contract explicit, auditable, and automatic. It also means every tenant knows exactly what the SLA is: your 2FA codes will always get through, even when we're under attack.

### Unsubscribe Webhook Receiver

Provider opt-out webhooks (from email/SMS providers signalling that a contact unsubscribed) are verified with **HMAC-SHA256 signature + IP allowlist** before processing. An unverified webhook could silently suppress thousands of legitimate contacts. Verification is mandatory, not optional.

---

## 4. Layer 1 — Ingress & Identity

**One-line summary:** Every request enters here, gets authenticated, stamped with a tenant ID, and routed to the right place.

### The Auth Chain

Every single request — without exception — passes through this chain:

```
AUTH → JWT Revocation Check → Tenant Context Stamp → Cell Router
```

**Step 1: AUTH (Keycloak/Auth0)**
Validates the JWT token. Checks RBAC permissions. Enforces MFA.

**Step 2: JWT Revocation Check (Redis blocklist)**
Even valid, non-expired tokens can be immediately blocked here. This is used when an admin account is offboarded or a token is suspected compromised. The check happens in Redis — sub-millisecond.

**Step 3: Tenant Context Injector**
Stamps `tenant_id` on every request. This is how data isolation is enforced throughout the entire system. Downstream services read the tenant from context — they never trust tenant identity from client input.

**Step 4: Cell Router (Envoy)**
Routes the request to the right tenant cell. Splits traffic into two paths: priority (2FA) or standard (campaigns).

### Two Paths After the Router

**Priority path — 2FA and transactional:**
```
Fast Ingestor (dedicated node pool) → Fast-Path Suppression → Gold Kafka Lane
```
- Bypasses the rate limiter entirely. A 2FA message cannot wait in a queue.
- Suppression check uses a Bloom filter as a performance hint (explained below).
- Goes directly onto the Gold Kafka Lane.

**Standard path — campaigns:**
```
Rate Limiter → Suppression Check → Campaign API → CRM write → CDC → Silver Kafka Lane
```
- Subject to per-tenant rate limiting and automatic backpressure.
- Full Postgres suppression lookup.
- Campaign API runs on GCP (co-located with the CRM) — same-cloud write, no cross-cloud latency.

### Suppression: Bloom Filter + Postgres

Before any message is sent, the platform checks: "Has this contact opted out?"

There are two layers, and they serve different purposes:

**Layer 1 — Bloom Filter (performance hint only, NOT a compliance gate)**
An in-memory structure (~1.2GB for 1 billion entries at 1% FPR). It can answer "this contact is *definitely not* suppressed" in sub-millisecond time with zero database lookup. If it gives that answer, we skip the Postgres check.

**Layer 2 — Postgres (the compliance gate)**
The only layer that matters for GDPR and CAN-SPAM compliance.

> **The most important constraint about the Bloom filter:**
>
> A Bloom filter is **append-only**. You can add entries but you cannot remove them. When someone unsubscribes, their entry cannot be deleted from the Bloom filter.
>
> This means:
> - An unsubscribe event **always** forces a Postgres lookup — the Bloom cannot safely answer "definitely not suppressed" for that contact.
> - The GDPR compliance guarantee lives entirely in Postgres. The Bloom is a latency optimisation, not a compliance tool.
> - The sync direction is Postgres → Redis on write. Never the reverse. Postgres always wins on conflict.

---

## 5. Layer 2 — Data Layer

**One-line summary:** Stores everything the platform needs to know — contacts, campaigns, delivery status — and handles PII protection and GDPR erasure.

### Two Databases for Two Different Access Patterns

No single database efficiently handles both workloads, so we use two:

**CRM — Citus/AlloyDB on GCP:**
- Stores contact records, audience segments, campaign configurations, tenant admin data
- 32 shards, keyed by `tenant_id`
- Strong consistency — the source of truth for identity and compliance
- Write ceiling: 200,000 TPS

**Append Store — Google Bigtable:**
- Stores high-frequency engagement events: opens, clicks, bounces
- Append-only, no updates
- Change stream feeds the Kafka Silver Lane via CDC
- Write ceiling: 500,000+ TPS

> **Why not one database?** Engagement events arrive at 500,000+ TPS in append-only bursts. Routing those writes into the CRM would saturate its connection pool and make normal contact queries unpredictable. Bigtable is built precisely for this access pattern. The two stores are complementary: CRM owns identity and structure, Bigtable owns event history.

### Write Admission Controller

Before any write reaches PgBouncer (the connection pooler), it passes through the Write Admission Controller. When the queue fills past 80% capacity, it rejects new writes with `503 + Retry-After` instead of letting them pile up.

> **Why reject with a 503 instead of queuing?**
>
> A 503 with a Retry-After header tells the client exactly what happened and exactly when to retry safely. A silent timeout after 30 seconds leaves the client with no information — it doesn't know if the write landed or not. Silent timeouts cause duplicate writes and corrupted state. Explicit rejections enable safe idempotent retries.

### Campaign Status DB (Cloud Spanner)

Tracks every campaign's lifecycle state: `draft → running → done / failed`. Globally consistent. Updated in real time by delivery workers. Queried by dashboards for live campaign stats.

On DR failover, the Saga State Redis cluster (which tracks in-flight message steps) rebuilds from this Spanner database.

### PII Protection

All personally identifiable data is **tokenized at write time** by the PII Masking layer. The token maps to an AES-256 encryption key stored in the PII Token Registry (GCP Secret Manager). This tokenization is the foundation of the cold-tier GDPR erasure strategy — see below.

### GDPR Erasure — Seven Tiers, Seven Mechanisms

When a contact submits a deletion request, the Erasure Controller cascades across all seven storage tiers. The mechanism is specific to each tier — not a generic `DELETE WHERE user_id = X`, because that would cause severe problems in several tiers.

| Tier | Mechanism | Why This Specific Mechanism |
|---|---|---|
| CRM (AlloyDB) | Soft-delete + async partition drop | A full-table scan across 32 shards is extremely expensive. Soft-delete is instant; the async partition drop runs off-peak. |
| Bigtable (Event Store) | `DeleteFromRow` API, row-key scoped | Native Bigtable operation. Does not lock adjacent rows. |
| ClickHouse (Hot Analytics) | `ALTER TABLE DROP PARTITION` by PII token | A naive `DELETE` on ClickHouse creates an async MergeTree mutation that locks compaction cluster-wide. DROP PARTITION is instant and clean. |
| S3 Iceberg (Warm Analytics) | GDPR row-delete manifest + Iceberg row-delete | No object rewrite needed. The manifest records the deletion. |
| S3 Glacier (Cold Archive) | **Revoke the PII token in Token Registry** | Glacier objects are never retrieved or modified. Revoking the AES-256 key in Secret Manager makes all Glacier data for that contact permanently unreadable in-place. Legal basis: Art.17(3)(b) — aggregate data can be retained, personal data must be inaccessible. |
| Campaign Status DB (Spanner) | Rate-limited delete | Standard structured delete. |
| Suppression DB | Rate-limited delete | Standard structured delete. |

Rate limit: 10,000 operations/minute, per-tenant queue. SLA: 30 days for hot/warm tiers, 90 days for cold (token revoke within 24 hours).

> **Why rate-limit erasure if GDPR gives 30 days?**
>
> An enterprise tenant offboarding at scale could simultaneously spike ClickHouse merge CPU, saturate Iceberg partition rewrites, and flood Glacier batch jobs. We have 30 days — there is no operational reason to rush. Rate limiting protects cluster health while still meeting the SLA by a wide margin.

---

## 6. Layer 3 — The Event Bus

**One-line summary:** All asynchronous communication flows through Kafka. This layer decouples writes from sends and handles overflow.

### Two Kafka Lanes

The single most important infrastructure decision in the platform is keeping 2FA traffic and campaign traffic on **completely separate Kafka clusters with separate consumer pools**.

| | **Gold Lane** | **Silver Lane** |
|---|---|---|
| Purpose | 2FA and transactional only | Bulk campaign sends |
| Partitions | 120 (RF=3) | 600 (RF=3) |
| Consumers | Dedicated cluster — never shared | HPA-scaled 10–500 pods |
| Lag alert | >50,000 → SRE paged | >5,000,000 → SRE paged |
| Overflow | **None.** Gold overflow = P0 capacity incident. Disk >70% → partition scale immediately. | S3 Iceberg spill at lag >1M. Re-entry within 15 minutes. |
| Retention | 72 hours | 7 days |

> **Why can't we use priority headers on one cluster?**
>
> Kafka does not honour priority headers for consumption order. The consumer processes events in partition order regardless of what metadata the message carries. The only way to guarantee 2FA events are consumed before bulk campaign events is to put them in a cluster with a dedicated consumer pool that has no knowledge of the Silver Lane. Two clusters. No exceptions.

### Topic Authorization

Confluent RBAC enforces per-topic, per-service-account access. Producers have write-only access. Consumers have read-only access. Every message envelope must carry a validated `tenant_id` header. A message without one is rejected at the consumer.

### Dead Letter Queue and Replay

Messages that fail three consecutive delivery attempts are routed to the **Dead Letter Queue (DLQ)**.

A DLQ Replay Service can re-inject them back into the appropriate lane (Gold failures → Gold, Silver failures → Silver). But it will not do so automatically.

> **Why does replay require a manual root-cause gate?**
>
> Automatic replay into a still-broken system is a disaster amplifier. The messages fail again, return to the DLQ, get replayed again — an infinite loop that increases load on a struggling system. The manual gate forces an engineer to confirm that the underlying problem is actually resolved before the backlog is re-injected. Global replay cap: 5,000 msg/s total. Per-tenant cap: 200 msg/s.

### MSK Fallback (Cold Standby)

Amazon MSK (managed Kafka) is a cold standby for Confluent. MirrorMaker 2 continuously replicates the **Silver Lane only** from Confluent to MSK. Gold Lane is not replicated — its volume is small and Confluent's RF=3 is sufficient.

On failover from Confluent to MSK, MirrorMaker 2's offset translation creates a ~15-minute window where events that were already processed will be re-consumed. The dedup tiers absorb this — but only if they are warm. This is the problem solved by the DEDUP_SHADOW worker described in Layer 6.

---

## 7. Layer 4 — The Execution Engine

**One-line summary:** Consumes events from Kafka and sends messages to external providers (SES, Twilio, FCM, APNs, etc).

### Two Orchestration Paths

**SAGA (Temporal.io) — for 2FA and transactional:**
- Multi-step orchestration with compensating transactions (if a step fails, it can undo previous steps safely)
- Idempotent steps — every step is safe to retry
- Maximum saga lifetime: 24 hours
- Bypasses the Send-Time Optimizer (2FA cannot wait for timezone scheduling)
- Goes directly to the Circuit Breaker

**CHOREO (Kafka consumers) — for bulk campaigns:**
- Stateless HPA-scaled workers, 10–500 pods
- Reads from Silver Lane
- Passes through Send-Time Optimizer (timezone scheduling, burst staggering)
- Then Circuit Breaker

### Three-Tier Deduplication

Every message passes a dedup check before sending. This prevents duplicate sends when a message is retried after failure or when the DR region takes over from a failed primary.

```
L1 In-Process LRU (per worker, ~512MB)
  → cache miss → L2 Counting Bloom Filter (~72GB)
    → confirmed duplicate → L3 Slim Redis (~14GB, confirmed dupes only)
```

**Why three tiers and not just Redis?**

A naive full-keyspace Redis dedup at 10 billion events/day requires **1.4 TB** of working set. The largest available ElastiCache instance is 416 GB. When Redis evicts keys under memory pressure to free space, it discards dedup records — and the next time that event is replayed, the dedup check passes and the message is sent again. That's a silent duplicate send.

The three-tier approach solves this:
- **L1** handles same-worker retries for free (sub-ms, zero network)
- **L2** (Counting Bloom, not a standard Bloom) handles cross-worker dedup across 10B keys at 72 GB — not 1.4 TB. "Counting" means it supports TTL expiry by decrementing counters, not just setting bits.
- **L3** stores only confirmed duplicate hits — approximately 1% of the naive full-keyspace size.

All three tiers **fail-open**: if a tier is unavailable, the send proceeds and the event is logged for audit. A missed dedup is far better than blocking all sends.

### Circuit Breaker — Per-Tenant, Per-Provider

The Circuit Breaker (CB) protects external providers. When error rates exceed a threshold, the CB opens and traffic routes to fallback providers.

**The critical design choice: one CB instance per tenant per provider — not one shared CB.**

With a shared CB, one tenant sending a malformed push notification batch (which causes FCM to return errors) would open the circuit for all 5,000 tenants in the cell. That is a cell-wide push blackout caused by a single tenant's bad payload. With per-tenant CBs, only that tenant's circuit opens. The other 4,999 tenants are completely unaffected.

At 5,000 tenants × 2 push providers = **10,000 CB instances**. Memory cost: ~2KB each = **~20MB total**. The blast radius protection is worth 20MB.

**Push providers need different tuning than email/SMS:**

APNs routinely returns HTTP 429 (Too Many Requests) on legitimate burst traffic. FCM has per-app rate limits that vary by registration token age. A CB tuned for email's steady error profile (50% error rate threshold, 10-second window) will false-trip constantly on normal push behaviour.

| Channel | Error Window | Open Threshold | Reason for Difference |
|---|---|---|---|
| Push (FCM / APNs) | 30 seconds | 80% | Push providers are bursty; tight thresholds = constant false trips |
| Email / SMS / Social | 10 seconds | 50% | More predictable, steady error profile |

Half-open probe: 30 seconds. A single probe request checks whether the provider has recovered before fully re-closing the circuit.

The SAGA 2FA thread pool is isolated from CHOREO bulk threads. A bulk campaign flooding the CB cannot affect 2FA sends.

### Token Bucket — Per-Tenant, Per-Provider

Enforces rate limits per tenant per provider. Key format: `{tenant_id}:{provider}`. One tenant's burst cannot exhaust another tenant's rate quota. No extra Redis nodes needed — logical isolation via key-space partitioning in the existing rate cluster.

### Provider Fallback Chains

| Channel | Primary | Secondary | Tertiary |
|---|---|---|---|
| Email | AWS SES | SendGrid | Mailgun |
| SMS / WhatsApp | Twilio | Vonage | MessageBird |
| Push Android | FCM | — | No fallback |
| Push iOS | APNs | — | No fallback |
| Social | Meta Graph API | LinkedIn | No fallback |

Push and social have no alternate providers. When a CB opens on push, that tenant's messages go to the DLQ. There is no silent drop.

### Delivery Receipt Reconciler

After every send, the platform expects a delivery receipt from the provider (webhook):
- `sent` / `delivered` / `bounced` / `failed`
- Hard bounces are automatically added to the Suppression DB
- Status is written to the Campaign Status DB
- If no receipt arrives within 15 minutes, a scheduled sweep job queries the provider API directly
- Unresolvable after the sweep → DLQ

### Canary Deployments — Tenant-Aware

New code rolls out in tenant-aware waves, not a random 5%:

| Wave | Who Gets It | % of Traffic |
|---|---|---|
| Wave 1 | Small tenants only | ~5% |
| Wave 2 | Medium tenants | ~25% |
| Wave 3 | All tenants including enterprise | 100% |

> **Why tenant-aware instead of random 5%?**
>
> At trillion-event scale, a random 5% could include a large enterprise tenant's campaign launch. A deployment bug during that launch is a catastrophic failure for your most important customer. Tenant-aware waves mean a deployment bug hits small, low-stakes tenants in Wave 1. By Wave 3, you have high confidence. Large tenants only see new code after it's been validated.

All new versions must pass a load test at **2x target TPS** before promotion to production.

---

## 8. Layer 5 — Analytics & Intelligence

**One-line summary:** Turns delivery events and engagement signals into live dashboards, ML audience tuning, and the long-term compliance archive.

### Stream Processing — Apache Flink (Exactly-Once)

Flink processes all delivery events from workers in real time. It uses **exactly-once semantics** — every event is processed exactly one time, not more, not less.

> **Why exactly-once instead of at-least-once?** (At-least-once is cheaper and simpler.)
>
> Because Flink's output feeds the ML segment scoring model. If events are duplicated in the analytics pipeline, engagement scores get inflated. Inflated scores include the wrong contacts in future campaigns. At 0.1% false-positive rate on a trillion-event stream, that's 1 billion phantom engagement events corrupting audience decisions. Exactly-once costs more but produces correct data.

Before writing to any analytics tier, all events pass through the **PII Anonymizer**: strips all personal data, replaces contact identities with tokenized IDs. No PII ever reaches the analytics storage tiers.

### Three Storage Tiers

| Tier | Technology | Retention | Purpose |
|---|---|---|---|
| **Hot** | ClickHouse (8-shard, RF=3, MergeTree) | 30 days | Live dashboards, real-time tenant queries, 2M rows/sec ingestion |
| **Warm** | AWS S3 + Apache Iceberg (Parquet) | 2 years | Historical analytics, batch ETL every 6 hours |
| **Cold** | AWS S3 Glacier Deep Archive | 7 years | Regulatory compliance retention only |

> **ClickHouse is NOT cross-region replicated.**
>
> Writing 2 million rows/second with synchronous cross-region replication requires a dedicated network link and significantly increases write latency on every analytics event. Analytics dashboards do not justify this cost. After a regional failover, dashboards show stale data until ClickHouse is rebuilt from the S3 warm tier. This is a documented, accepted tradeoff — it's in the runbook.

### ML Feedback Loop

Engagement signals flow from Flink → ML Gateway (Vertex AI) → ML Signal Buffer (Kafka topic, 1h TTL) → CRM segment tuning.

The signal buffer absorbs outages in the ML service — signals are buffered for up to an hour and drain automatically when the service recovers. If the drop rate exceeds 5% in 10 minutes, an alert fires.

---

## 9. Layer 6 — Observability & Disaster Recovery

**One-line summary:** Watches everything, alerts when something is wrong, and coordinates regional failover when a cloud region goes offline.

### What Gets Monitored

The SLA Monitor aggregates signals from all telemetry sources and fires alerts. Every alert links to a runbook. Every runbook is owned by a named team.

Key alert thresholds:

| Signal | Alert Threshold |
|---|---|
| Gold Lane consumer lag | > 50,000 messages → page SRE |
| Silver Lane consumer lag | > 5,000,000 messages → page SRE |
| DLQ message depth | > 1,000 messages → alert |
| Circuit breaker open rate | Per-cell threshold |
| Worker error rate / p99 latency | Per-channel threshold |
| Provider fallback spend | > 2× primary spend |
| ML signal drop rate | > 5% in 10 minutes |
| Flink checkpoint time | > 120 seconds |
| Receipt sweep misses | Unresolved after 15 minutes |
| **DR dedup shadow-warm lag** | **> 30 seconds → P1 alert** (this alert fires *before* any failover is needed — it tells you your RTO is about to degrade) |

### Disaster Recovery

**What happens when a cloud region goes down:**

1. Route53 health checks detect the failure in under 60 seconds
2. Standby services in us-west-2 (AUTH, Rate Limiter, Suppression DB) are promoted
3. MSK (the Kafka cold standby) takes over from Confluent
4. Channel Workers in the DR region start — but only after DEDUP_SHADOW confirms the dedup state is warm

**Recovery Time Objective (RTO):**
- **15 minutes** — if DEDUP_SHADOW lag is under 30 seconds at the time of failover
- **30 minutes** — if DEDUP_SHADOW is lagged and needs to catch up first

### The Cold Bloom Problem (and Why It Matters)

The dedup tiers (L1 LRU, L2 Counting Bloom, L3 Redis) exist only in the primary region. On a failover they are empty. MirrorMaker 2's offset translation creates a ~15-minute window where events already processed will be re-consumed from MSK.

With empty dedup tiers, every one of those re-consumed events fires as a duplicate send. At the platform's scale, that is approximately **104 million duplicate messages per failover event** — including duplicate 2FA OTPs. A 2FA duplicate is not just an annoyance; it's a security incident (the original OTP was consumed by the user, but a second one now exists in flight).

**The fix: DEDUP_SHADOW worker**

A dedicated worker runs **continuously** in the DR region (us-west-2):
- Consumes from MSK (which receives the Silver Lane via MirrorMaker 2)
- Writes only to the DR copy of the L2 Counting Bloom
- Never triggers any sends — it only keeps the Bloom filter current
- Lag target: under 30 seconds

Channel Workers in the DR region have a **hard startup gate** on DEDUP_SHADOW lag. The workers will not start until DEDUP_SHADOW confirms lag < 30 seconds. This is enforced as a health-check dependency — it cannot be skipped under pressure the way a runbook step can.

**Cost: ~$400/month** for the DR ElastiCache instance. This is the cost of preventing 104 million duplicate sends per failover.

DR drills: chaos injection monthly (Gremlin), full failover quarterly, full game day bi-annually.

---

## 10. All Critical Design Decisions

Every major architectural decision, the problem it solved, the alternative that was rejected, and what was given up.

---

### Decision 1: Two Kafka Clusters (Gold + Silver)

**Problem:** 2FA codes and bulk campaigns cannot share Kafka infrastructure. A big campaign hammering 600 partitions will starve a 2FA message.

**What we tried first:** Priority headers on one cluster. Kafka ignores them for consumption order. Useless.

**What we did:** Two separate clusters with separate consumer pools. Gold can never be starved by Silver.

**What we gave up:** Two clusters to operate and monitor instead of one.

---

### Decision 2: Three Isolated Redis Clusters

**Problem:** JWT revocation, dedup, saga state, and rate limiting all need Redis. One cluster means one failure takes down all of them at once.

**What we did:** Three clusters — each with its own failure domain and a defined recovery path.

**What we gave up:** More infra to run.

---

### Decision 3: Tiered Dedup (L1 LRU + L2 Counting Bloom + L3 Slim Redis)

**Problem:** Full-keyspace Redis dedup at 10B events/day = 1.4 TB working set. Doesn't fit in one ElastiCache instance. Eviction = silent duplicate sends.

**What we did:** Three tiers that collectively use ~87 GB instead of 1.4 TB, with fail-open semantics at every tier.

**What we gave up:** More complex dedup code path.

---

### Decision 4: Per-Tenant Per-Provider Circuit Breakers

**Problem:** One shared CB means one tenant's bad push batch causes a cell-wide push blackout for all 5,000 tenants.

**What we did:** 10,000 CB instances (5k tenants × 2 push providers). ~20 MB total. Blast radius = 1 tenant.

**What we gave up:** 10,000 CB instances to aggregate-monitor. Still trivial — one alert aggregates all of them.

---

### Decision 5: Push CB Thresholds Differ From Email/SMS

**Problem:** APNs returns 429s on legitimate burst traffic. A CB tuned for email (50% threshold, 10s window) will constantly false-trip on normal push behaviour.

**What we did:** Push CB: 80% threshold, 30-second window. Email/SMS: 50% threshold, 10-second window.

**What we gave up:** Push provider outages take slightly longer to detect (higher error threshold before opening).

---

### Decision 6: Write Admission Controller (503 Instead of Silent Timeout)

**Problem:** On DB failover, writes queued in PgBouncer silently timeout. Client doesn't know if the write landed.

**What we did:** Reject writes with `503 + Retry-After` when queue > 80%. Explicit failure > silent timeout.

**What we gave up:** Clients see 503s during the DB failover window.

---

### Decision 7: DLQ Replay Requires a Manual Root-Cause Gate

**Problem:** Auto-replay into a still-broken system = infinite loop amplifying load. Every replayed message fails, goes back to DLQ, gets replayed again.

**What we did:** SRE must confirm root cause is resolved before replay starts. 5,000 msg/s global cap. 200 msg/s per-tenant cap.

**What we gave up:** Recovery is not fully automatic. Someone must press a button.

---

### Decision 8: Bloom Filter Is a Performance Hint — Postgres Is the Compliance Gate

**Problem:** The Bloom filter is append-only. Unsubscribes cannot be reflected in it.

**What we did:** Bloom answers "definitely not suppressed" → skip database lookup. All other cases → Postgres lookup. GDPR compliance lives entirely in Postgres.

**What we gave up:** Unsubscribe events always hit Postgres (cannot be cached in Bloom). Slight latency increase on the unsubscribe path.

---

### Decision 9: Per-Tier GDPR Erasure Mechanisms (Not Generic DELETE)

**Problem:** `DELETE WHERE user_id = X` on ClickHouse creates an async MergeTree mutation with a metadata lock that blocks compaction cluster-wide. Generic DELETE breaks production analytics.

**What we did:** Each tier has a specific mechanism. ClickHouse: DROP PARTITION by PII token. Cold/Glacier: revoke the encryption token — no Glacier retrieval, no data modification.

**What we gave up:** More complex erasure controller. Each tier requires different implementation and operational knowledge.

---

### Decision 10: ClickHouse Is Not Cross-Region Replicated

**Problem:** 2M rows/sec with synchronous cross-region replication requires a dedicated network link and increases write latency on every event.

**What we did:** ClickHouse is regional. After failover, dashboards are stale until rebuilt from S3 warm tier (hours, SRE-led).

**What we gave up:** Analytics unavailable during regional failover. This is documented and accepted.

---

### Decision 11: DEDUP_SHADOW Runs Continuously (Not On-Demand at Failover)

**Problem:** Replaying 15 minutes of Kafka into an empty L2 Bloom at failover time takes approximately 12 minutes. That pushes RTO from 15 to 27 minutes. The alternative without any warm-up is 104M duplicate sends.

**What we did:** A dedicated worker runs 24/7 in the DR region keeping the Bloom warm. Hard gate on Channel Worker startup.

**What we gave up:** ~$400/month in standby infrastructure costs.

---

### Decision 12: Campaign API Runs on GCP (Not AWS)

**Problem:** Campaign API on EKS (AWS) writing to AlloyDB on GCP = every campaign write is a synchronous cross-cloud call. Cross-cloud egress adds latency and creates a dependency on the AWS-GCP network interconnect. A GCP networking event can saturate AWS connection pools within seconds.

**What we did:** Campaign API runs on GKE, co-located with AlloyDB. Same-cloud write: p99 < 20ms. 2FA path is completely unaffected (it uses the Confluent Gold Lane, not Campaign API).

**What we gave up:** Campaign API uses GKE tooling instead of EKS tooling. The team supports both.

---

### Decision 13: Tenant-Aware Canary (Not Global 5%)

**Problem:** At trillion-event scale, a random 5% sample could include a large enterprise tenant's campaign launch. A deployment bug hits your most important customer.

**What we did:** Wave 1 is small tenants only. Large tenants don't see new code until Wave 2 or 3.

**What we gave up:** Deployments take longer to fully roll out.

---

### Decision 14: Flink Uses Exactly-Once Semantics

**Problem:** At-least-once is cheaper but duplicates events. 0.1% FPR at trillion scale = 1 billion phantom events inflating ML engagement scores.

**What we did:** Exactly-once. RocksDB incremental checkpointing every 60 seconds to GCS. Max state size 500 GB per Flink job.

**What we gave up:** Higher processing cost and slightly more complexity than at-least-once.

---

### Decision 15: Cloudflare Only — No AWS Shield

**Problem:** Running both Cloudflare Enterprise and AWS Shield adds per-request cost and latency for zero additional protection. They cover identical threat classes.

**What we did:** CF Enterprise is the sole DDoS/WAF provider.

**What we gave up:** Nothing. Both providers cover the same threats.

---

## 11. How a Message Travels End-to-End

### 2FA Login Code

```
1.  App calls Developer API: "send 2FA code to user X"
2.  Cloudflare: WAF check + TLS termination (~2ms)
3.  AWS API Gateway: payload validation (~5ms)
4.  AUTH: JWT validation + RBAC (~3ms)
5.  JWT Revocation check: Redis blocklist (<1ms)
6.  Tenant Context: tenant_id stamped on request
7.  Cell Router: detects priority traffic → routes to Fast Ingestor
8.  Fast Ingestor: bypasses rate limiter entirely
9.  Fast-Path Suppression (Bloom filter):
      → "definitely not suppressed" → skip Postgres
      → "maybe suppressed" → Postgres lookup
10. Published to GOLD Kafka Lane (~5ms)
11. SAGA (Temporal) picks up the event (~10ms)
12. SAGA → Circuit Breaker (per-tenant per-provider)
13. Circuit Breaker → Token Bucket (per-tenant per-provider)
14. Dedup check: L1 LRU → L2 Bloom → L3 Redis (fail-open)
15. Push Worker (WPUSH) sends to FCM or APNs
16. Provider receipt (delivery webhook) → Receipt Reconciler
17. Status written to Campaign Status DB (Spanner)
18. If hard bounce: → Suppression DB
19. Delivery event → Flink → PII Strip → ClickHouse Hot Tier
20. Live dashboard updates
```

**Target: p99 < 10 seconds from API call to provider handoff**

---

### Campaign Email

```
1.  Campaign manager sets up campaign in UI
2.  Campaign API (GKE, GCP) writes to CRM (AlloyDB, same-cloud) — p99 <20ms
3.  Debezium CDC publishes CRM change to SILVER Kafka Lane
4.  CHOREO workers consume the event from Silver Lane
5.  Send-Time Optimizer: applies timezone scheduling + staggered burst
6.  Circuit Breaker (per-tenant) → Token Bucket (per-tenant)
7.  Dedup check: L1 LRU → L2 Bloom → L3 Redis
8.  Email Worker (WEMAIL) sends to AWS SES
      → SES CB opens → SendGrid fallback (60% of SES quota)
      → SendGrid CB opens → Mailgun fallback (40% of SES quota)
9.  Provider receipt → Receipt Reconciler → Campaign Status DB
10. Hard bounce → Suppression DB (automatic)
11. Engagement events (opens, clicks) → Flink → PII Strip → ClickHouse
12. Live campaign analytics dashboard updates
13. ML feedback: Flink → Vertex AI → ML Signal Buffer → CRM (segment tuning)
```

---

## 12. How the System Handles Failure

| Failure | What Happens Automatically | Impact on Tenants |
|---|---|---|
| AWS SES goes down | CB opens → SendGrid fallback → Mailgun fallback | None — transparent failover |
| Redis dedup shard dies | Fail-open — send proceeds, logged | Near-zero risk of one duplicate send |
| Kafka Silver lag spikes | Rate limiter auto-sheds Tier 3 (analytics) then Tier 4 (ML) | Dashboards go stale — campaign sends fully unaffected |
| CRM primary dies | Write Admission Controller returns 503 + Retry-After | Clients retry safely — no silent data loss |
| Schema Registry outage | Producers use cached schemas (1-hour grace period) | No impact for up to 1 hour |
| Full cloud region fails | Route53 activates DR standbys (<60s), MSK takes over | 15–30 minute RTO |
| Confluent outage | MirrorMaker 2 failover to MSK | ~15 minutes potential duplicates absorbed by dedup (if DEDUP_SHADOW current) |
| One tenant sends bad push batch | That tenant's CB opens | Other 4,999 tenants completely unaffected |
| DLQ floods with one tenant's messages | Per-tenant 200 msg/s replay cap | Other tenants' replay budget protected |
| ClickHouse cluster failure | Dashboards go stale | SRE rebuilds from S3 warm tier (hours, not minutes) |
| DEDUP_SHADOW lag exceeds 30s | P1 alert fires — SRE can correct before a failover is even needed | None yet — alert is preventive |

---

## 13. Scale Targets at a Glance

| Metric | Target |
|---|---|
| Uptime | 99.999% — ≤ 5 min 26 sec downtime/year |
| RTO (DEDUP_SHADOW current) | 15 minutes |
| RTO (DEDUP_SHADOW lagged) | 30 minutes |
| RPO | 1 hour |
| CRM write throughput | 200,000 TPS (32-shard AlloyDB) |
| Event store write throughput | 500,000+ TPS (Bigtable) |
| Analytics ingest | 2,000,000 rows/second (ClickHouse, 8 shards) |
| Gold Lane lag alert | 50,000 messages |
| Silver Lane lag alert | 5,000,000 messages |
| Identity lookup SLA | < 50ms p99 |
| Kafka total partitions | 720 (Gold 120 + Silver 600) |
| Worker auto-scaling range | 10–500 pods per channel |
| Circuit Breaker instances | ~10,000 (5k tenants × 2 push providers) |
| CB total memory | ~20 MB |
| Dedup L2 Bloom size | ~72 GB |
| Dedup L3 Redis size | ~14 GB (confirmed dupes only) |
| Naive Redis dedup size (rejected) | 1.4 TB — doesn't fit in one instance |
| GDPR erasure rate cap | 10,000 ops/minute |
| GDPR SLA hot/warm | 30 days |
| GDPR SLA cold | 90 days (token revoke within 24h) |
| DR dedup shadow-warm lag target | < 30 seconds |
| DR shadow-warm monthly cost | ~$400 |
| Duplicate sends prevented per failover | ~104 million |
