# Infrastructure Architecture
## Multi-Tenant Campaign Platform — Operational Implementation

> **Who this is for:** Engineers, SREs, and anyone who needs to understand how the platform is actually built, deployed, and operated across AWS, GCP, Azure, and Cloudflare. The companion Platform Architecture doc covers *what* the system does. This doc covers *where it runs, on what technology, and why each component was chosen*.

---

## Table of Contents

1. [Cloud Responsibilities and Regions](#1-cloud-responsibilities-and-regions)
2. [Global Edge — Cloudflare Enterprise](#2-global-edge--cloudflare-enterprise)
3. [Ingress & Identity — AWS us-east-1 (Primary)](#3-ingress--identity--aws-us-east-1-primary)
4. [Data Layer — GCP us-central1](#4-data-layer--gcp-us-central1)
5. [Event Bus — Confluent Cloud + AWS MSK](#5-event-bus--confluent-cloud--aws-msk)
6. [Execution Engine — EKS + GKE](#6-execution-engine--eks--gke)
7. [External Providers & Fallback Chains](#7-external-providers--fallback-chains)
8. [Analytics & Intelligence — GCP + AWS S3](#8-analytics--intelligence--gcp--aws-s3)
9. [Observability & Disaster Recovery](#9-observability--disaster-recovery)
10. [Governance](#10-governance)
11. [Key Infrastructure Decisions](#11-key-infrastructure-decisions)
12. [DR Runbook](#12-dr-runbook)
13. [Latency Budget](#13-latency-budget)

---

## 1. Cloud Responsibilities and Regions

Each cloud was chosen because it is genuinely best at what it does in this platform — not for vendor consolidation.

| Cloud | Primary Region | DR / Replica | What It Owns |
|---|---|---|---|
| **Cloudflare** | Global (200+ PoPs) | — | Edge, DDoS, WAF, TLS termination, rate rules |
| **AWS** | us-east-1 | us-west-2 (warm standby) | EKS workers, ElastiCache, S3, Glacier, Route53, MSK fallback |
| **GCP** | us-central1 | europe-west4 (replica) | AlloyDB CRM, Bigtable, Cloud Spanner, GKE, Flink/Dataflow, Vertex AI, ClickHouse |
| **Azure** | — | — | Entra ID (employee SSO), Purview (data governance) |
| **Confluent** | Multi-cloud | — | Kafka Gold + Silver Lanes, Schema Registry, ML Signal Buffer |
| **AWS MSK** | us-west-2 | — | Cold Kafka standby only — activated on Confluent failure |

**Inter-cloud synchronous traffic is minimised by design.** The only cross-cloud synchronous path is delivery workers (EKS/AWS) writing engagement events to analytics (GCP). CRM writes, Campaign API calls, and CDC events are all same-cloud (GCP). Cross-cloud async traffic via S3 and Kafka replication is acceptable.

---

## 2. Global Edge — Cloudflare Enterprise

Every request from the public internet enters through Cloudflare before touching any origin server. This is non-negotiable — no direct access to AWS API Gateway.

### Cloudflare CDN

- **Anycast routing** — requests go to the nearest of 200+ PoPs worldwide, minimising round-trip time
- **TLS 1.3 termination at the PoP** — encrypted from user device to the PoP; Cloudflare private backbone to origin
- **Edge latency: ~2ms p99** (session reuse — no per-request TLS handshake)

### Cloudflare WAF

- OWASP Top 10 ruleset
- Bot management and credential stuffing detection
- Geo-blocking and per-IP rate rules
- Payload size limits and template injection prevention
- Single WAF plane — no downstream re-inspection by another WAF

### AWS NLB — TCP Passthrough

After Cloudflare, traffic reaches an AWS Network Load Balancer operating at **Layer 4 only**. It does not re-terminate TLS. It does not inspect application-layer content. It passes TCP packets through to AWS API Gateway.

Passthrough latency: **< 1ms**

> **Why no TLS re-termination at the NLB?**
>
> Cloudflare already terminated TLS at the edge PoP. Re-terminating at the NLB means two full TLS handshakes per connection — one at Cloudflare, one at the NLB. That adds latency on every request for zero security benefit. The certificate is managed by Cloudflare. The NLB is a pure TCP proxy. mTLS is enforced east-west (between internal services via Istio) — not at the edge.

### AWS API Gateway v2

- Route versioning: `/v1`, `/v2`
- API Lifecycle Manager: RFC 8594 Sunset headers on deprecated routes, migration guides per major version
- Payload validation before request reaches application code
- Auth forwarding to the AUTH service
- Latency: ~5ms

### Secrets Manager (AWS + HashiCorp Vault)

All service credentials are managed here. **Sidecar injection pattern**: credentials are injected into pods via a Vault sidecar — services never read secrets directly from environment variables or config files. Auto-rotation every 30 days.

### Service Mesh — Istio/Linkerd (East-West Only)

Enforces mTLS on all service-to-service traffic inside the cluster. Key facts:

- **Not at the edge** — Cloudflare owns edge TLS. Istio/Linkerd covers internal traffic only.
- **Session-level mutual auth** — not per-request TLS. Steady-state overhead: ~0.5ms/hop.
- Re-handshake only on pod restart, not on every RPC.
- Cross-cluster traffic (EKS ↔ GKE) goes over AWS ↔ GCP VPC peering, also mTLS.

### Compliance Gateway + Unsubscribe Webhook Receiver

Opt-out webhooks from email and SMS providers are received here. Before any opt-out is processed:

1. HMAC-SHA256 signature is verified (per-provider key)
2. Source IP is checked against the provider's allowlist
3. Idempotency key ensures the same unsubscribe event isn't processed twice

An unverified webhook could silently suppress thousands of contacts. Verification is mandatory before any suppression record is written.

---

## 3. Ingress & Identity — AWS us-east-1 (Primary)

All application traffic arrives here after passing the edge layer.

### Entry Points (EKS)

**UI / Authoring Service**
- Drag-and-drop campaign builder
- Template versioning
- Live campaign dashboard (fed by Flink stream in real time)
- Runs on EKS on-demand nodes (not Spot — UI availability is user-facing)

**Developer API**
- External integrations, webhook ingress, programmatic campaign triggers
- Versioned contracts with RFC 8594 sunset header enforcement

### The Auth Chain (EKS On-Demand)

Every request passes through these four steps in sequence. No shortcuts.

**AUTH — Keycloak/Auth0:**
OIDC, RBAC, JWT validation, MFA enforcement. Runs on on-demand nodes — auth must not be preempted by Spot interruption.

**JWT Revocation — ElastiCache Redis:**
Redis-backed blocklist. Enables immediate token revocation for admin offboarding or compromised credentials. Checked on every single request. TTL aligned to JWT expiry.

**Tenant Context Injector:**
Stamps `tenant_id` on every request. This is how the system enforces data isolation throughout. Downstream services read tenant from context — they never accept tenant identity from client-supplied data.

**Cell Router — Envoy Proxy:**
Routes to the correct tenant cell. Splits into two execution paths:
- Priority (2FA/transactional) → Fast Ingestor (dedicated node pool, bypasses rate limiter)
- Standard (campaigns) → Rate Limiter

### Rate Limiter (EKS + ElastiCache)

- Envoy + Redis token bucket, per-tenant quota
- Watches Kafka Silver Lane consumer lag
- When lag > 5 million messages: automatically starts shedding Tier 3 (analytics) traffic, then Tier 4 (ML)
- Tier 1 (2FA) and Tier 2 (campaigns) are never shed by the rate limiter

### Fast Ingestor — Priority Pool (EKS Gold-Pool)

- Dedicated EKS node pool — no shared scheduling with standard traffic
- 2FA and transactional traffic only
- Bypasses the rate limiter entirely
- `tenant_id` envelope validated at the Gold Kafka consumer
- Feeds directly to the Gold Lane

### Fast-Path Suppression (EKS)

In-memory Bloom filter: ~1.2 GB for 1 billion entries at 1% FPR.

The Bloom filter is a **performance hint only — not a compliance gate.**

| Bloom says | Action |
|---|---|
| "Definitely not in the suppression list" | Skip Redis + Postgres lookup entirely |
| "Maybe in the suppression list" | Force Postgres lookup |

Critical constraint: the Bloom filter is **append-only**. When a contact unsubscribes, their entry cannot be removed from the filter. An unsubscribe event always forces a Postgres lookup. The compliance guarantee lives entirely in Postgres — not in the Bloom.

Fail-open on Redis miss → falls back to Postgres as the source of truth.

### Suppression DB (ElastiCache + RDS Postgres)

- **Postgres:** source of truth and GDPR compliance gate
- **Redis:** read-through cache, max 30 seconds stale
- Sync direction: **Postgres → Redis on write.** Never the reverse.
- On Redis cold start (after failover): rebuilt from Postgres
- Bloom filter in Fast-Path Suppression is a hint; Postgres always wins on conflict

### Campaign API (GKE — co-located with CRM on GCP)

**This service runs on GKE (GCP), not EKS (AWS).** This is a deliberate placement decision.

> **Why Campaign API runs on GCP, not AWS:**
>
> The original design ran Campaign API on EKS. Every campaign creation, audience update, and template write was a synchronous cross-cloud call from AWS EKS to AlloyDB on GCP. Cross-cloud egress adds latency and creates a hard dependency on the health of the AWS ↔ GCP network interconnect. A GCP networking event could saturate AWS connection pools within seconds.
>
> Moving Campaign API to GKE makes all CRM writes same-cloud: p99 < 20ms. The 2FA path is completely unaffected — it uses the Confluent Gold Lane and never touches Campaign API.

### DR Standby — us-west-2

Warm standby activated by Route53 health-check failover:

- **AUTH2** — Keycloak read replica, promoted to primary on failover
- **DIST_QUOTA2** — Rate limiter standby, starts with conservative defaults
- **SUPPDB2** — Postgres streaming replica; Redis rebuilt from Postgres on activation

---

## 4. Data Layer — GCP us-central1

The system of record lives entirely on GCP. All structured data, event history, campaign state, and PII lives here.

### Write Path

**Write Admission Controller (GKE sidecar):**
Sits in front of PgBouncer. Rejects new writes with `503 + Retry-After` when the queue exceeds 80% capacity. Prevents the silent timeout scenario where a client doesn't know if its write landed.

> A 503 with Retry-After is always better than a silent timeout. The client knows exactly what happened and exactly when to retry safely. Silent timeouts produce duplicate writes and corrupted state.

**PgBouncer HA:**
Connection pooler for AlloyDB. NLB-fronted (no single point of failure). Max 10,000 client connections. Transaction pooling mode. Prevents Postgres connection exhaustion under burst load.

**CRM — Citus/AlloyDB (GCP AlloyDB):**
- 32 shards, `tenant_id` as the shard key
- AlloyDB read replicas per region
- PITR backups every 1 hour
- Streaming replica to GCP europe-west4
- Write ceiling: 200,000 TPS

**Append Store — Google Bigtable:**
- Stores high-frequency contact engagement events (opens, clicks, bounces)
- Append-only, no updates, no deletes in normal operation
- Change stream enabled → feeds Bigtable CDC → Silver Lane
- Write ceiling: 500,000+ TPS

**Online Schema Migration (GKE Job):**
`pgroll` / `pt-online-schema-change`. Zero-downtime migrations — the table stays live during the migration. A rollback plan is required for every migration before it is allowed to run.

### CDC and Schema Enforcement

**Debezium CDC (GKE):**
Transaction-bound outbox pattern on CRM. Streams CRM changes to the Silver Kafka Lane at-least-once.

**Bigtable CDC (GKE):**
Change stream connector. Streams contact events to Silver Lane, schema-validated via Confluent Schema Registry.

**Confluent Schema Registry (RF=3, clustered):**
Enforces Avro / Protobuf schemas on all Kafka producers. Breaking schema changes are rejected at deploy time, not at runtime. Producers cache schemas locally with a 1-hour TTL — if Schema Registry is unavailable, producers continue using cached schemas for up to 1 hour (fail-open). New schemas are registered on deploy, never lazily at runtime.

### Identity & Campaign State

**Identity SLA Validator (GKE):**
Checks every identity lookup against a 50ms SLA. Compliance-critical entities (offboarded tenants, GDPR erasure subjects): hard reject on stale data. Non-critical paths: circuit-break to stale cache (max 5 minutes stale). This is a guardrail against serving wrong data under load.

**Memorystore Redis — Global ID Map (GCP):**
Contact and tenant resolution cache. 3-replica cluster, TTL 5 minutes, LRU eviction. SLA: < 50ms.

**Cloud Spanner — Campaign Status DB:**
`draft → running → done / failed` per campaign. Tenant-partitioned, globally consistent, updated in real time by delivery workers. Also serves as the **rebuild source** for Saga State Redis on DR failover.

### Privacy & Compliance

**PII Masking Layer (GKE sidecar):**
All PII is tokenized at write time — before data reaches CRM or Bigtable. Field-level AES-256 encryption. Audit log on every access. Issues PII tokens to the Token Registry.

**PII Token Registry (GCP Secret Manager):**
Maps PII token → AES-256 encryption key. Replicated across 3 regions, RPO zero. Revoking a key makes all tokenized data referencing that token permanently unreadable — across all tiers, including Glacier, without any data retrieval.

**GDPR Erasure Controller (GKE):**
Rate-limited: 10,000 ops/minute, per-tenant queue. Mechanism is tier-specific:

| Tier | Mechanism | Key Reason |
|---|---|---|
| CRM (AlloyDB) | Soft-delete + async partition drop | No full-table scan on 32 shards |
| Bigtable | `DeleteFromRow` API, row-key scoped | Native, no adjacent row locks |
| ClickHouse (Hot) | `ALTER TABLE DROP PARTITION` by PII token | Naive DELETE = async MergeTree mutation = metadata lock blocking compaction cluster-wide |
| S3 Iceberg (Warm) | GDPR row-delete manifest + Iceberg row-delete | No object rewrite needed |
| S3 Glacier (Cold) | Revoke token in Token Registry | Glacier objects never retrieved. Token revocation makes them permanently unreadable. Legal basis: Art.17(3)(b). |
| Spanner (Status) | Rate-limited delete | Standard |
| Suppression DB | Rate-limited delete | Standard |

SLA: 30 days hot/warm · 90 days cold · token revoke SLA: 24 hours.

**Cloud DLP (GCP):**
Scheduled daily PII scan across CRM and Bigtable. Auto-detects unmasked PII fields and triggers the tokenization pipeline. A safety net for cases where PII masking failed at write time.

---

## 5. Event Bus — Confluent Cloud + AWS MSK

### Gold Lane (Confluent Cluster-A)

| Property | Value |
|---|---|
| Purpose | 2FA and transactional messages only |
| Partitions | 120, RF=3, ISR=2 |
| Retention | 72 hours |
| Overflow | **None.** Gold disk > 70% → P0 alert + immediate partition scale. Gold overflow is not normal ops — it is a capacity planning failure. |
| Lag alert | > 50,000 messages → SRE paged immediately |

### Silver Lane (Confluent Cluster-B)

| Property | Value |
|---|---|
| Purpose | All bulk campaign sends |
| Partitions | 600, RF=3, ISR=2 |
| Auto-scaling | Min 60 partitions at low load |
| Retention | 7 days |
| Overflow | S3 Iceberg spill triggered at lag > 1M messages. Re-entry via Hydration Worker within 15 minutes. Max 500 msg/s per tenant on re-entry. |
| Lag alert | > 5,000,000 messages → SRE paged |

### Topic Authorization — Confluent RBAC

Per-topic, per-service-account access control:
- Producers: write-only
- Consumers: read-only
- Every message envelope must carry a valid `tenant_id` header — validated at the consumer. A message without one is rejected.

This applies to Gold Lane, Silver Lane, and the ML Signal Buffer.

### Dead Letter Queue (Confluent topic)

Messages failing after 3 attempts land here. TTL: 7 days. Depth alert fires at 1,000 messages.

**DLQ Replay Service (EKS controller):**
- Global cap: 5,000 msg/s
- Per-tenant cap: 200 msg/s
- **Root-cause gate required.** SRE must confirm the underlying cause is resolved before replay starts.
- Gold failures re-injected to Gold. Silver failures to Silver.
- Full audit trail per replay event.

### MirrorMaker 2 + MSK Fallback

MirrorMaker 2 continuously replicates the **Silver Lane only** from Confluent to AWS MSK Serverless:

- Gold Lane is not replicated — its volume is small and Confluent RF=3 is sufficient.
- MM2 translates Confluent offsets → MSK offsets continuously.
- On failover to MSK: consumer groups restart from the translated offset.
- Up to 15 minutes of duplicate events are expected — absorbed by tiered dedup **if** the DR dedup state is warm.

**Critical:** Channel Workers in the DR region are gated on DEDUP_SHADOW lag < 30 seconds before starting. See Section 9 for the DEDUP_SHADOW design.

DR runbook for Kafka failover:
1. Verify DEDUP_SHADOW lag < 30 seconds
2. Start MSK consumers
3. Monitor Silver lag recovery
4. Verify DEDUP audit log for unexpected duplicate rate

---

## 6. Execution Engine — EKS + GKE

### Orchestration

**SAGA — Temporal.io (EKS On-Demand):**
Multi-step orchestration with compensating transactions for 2FA and transactional sends.
- 3-node Temporal Server, Postgres backend
- Idempotent steps: every step is safe to retry
- Saga TTL: 24-hour maximum lifetime
- **Bypasses Send-Time Optimizer** — 2FA cannot wait for timezone scheduling
- Goes directly to Circuit Breaker
- Runs on on-demand nodes — not Spot. A Spot interruption during a 2FA saga is unacceptable.

**CHOREO — Kafka Consumers (EKS Spot):**
Stateless bulk workers for campaign sends.
- HPA: 10–500 pods per channel
- Partition:pod ratio capped at 1.2x (prevents over-partitioning)
- Consumer group rebalance on pod scaling
- Passes through Send-Time Optimizer → Circuit Breaker
- Runs on Spot — PodDisruptionBudget: `maxUnavailable=1`, 90-second graceful drain on Spot interruption

**Send-Time Optimizer (EKS):**
- Timezone-aware scheduling (sends arrive at the right local hour)
- Staggered burst: max send rate per tenant to avoid provider rate limit spikes
- Tenant-aware canary routing for new deployments

**Provider Budget Manager (EKS):**
- Per-tenant fallback quota management
- Fallback quota: 60% of primary provider limit
- Cost alert when fallback spend exceeds 2× primary
- Prevents a single tenant from monopolising fallback capacity during a mass provider failure

### State Tier — 3 Isolated ElastiCache Clusters (Cross-AZ)

Three clusters with independent failure domains. A failure in one does not affect the others.

**Tiered Deduplication (replaces single Redis dedup):**

> Why three tiers and not one Redis cluster?
>
> A naive full-keyspace Redis dedup at 10B events/day requires 1.4 TB of working set. The largest available ElastiCache instance (r7g.16xlarge) has 416 GB of memory. When Redis evicts keys to free space under memory pressure, it discards dedup records silently — the next replay of that event fires as a duplicate send. The platform was unknowingly sending duplicates with no alert.

| Tier | Technology | Size | Latency | Role |
|---|---|---|---|---|
| **L1** | In-process LRU per worker | ~512 MB/worker | Sub-ms, zero network | Last 100k message IDs per worker. Absorbs same-worker retry storms instantly. |
| **L2** | Counting Bloom Filter (ElastiCache-backed) | ~72 GB total | < 1ms | 10B keys at 0.1% FPR. **Counting** (4-bit counters) supports TTL expiry by decrementing counters, not just setting bits. DR: shadow copy kept warm by DEDUP_SHADOW in us-west-2. |
| **L3** | Slim Redis — confirmed dupes only | ~14 GB | ~1ms | Stores only confirmed duplicate hits. 99% smaller than the full-key naive store. TTL 24h, 3-shard cross-AZ. |

Chain: L1 miss → check L2. L2 confirmed duplicate → record in L3. All 4 channel workers are gated by L1.
All tiers **fail-open** — a miss allows the send and logs it.

**REDIS_STATE — Saga State (ElastiCache, 3-shard, cross-AZ):**
Saga step tracking per in-flight message. On DR failover: rebuilds from Cloud Spanner (Campaign Status DB). Pull model — workers read state; Redis is passive.

**REDIS_RATE — Rate Buckets (ElastiCache, 3-shard, cross-AZ):**
Per-tenant per-provider rate buckets. Key: `{tenant_id}:{provider}`. 5,000 tenants × 3 providers = ~15,000 logical buckets with no extra nodes. On DR failover: resets to conservative defaults, ramps up gradually.

### Circuit Breaker — Per-Tenant, Per-Provider (EKS Sidecar)

Resilience4j. Protects external providers from error-rate spikes.

**Per-tenant, per-provider isolation:**

At 5,000 tenants × 2 push providers = **10,000 CB instances**. Memory cost: ~2 KB × 10,000 = **~20 MB total**.

Why this matters: with a shared CB, one tenant's malformed push batch causing FCM to return errors opens the CB for all 5,000 tenants in the cell. Cell-wide push blackout from a single payload bug. Per-tenant CBs isolate the blast radius to one tenant.

Per-provider isolation matters too: APNs returns 429s on legitimate burst traffic. FCM has per-app rate limits by token age. They fail differently. A single "push" CB cannot distinguish them.

| Channel | Error Window | Open Threshold | Half-Open Probe |
|---|---|---|---|
| Push (FCM, APNs) | 30 seconds | 80% | 30 seconds |
| Email (SES, SendGrid, Mailgun) | 10 seconds | 50% | 30 seconds |
| SMS (Twilio, Vonage, MessageBird) | 10 seconds | 50% | 30 seconds |
| Social (Meta, LinkedIn) | 10 seconds | 50% | 30 seconds |

SAGA 2FA thread pool is isolated from CHOREO bulk threads. Bulk campaign CB pressure cannot starve 2FA sends.

### Token Bucket — Per-Tenant, Per-Provider (EKS Sidecar)

Key format: `{tenant_id}:{provider}` in REDIS_RATE. One tenant's burst cannot exhaust another tenant's quota. No extra Redis nodes — key-space partitioning in the existing cluster.

### Channel Workers — EKS Spot

All four workers: `PodDisruptionBudget: maxUnavailable=1`, 90-second graceful drain on Spot interruption.

| Worker | Providers | On CB Open |
|---|---|---|
| WEMAIL | AWS SES → SendGrid → Mailgun | Route to next provider in chain |
| WSMS | Twilio → Vonage → MessageBird | Route to next provider in chain |
| WPUSH | FCM (Android), APNs (iOS) | No alternate provider. That tenant's messages → DLQ. No silent drop. |
| WSOCIAL | Meta Graph API, LinkedIn | No alternate provider. That tenant's messages → DLQ. |

All workers: pass L1 dedup gate before sending, read saga state from REDIS_STATE (pull model), feed delivery events to Flink after sending.

### Delivery Receipt Reconciler (EKS)

- Ingests provider delivery webhooks: `sent / delivered / bounced / failed`
- Writes status to Cloud Spanner (Campaign Status DB)
- Hard bounces → Suppression DB automatically
- AWS credentials: Vault sidecar injection. GCP credentials: Workload Identity Federation (short-lived SA token — no static service account keys).
- SLA: reconcile within 5 minutes

**Receipt Sweep (EKS CronJob):**
Every 10 minutes. Finds messages with no receipt after 15 minutes. Queries provider API directly for status. Unresolvable → DLQ.

### Deployment Safety

**Canary Controller — Argo Rollouts:**

| Wave | Tenants | Traffic |
|---|---|---|
| Wave 1 | Small tenants only | ~5% |
| Wave 2 | Medium tenants | ~25% |
| Wave 3 | All tenants including enterprise | 100% |

Auto-rollback on error spike. Tenant-aware — not a random 5% that might hit a large enterprise. Managed by Argo CD.

**Load Test Gate (k6 / Gatling):**
Must pass at 2× target TPS before promotion to production. Blocks each scale milestone.

---

## 7. External Providers & Fallback Chains

All provider calls are gated through the Circuit Breaker and Token Bucket before reaching the provider API. The Provider Budget Manager enforces per-tenant fallback quotas.

### Email

```
Primary:    AWS SES         (DKIM signing, dedicated IPs)
Fallback 1: SendGrid        (60% of SES quota limit)
Fallback 2: Mailgun         (40% of SES quota limit)
All CB open: → DLQ
```

### SMS / WhatsApp

```
Primary:    Twilio          (SMS + WhatsApp, delivery receipt loop)
Fallback 1: Vonage          (60% of Twilio cap)
Fallback 2: MessageBird     (per-country routing)
All CB open: → DLQ
```

### Push

```
Android:    FCM (Batch API v1)
iOS:        APNs (HTTP/2, cert rotation on 410 response)
No alternate providers.
CB open (per-tenant) → that tenant's messages → DLQ
Other tenants: completely unaffected
```

### Social

```
Facebook + Instagram:  Meta Graph API (OAuth token rotation)
LinkedIn:              LinkedIn Marketing API (OAuth token rotation)
No alternate providers.
CB open (per-tenant) → that tenant's messages → DLQ
```

---

## 8. Analytics & Intelligence — GCP + AWS S3

### Stream Processing — Apache Flink on GCP Dataflow

- **Exactly-once semantics** — because output feeds ML models. Duplicated events inflate engagement scores; 0.1% FPR at trillion scale = 1 billion phantom events.
- RocksDB incremental checkpointing every 60 seconds to GCS
- Checkpoint timeout alert: > 120 seconds
- Max state size per Flink job: 500 GB (forces job decomposition before state becomes unmanageable)
- Watermark: 30-second late arrival tolerance
- All events pass through the **PII Anonymizer (GKE Sidecar)** before any analytics write: strips PII, replaces with tokenized IDs. Zero PII guarantee in HOT/WARM/COLD tiers.

### ML Gateway — Vertex AI Pipelines

- Engagement scoring model
- Async segment tuning back to CRM
- **ML Signal Buffer** (Confluent Kafka topic, 1h TTL): absorbs ML service downtime without dropping signals
- Alert: drop rate > 5% in 10 minutes → fire immediately

### Storage Tiers

**Hot Tier — ClickHouse Cloud (GCP):**
- 8 shards, RF=3, MergeTree tables
- TTL: 30 days (auto-expiry)
- Write: 2,000,000 rows/second
- Node failure with RF=3: no data loss
- **Not cross-region replicated.** Synchronous replication at 2M rows/sec requires a dedicated network link and increases write latency on every analytics event. Analytics dashboards do not justify this cost.
- After regional failover: rebuild from WARM tier (S3 Iceberg). Manual SRE operation, documented in runbook. Dashboards show stale data during rebuild — documented and accepted.

**Warm Tier — AWS S3 + Apache Iceberg:**
- Batch ETL from ClickHouse every 6 hours
- Parquet files, Z-order indexed by `tenant_id`
- Retention: 2 years
- Multi-region by default (S3 cross-region replication)
- EU data: stored in S3 eu-west-1, AWS DPA and SCCs in place
- PII-free: PII Anonymizer guarantees only tokenized IDs reach this tier

**Cold Tier — AWS S3 Glacier Deep Archive:**
- Annual archival from Warm tier
- Retention: 7 years (regulatory compliance)
- All data tokenized via PII Token Registry
- **GDPR erasure = token revocation only.** Glacier objects are never retrieved or modified for erasure. Revoking the AES-256 key in GCP Secret Manager makes all corresponding Glacier data permanently unreadable in-place.
- Legal basis: Art.17(3)(b) — aggregate data retained, PII unreadable
- No retrieval fee, no 12-hour Glacier restore window required for erasure

---

## 9. Observability & Disaster Recovery

### Telemetry Stack

| Tool | Role | Retention |
|---|---|---|
| **Datadog APM** | Metrics, APM traces, Kafka lag, DLQ depth, provider fallback cost, p99 latency, worker error rates | Per Datadog plan |
| **Grafana Cloud + Tempo** | Dashboards, tail-based distributed tracing (100% of errors, 1% of healthy paths), trace-ID correlated | Per Grafana plan |
| **Elastic Cloud** | Structured JSON logs, correlated by trace-ID, all layers | 30 days hot |
| **GCP Cloud Audit Logs** | Data access logs: CRM, Bigtable, Spanner — forwarded to Elastic for correlation | 400 days |
| **AWS CloudTrail** | All AWS API calls (SOC2 / ISO27001 evidence) | 7 years |

### SLA Monitor

Aggregates from Datadog + Grafana + Elastic. Fires via PagerDuty with a runbook link in every alert.

| Signal | Alert Threshold |
|---|---|
| Gold Lane consumer lag | > 50,000 messages → page SRE |
| Silver Lane consumer lag | > 5,000,000 messages → page SRE |
| DLQ depth | > 1,000 messages |
| CB open rate | Per-cell threshold |
| Worker error rate / p99 latency | Per-channel threshold |
| Provider fallback spend | > 2× primary |
| ML signal drop rate | > 5% in 10 minutes |
| Flink checkpoint time | > 120 seconds |
| Receipt sweep misses | Unresolved after 15 minutes |
| GDPR erasure SLA progress | Behind schedule |
| **DEDUP_SHADOW lag** | **> 30 seconds → P1 (degrades RTO before failover is even needed)** |

SLOs: 2FA p99 delivered < 10s end-to-end · Campaign first batch p99 < 2 min · DLQ resolution < 4h · Receipt reconcile < 5 min.

### Disaster Recovery

**Route53 Failover:**
- Health-check based, auto-activates in < 60 seconds
- Activates us-west-2 standbys: AUTH2, DIST_QUOTA2, SUPPDB2
- RPO: 1 hour
- RTO: **15 minutes** if DEDUP_SHADOW lag < 30s at failover · **30 minutes** if lagged

### The Cold Bloom Problem — Explained

On a regional failover, the dedup tiers (L1 LRU, L2 Counting Bloom, L3 Redis) start empty in the DR region. MirrorMaker 2's offset translation creates a ~15-minute window where events already processed will be re-consumed from MSK.

With empty dedup tiers:
- Every re-consumed event fires as a duplicate send
- At platform scale: ~104 million duplicate sends per failover event
- Includes duplicate 2FA OTPs → **security incident** (used OTP re-sent)

### DEDUP_SHADOW — The Solution

A dedicated EKS pod runs **continuously** in the us-west-2 DR region:

- Consumes from MSK Silver (the MM2 feed of Confluent Silver Lane)
- Writes only to the DR copy of DEDUP_L2 Counting Bloom
- **Never triggers any sends** — shadow mode only
- Lag target: < 30 seconds
- Cost: ~$400/month (ElastiCache r7g.2xlarge × 2 AZ in DR region + one pod)

**Hard gate on Channel Worker startup:**
Channel Workers in the DR region will not start until DEDUP_SHADOW confirms its lag is < 30 seconds. This is enforced as a Kubernetes health-check dependency — it cannot be skipped under pressure the way a runbook note can be skipped.

### GitOps — Argo CD

All infrastructure is defined as code. Argo CD handles:
- Drift detection (any manual change triggers an alert)
- Full audit trail of every deployment
- Manages Argo Rollouts (canary controller)
- Managed by Terraform Cloud

### DR Drills

- **Monthly:** Gremlin chaos injection (individual component failures)
- **Quarterly:** Full regional failover
- **Bi-annually:** Full game day (multi-failure scenario)
- Results feed back into runbook updates

---

## 10. Governance

**Azure Entra ID:**
Employee SSO via OIDC + PKCE federation. No SAML. Privileged Identity Management for infrastructure access. OIDC federated into the AUTH service for employee-facing operations.

**Azure Purview:**
Data lineage tracking across CRM, ClickHouse, and S3 Iceberg (Warm). PII catalog and compliance reports. AWS and GCP connectors registered. Used for audit evidence.

**AWS Cost Explorer:**
Per-tenant cost tracking. Fallback provider spend alert at 2× primary. Chargeback reporting per tenant. Actual spend data fed into Provider Budget Manager.

**AWS CloudTrail:**
All AWS API calls logged. 7-year archive for SOC2 / ISO27001 evidence.

**GCP Cloud Audit Logs:**
Data access logs for CRM, Bigtable, and Spanner. 400-day retention. Forwarded to Elastic for correlation with application logs.

**Terraform Cloud:**
All infrastructure as code. Multi-cloud state management (AWS + GCP + Cloudflare in one state). Sentinel policy-as-code for compliance guardrails. Manages Argo CD, which manages Kubernetes workloads.

---

## 11. Key Infrastructure Decisions

| # | Decision | What Was Rejected | What Was Chosen | Why |
|---|---|---|---|---|
| 1 | DDoS/WAF provider | Cloudflare + AWS Shield (both) | Cloudflare only | Shield is redundant with CF Enterprise. Dual-provider adds latency on every request for zero security benefit. |
| 2 | Campaign API location | EKS (AWS) | GKE (GCP, same-cloud as CRM) | Cross-cloud synchronous write on hot path adds latency and creates interconnect dependency. Same-cloud: p99 < 20ms. |
| 3 | TLS at NLB | Re-terminate TLS at NLB | TCP passthrough | CF already terminated TLS. Second termination = two handshakes per connection with no benefit. |
| 4 | Dedup storage | Single Redis cluster (would need 1.4 TB) | L1 LRU + L2 Counting Bloom + L3 slim Redis | 1.4 TB doesn't fit in one ElastiCache instance. Eviction = silent duplicate sends. |
| 5 | CB isolation | Shared per-provider CB | Per-tenant per-provider (10,000 instances) | Shared CB = one bad tenant payload blackouts 5,000 tenants. Per-tenant: blast radius = 1. |
| 6 | Push CB thresholds | Same as email (50%, 10s window) | 80% threshold, 30s window | APNs/FCM have legitimately bursty 429 traffic. 50% threshold = constant false trips. |
| 7 | DR dedup warm-up | Replay 15 min of Kafka at failover time | Continuous DEDUP_SHADOW in DR region | Replay at failover time adds ~12 min to RTO. Shadow-warm keeps L2 Bloom continuously current for ~$400/month. |
| 8 | Gold Lane overflow | S3 overflow as normal ops | No overflow — overflow = P0 capacity incident | Gold carries 2FA. If Gold fills, that is a planning failure, not a buffer scenario. |
| 9 | Cold-tier GDPR erasure | Retrieve from Glacier + delete | Token revocation in Token Registry | No Glacier retrieval fee, no 12-hour wait. Legally sufficient under Art.17(3)(b). |
| 10 | ClickHouse erasure | `DELETE WHERE user_id = X` | `ALTER TABLE DROP PARTITION` by PII token | Naive DELETE = async MergeTree mutation = metadata lock blocking compaction cluster-wide. |
| 11 | ClickHouse replication | Cross-region synchronous | Regional only, rebuild from S3 on DR | 2M rows/sec cross-region sync needs a dedicated network link and adds write latency. Analytics doesn't justify it. |
| 12 | 2FA EKS node pool | Shared pool with bulk workers | Dedicated on-demand Gold-Pool | Spot interruption during a 2FA saga sequence is a user-facing failure. 2FA workers run on on-demand only. |

---

## 12. DR Runbook

### Full Regional Failover: us-east-1 → us-west-2

```
T+0:00  Route53 health check detects primary region failure
T+0:01  Route53 activates standby services:
          - AUTH2 (Keycloak read replica promoted to primary)
          - DIST_QUOTA2 (rate limiter, conservative defaults)
          - SUPPDB2 (Postgres streaming replica active; Redis rebuilds from Postgres)

T+0:02  MSK (us-west-2) receives MM2-replicated Silver Lane events

T+0:03  CHECK: DEDUP_SHADOW lag?
          < 30s → proceed immediately to T+0:04
          > 30s → wait for DEDUP_SHADOW to catch up before proceeding

T+0:04  DEDUP_SHADOW health gate passes → Channel Workers unblocked
          WEMAIL, WSMS, WPUSH, WSOCIAL start in DR region

T+0:06  Monitor:
          - Silver Lane lag recovery rate
          - DEDUP L2 audit log (expect < 0.1% FPR duplicates)
          - Receipt Reconciler writing status to Cloud Spanner

T+0:15  End-to-end test: send test message on all channels using a test tenant
T+0:15  RTO target met (if DEDUP_SHADOW was current at T+0:03)

T+1:00  SRE begins ClickHouse rebuild from S3 Iceberg warm tier
         Dashboards show stale data until rebuild completes — expected and documented
```

### Confluent → MSK Failover Only (No Region Failure)

```
T+0:00  Confluent health alert fires → SRE paged
T+0:01  Verify DEDUP_SHADOW lag < 30 seconds
T+0:02  Redirect Silver Lane consumers to MSK
T+0:03  Gold Lane: Confluent RF=3 is sufficient — no MSK failover needed for Gold
T+0:05  Monitor for duplicate rate spike (expect ~0.1% FPR — absorbed by L2 Bloom)
T+0:15  If DLQ backlog needs replay: SRE clears root-cause gate, starts replay
```

### Per-Component Recovery Paths

| Component | Failure | Recovery |
|---|---|---|
| AlloyDB primary | Node failure | Automatic replica promotion. PITR if data corruption. |
| ElastiCache shard | Node failure | Cluster-mode Redis promotes replica automatically. |
| DEDUP_L2 Bloom | Cluster failure | Fail-open (sends proceed). Alert fires. Rebuild from Kafka replay if needed. |
| REDIS_STATE | Cluster failure | Fail-open. Rebuild from Cloud Spanner on recovery. |
| ClickHouse cluster | Full failure | Dashboards stale. Manual SRE rebuild from S3 Iceberg warm tier. |
| Flink job crash | OOM / checkpoint failure | Auto-restart from last RocksDB checkpoint (60-second intervals). |
| Schema Registry | Outage | Producers use cached schemas (1-hour grace). New schema deploys blocked until recovered. |
| Bigtable | Regional failure | europe-west4 replica promoted. |
| DEDUP_SHADOW | Pod failure | Restart. L2 Bloom stays warm — lag clock starts from restart. Alert fires if lag > 30s. |

---

## 13. Latency Budget

End-to-end p99 latency from API call to provider handoff for a 2FA send. **Target: < 10 seconds.**

| Stage | Component | Latency (p99) |
|---|---|---|
| Cloudflare PoP | TLS termination, WAF, DDoS | ~2ms |
| AWS NLB | TCP passthrough | < 1ms |
| AWS API Gateway | Payload validation | ~5ms |
| AUTH chain | JWT validate + revoke check + CTX stamp + Cell Router | ~8ms |
| Fast Ingestor | Priority path, bypass rate limiter | ~1ms |
| Fast-Path Suppression | Bloom filter check | < 1ms |
| Kafka produce | Gold Lane publish | ~5ms |
| SAGA consumer | Temporal pick-up | ~10ms |
| Dedup — L1 | In-process LRU hit | < 1ms |
| Circuit Breaker + Token Bucket | Sidecar processing | ~2ms |
| mTLS east-west | Per hop overhead (steady-state) | ~0.5ms |
| Provider call | FCM / APNs / Twilio API | ~200–500ms |
| **Total (excl. provider)** | | **~35ms** |
| **Total (incl. provider)** | | **~235–535ms** |

The 10-second p99 SLO accounts for provider response variability, one retry on first failure, and burst queueing.
