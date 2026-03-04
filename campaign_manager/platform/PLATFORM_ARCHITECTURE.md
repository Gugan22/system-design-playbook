# 🌍 Global Multi-Region SaaS Platform — Consolidated Architecture Document (Single File)

> **Purpose:** Single-page, copy-ready, developer + operator-friendly architecture doc covering decisions, rationale, pros/cons, components, configuration highlights, alternatives, and failure scenario walkthroughs — with mermaid diagrams included.

---

## Executive Summary (one-line)
A multi-tenant, compliance-first messaging & analytics platform built with deterministic tenant routing, priority-aware event lanes (Gold/Silver), hybrid SoR + append-store persistence, strong isolation of failure domains, and observability-first operational controls to guarantee transactional delivery for critical flows and scalable bulk processing for campaigns.

---

## Top-level Design Decisions (high level)
- **Gold/Silver Kafka lanes** to separate latency-critical vs bulk traffic.  
- **Hybrid SoR + Append Store (Citus/Postgres + Scylla/Dynamo)** for durability + high write throughput.  
- **Service mesh + mTLS** for east-west trust.  
- **Degradation contract** with tiered shedding (Tier1 never shed).  
- **Schema registry & online migration** to prevent breaking changes.  
- **S3 spill & Hydration** to prevent Kafka collapse under disk/throughput pressure.  
- **Three isolated Redis clusters** for idempotency, runtime state, and rate limiting (failure isolation).  
- **Active-passive multi-region DR** using MirrorMaker2 for Kafka and cross-region replication for DBs.

Each decision below is repeated as: *What was chosen → Why → Pros/Cons → Why not alternatives → Components present → Why chosen → Key settings/configuration.*

---

# Layered Architecture (short map)

```mermaid
flowchart LR
  L0[Layer 0 — Global Network & Security] --> L1[Layer 1 — Ingress & Identity]
  L1 --> L2[Layer 2 — Metadata & System of Record]
  L2 --> L3[Layer 3 — Event Bus (Gold / Silver)]
  L3 --> L4[Layer 4 — Execution Engine (Workers / Sagas)]
  L4 --> L5[Layer 5 — Analytics & Memory]
  L5 --> L6[Layer 6 — Observability & DR]
  L6 -.-> L1 & L3 & L4
```

---

# L0 — Global Network & Security (Owner: Infra/NetSec)

### Decisions
- Use CDN/Anycast (CloudFront / Fastly) + TLS 1.3 + ACM for certs.  
- L3/L4 absorption at edge (AWS Shield or equivalent) + WAF for L7.  
- Central API gateway (Kong / AWS APIGW) for versioning, payload validation, and edge policies.  
- Secrets manager (Vault / AWS Secrets Manager) with sidecar injection.  
- Compliance gateway for PII/opt-out enforcement.  
- Service mesh for east-west mTLS enforcement.

### Why
Protect edge, reduce blast radius, centralize policy and compliance enforcement, and ensure encrypted internal traffic with identity enforcement.

### Pros
- Reduces attack surface and L3/L4 load on backend.  
- Centralized compliance checks and cert rotation.  
- mTLS enforces identity across services.

### Cons
- Cost and complexity at the perimeter.  
- Increased operational surface (WAF tuning, mesh policies).

### Why not alternatives
- Relying only on WAF is insufficient for volumetric attacks.  
- No mesh → weaker internal zero-trust.

### Components & Reasons
- **CDN/Anycast** (CloudFront/Fastly): global PoPs, TLS offload, cache.  
- **TLS Termination & ACM**: automated cert rotation, TLS 1.3.  
- **AWS Shield / DDoS protection**: L3/L4 absorption.  
- **WAF (OWASP)**: payload filtering and rate rules.  
- **API Gateway**: route versioning, auth forwarding, payload validation.  
- **Secrets Manager**: credential rotation, sidecar injection.  
- **Compliance Gateway**: GDPR, DMARC/SPF, opt-out handling.  
- **Service Mesh (Istio/Linkerd)**: mTLS, traffic policy.

### Key Settings / Configs (highlights)
- TLS: enforce TLS 1.3; HSTS.  
- WAF: OWASP CRS, IP rate rules, geo-block lists.  
- API GW: route versioning (`/v1`, `/v2`), deprecation sunset headers.  
- Secrets: auto-rotate every 30 days; sidecar injection for all services.  
- mTLS: per-service certs, rotated via Secrets Manager.
- Degrade policy: apply priority-tier info to API gateway headers for downstream enforcement.

---

# L1 — Ingress & Identity (Owner: Platform/Infra)

### Decisions
- OIDC / RBAC for auth; JWT validation at edge.  
- JWT revocation blocklist (Redis-backed).  
- Tenant Context Injector to stamp tenant-id.  
- Cell Router (tenant → cell) to route by tenant cell.  
- Distributed Rate Limiter (Redis token buckets) per-tenant.  
- Fast-Priority Ingestor bypass for 2FA/transactional hot paths.  
- Suppression DB: Redis Bloom for fast checks + Postgres as the durable SoT.

### Why
Secure multi-tenancy, immediate revoke, tenant isolation, fair quotas, and deterministic routing for performance and locality.

### Pros
- Token revocation works immediately.  
- Tenant fairness and protection from noisy neighbors.  
- Fast path ensures 2FA remains low-latency during degradation.

### Cons
- More moving parts: revocation store, token buckets, cell router logic.  
- Slight additional latency at ingress for auth + context injection.

### Why not alternatives
- Stateless JWT-only approach lacks immediate revocation.  
- Global single limiter can't enforce tenant fairness.

### Components & Reasons
- **OIDC Auth Service**: centralized auth flow.  
- **JWT Revocation (Redis)**: immediate revoke on compromise/admin offboard. TTL aligned with token TTL.  
- **Tenant Injector**: ensures tenant isolation signals follow requests.  
- **Cell Router**: reduces blast radius, optimizes data locality.  
- **Distributed Quota (Redis token bucket)**: per-tenant, per-provider quota enforcement.  
- **Fast Ingestor**: reserved resources for Tier1 flows.

- **Suppression DB**: Bloom filter in Redis for sub-ms suppression checks; Postgres as source-of-truth; write: Redis-first then Postgres (sync write). Rebuild from Postgres on Redis cold start. Max stale tolerance: 30s.

### Key Settings / Configs
- JWT revoke store TTL = token TTL (align).  
- Dist-quota: token bucket thresholds; automated SRE backpressure when Kafka lag > 5M.  
- Fast path: dedicated thread pool and memory reservation.  
- Suppression bloom FP rate tuned conservative; Redis-first write with Postgres durability.  
- Fail-open suppression behavior when DB down (audit logged).

---

# L2 — Metadata & System of Record (Owner: Data-Platform)

### Decisions
- Use Citus/Postgres (sharded by tenant_id) as the CRM SoR.  
- Use append-only event store (Scylla / Dynamo) for high-frequency event writes.  
- CDC-based Outbox (Debezium) to stream SoR changes to event lanes.  
- Enforce a Schema Registry (Confluent; Avro/Protobuf).  
- PgBouncer + Write Admission Controller rejecting writes when queue > 80%.  
- PII masking and Erasure Processor for GDPR compliance (rate-limited deletes).

### Why
Relational operations (transactions, relations) need ACID guarantees; append store provides massive write throughput and offloads CRM writes. CDC provides decoupling and eventual consistency to downstream systems.

### Pros
- Strong data consistency for CRM.  
- Scalable writes for high-frequency events.  
- Controlled schema evolution and compatibility checks.

### Cons
- Operational overhead: maintaining sharded Postgres + append store + CDC.  
- Schema migration needs careful coordination.

### Why not alternatives
- NoSQL-only SoR sacrifices relational guarantees.  
- Putting everything into Kafka as the SoR complicates transactional semantics for features like 2FA.

### Components & Reasons
- **Citus/Postgres**: sharded authoritative CRM, PITR backups hourly, read replicas per region.  
- **Append Store**: handles 500k+ TPS high-frequency contact events; reduced pressure on CRM.  
- **Debezium Outbox**: transaction-bound outbox pattern for reliable CDC.  
- **Schema Registry**: enforce Avro / Protobuf, RF=3.  
- **PgBouncer Write Admission**: prevents Postgres connection exhaustion; 503 + Retry-After if queue >80%.  
- **PII Masking / Erasure**: tokenization at write time, field-level encryption, access audit logs.  
- **Online Schema Migration Tools**: pt-online-schema-change or pgroll for zero-downtime migrations.

### Key Settings / Configs
- CRM Shards: 32 shards by tenant_id.  
- PgBouncer: max client conns, write-admit threshold = 80% queue.  
- PITR: snapshot hourly; WAL retention per policy.  
- Erasure: rate-limited deletes (max 10k deletes/min), SLA Hot/Warm/Cool: 30d/90d.  
- Append store write ceiling: 500k+ TPS.

---

# L3 — The Highway (Event Bus) (Owner: Messaging-Infra)

### Decisions
- Two Kafka clusters/lanes: **Gold** (low-latency, time-critical) and **Silver** (high-throughput bulk).  
- Enforce Kafka ACLs per topic & header-level tenant-id.  
- Spill to S3/Iceberg when brokers exceed disk or partition saturation.  
- DLQ with replay service; Hydration worker to re-ingest from spill.  
- Per-lane partitioning and ISR policies tuned for performance/resilience.

### Why
QoS separation prevents bulk workloads from impacting time-critical flows (2FA, transactional). Spill to object storage prevents broker disk saturation and possible data loss.

### Pros
- Clear QoS and resource isolation.  
- Predictable behavior under overload.  
- Re-entry path for spilled messages to prevent permanent loss.

### Cons
- More clusters and operational complexity.  
- Need for robust replay and idempotency handling.

### Why not alternatives
- Single Kafka cluster with priority flags risks noisy neighbor effects and priority inversion.  
- Infinite retention is too expensive and unneeded for many use-cases.

### Components & Reasons
- **Gold Lane (Kafka Cluster A)**: RF=3, partitions=120, retention=72h, ISR=2 — for low latency, critical messages.  
- **Silver Lane (Kafka Cluster B)**: RF=3, partitions=600, retention=7d — for bulk campaigns.  
- **Kafka ACL**: per-topic, per-service-account.  
- **Gold S3 Spill / Silver S3 / Iceberg**: overflow storage with partitioned tenancy.  
- **Hydration Worker**: controlled re-entry to clusters (throttled per tenant).  
- **DLQ & Replay Service**: TTL=7d, auto-replay after 30min, per-tenant replay caps (200 msg/s) and global cap (5k msg/s).

### Key Settings / Configs
- Gold partitions = 120; retention = 72h.  
- Silver partitions = 600; retention = 7d.  
- DLQ retries = 3, TTL 7d, page SRE if depth >1k.  
- Spill SLA: re-entry under 15 minutes to avoid backlog cascade.  
- Tenant message envelope must include tenant-id header validated by consumers.

---

# L4 — The Muscle (Execution Engine) (Owner: Delivery-Eng)

### Decisions
- Saga orchestrator for multi-step flows (state in Redis-State).  
- Bulk stateless workers with HPA (10 → 500 pods).  
- Local LRU cache per worker to reduce Redis pressure.  
- Three isolated Redis clusters: Idempotency, Delivery State (Saga), Rate buckets.  
- Provider protection: Smart Circuit Breaker (CB) + Token Bucket (TB) per provider + Provider Budget Manager.  
- Receipt Reconciler + Receipt Sweep to reconcile delivery receipts and feed status/suppression stores.  
- Canary Controller and Load Test Gate for deployment safety.

### Why
To guarantee correct multi-step execution (compensations), bound retries, ensure idempotency, and isolate failures across user-visible delivery paths.

### Pros
- Strong failure containment.  
- Ability to compensate and replay workflows.  
- Delivery cost governance via provider budgets.  

### Cons
- Multiple Redis clusters increase operational surface.  
- Saga orchestration requires careful TTL and state rebuild plans.

### Why not alternatives
- Single Redis or DB-only state would create large blast radius or high latency.  
- Pure choreography (no orchestrator) makes complex transactions harder to reason about in multi-step flows.

### Components & Reasons
- **Saga Orchestrator**: state per message in Redis-State, TTL 24h, compensating transactions support.  
- **Choreo Workers**: bulk stateless consumers, partition-to-pod ratio limited to avoid hotspots.  
- **Redis-Idem**: dedup window (24h TTL) — fail-open with audit on shard loss (bounded duplicate risk).  
- **Redis-State**: saga step tracking — rebuild from STATUS_DB on region failover.  
- **Redis-Rate**: per-provider token buckets (cross-AZ replicas).  
- **Provider Budget Manager**: fallback quotas and alerts if fallback >2x.  
- **Circuit Breaker**: half-open probe every 30s; state closed/open/half-open.  
- **Token Bucket**: per-provider rate limiting with exponential backoff; perm-fail → DLQ.  
- **Receipt Reconciler & Sweep**: reconcile in <5min SLA; sweep every 10min for missing receipts.

### Key Settings / Configs
- Saga TTL = 24h max.  
- Redis clusters = 3-shard, 3-replica cross-AZ.  
- CB probe interval = 30s.  
- TB per-provider quotas; fallback default = 60% of primary limit.  
- Receipt reconcile SLA = 5 minutes; sweep interval = 10 minutes.  
- LCACHE max entries per worker = 10k.

---

# L5 — The Memory (Analytics & Intelligence) (Owner: Analytics-Eng)

### Decisions
- Apache Flink for stream processing with exactly-once semantics.  
- PII Anonymizer before analytics writes.  
- ClickHouse for Hot tier; S3 Iceberg Warm; Glacier Cold for archival.  
- ML Feedback Gateway and ML buffer (Kafka topic TTL 1h).

### Why
Real-time analytics for live dashboards and ML with durable warm/cold storage for retrospective analysis and compliance.

### Pros
- Exactly-once processing for accurate counters.  
- Low-latency analytics; multi-tier storage controls cost.  

### Cons
- Flink and ClickHouse operational complexity.  
- Need for capacity planning for stateful stream jobs.

### Why not alternatives
- Serverless stream functions or cloud-only analytic warehouses may lack low-latency guarantees or cost-efficiency at scale.

### Components & Reasons
- **Flink**: checkpoint every 60s, RocksDB state backend, watermark 30s late arrival tolerance.  
- **PII Anonymizer**: strips PII pre-write; ensures tokenized contact IDs.  
- **ClickHouse (Hot)**: MergeTree; TTL 30 days; 8-shard RF=3; high ingest performance.  
- **S3 Iceberg (Warm)**: Batch ETL every 6h, parquet + z-order by tenant; retention 2 years.  
- **Glacier (Cold)**: annual archival; compliance retention 7 years.  
- **ML Buffer (Kafka)**: TTL 1h to absorb ML service outages; alerts if drop >5% in 10m.

### Key Settings / Configs
- Flink checkpoint = 60s; checkpoint timeout alert at 120s.  
- Flink watermark allowed lateness = 30s.  
- ClickHouse hot TTL = 30 days.  
- Warm retention = 2 years; Cold retention = 7 years.  
- ML buffer TTL = 1 hour.

---

# L6 — Observability & Disaster Recovery (Owner: SRE-Team)

### Decisions
- Jaeger/Tempo for distributed tracing (tail-based sampling: 100% errors, 1% healthy).  
- Prometheus + Grafana for metrics + alerts.  
- Elastic/Loki for centralized logging (structured JSON).  
- Active-passive multi-region DR with MirrorMaker2 for Kafka.  
- DR drills (quarterly full failover; monthly chaos).

### Why
Fast detection + automated backpressure, predictable failover semantics, runbook-driven incident response.

### Pros
- Lower MTTR.  
- Automated backpressure reduces operator toil.  
- Predictable RPO/RTO.

### Cons
- Backup region capacity under-used compared to active-active.  
- Complexity in ensuring consistent replay and rebuild logic.

### Why not alternatives
- Active-active rejected because global transactional consistency is hard and high-risk for SoR and Redis-like state.

### Components & Reasons
- **Tracing (Jaeger/Tempo)**: trace-id on every request; tail-based sampling.  
- **Metrics (Prometheus/Grafana)**: Kafka lag, DLQ depth, p99 latency.  
- **Logs (Elastic/Loki)**: correlated by trace-id.  
- **SLA Monitor & Alerts (PagerDuty)**: runbook links on alerts.  
- **DR (Route53 + Cross-region replication)**: automatic failover.  
- **DR Drill Program**: validate runbooks and recovery procedures.

### Key Settings / Configs
- RPO = 1 hour; RTO = 15 minutes.  
- DR drills: full quarterly; chaos monthly.  
- Alert thresholds: Kafka lag, DLQ depth >1000, fallback cost >2x, Flink checkpoint >120s.

---

# Cross-cutting Policies & Configurations

- **Degradation Contract**: Tier1 (2FA/transactional) never shed; Tier2 campaign sends shed under load; Tier3 analytics shed; Tier4 ML feedback shed first. Enforced by DIST_QUOTA hooked into ingress chain.  
- **Schema-first**: All producers must register schemas; breaking changes blocked unless rollbacks are planned.  
- **Idempotency**: dedup window in Redis-Idem (24h).  
- **Fail-open vs Fail-closed**: Suppression checks fail-open (audited); auth fails closed; schema enforcement fails closed; dedup can fail-open with audit to preserve availability.  
- **Canary policy**: tenant-aware canaries: 5% → 25% → 100% with automatic rollback on error spike.  
- **Load gate**: k6/Gatling shadow traffic; blocking promotion unless pass at 2x target TPS.

---

# Key Configuration Table (compact)

| Item | Value / Highlight |
|------|-------------------|
| Gold partitions | 120 |
| Silver partitions | 600 |
| Gold retention | 72 hours |
| Silver retention | 7 days |
| Kafka RF | 3 |
| DLQ TTL | 7 days |
| DLQ retries | 3 |
| Saga TTL | 24 hours |
| Redis replicas | 3 per shard (cross-AZ) |
| PgBouncer write threshold | 80% queue |
| Flink checkpoint | 60 seconds |
| Flink checkpoint timeout alert | 120 seconds |
| ClickHouse hot TTL | 30 days |
| Erasure SLA (Hot/Warm/Cold) | 30d / 90d / 7 years |
| RPO / RTO | 1 hour / 15 minutes |
| ML buffer TTL | 1 hour |
| Circuit breaker probe | 30 seconds |
| Provider fallback quota | 60% of primary default |
| Canaries | 5% → 25% → 100% tenant-aware |

---

# Failure Scenarios — Walkthroughs (detailed & stepwise)

### Scenario A — Gold Kafka Broker Disk Pressure / Saturation
**Symptoms:** Broker disk usage > 70%; ISR decreased; producer latency spikes.  
**Automatic Actions:** Broker triggers spill to Gold S3 Overflow; producers receive backpressure signals.  
**SRE Actions:** Hydration worker will rehydrate at capped rate (500 msg/s per tenant). Monitor DLQ & lag.  
**Recovery:** After spill, re-entry to Gold lane via controlled rehydrate. If spill too large, promote critical messages priority and shed Tier 3/4 per Degradation Contract.  
**Why this helps:** Prevents catastrophic broker OOM and data loss while allowing prioritized re-entry.

---

### Scenario B — Redis Shard Loss (Redis-Idem)
**Symptoms:** One Redis shard crashes; some dedup keys unreachable.  
**Automatic Actions:** Dedup checks fail-open for affected keys with audit log; workers continue delivery (duplicate risk bounded by shard keyspace). Trigger SRE page.  
**SRE Actions:** Recover replica, rebuild shard, reconcile dedup gap via STATUS_DB if needed.  
**Recovery:** Once cluster restored, audit logs used to de-duplicate if duplicates caused issues.  
**Tradeoffs:** Availability (fail-open) vs strict dedup protection; chosen to preserve transactional flows.

---

### Scenario C — Primary Postgres Saturation (PgBouncer queue > 80%)
**Symptoms:** PgBouncer high connection queue, increased write latency & timeouts.  
**Automatic Actions:** Write Admission Controller rejects additional writes with `503 + Retry-After`. High-priority fast path (2FA) unaffected as it bypasses standard limiter. Alert SRE.  
**SRE Actions:** Scale DB or increase buffer; investigate high write source.  
**Recovery:** Throttled clients retry idempotently; post stabilization writes resume.  
**Why:** Prevents retry storms and full DB collapse.

---

### Scenario D — Provider (Email/SMS) Outage
**Symptoms:** Provider webhooks failing; increased provider error rate.  
**Automatic Actions:** Circuit breaker opens for provider; Delivery flows switch to fallback provider subject to Provider Budget Manager quotas. Ticket/alert if fallback spend >2x primary.  
**SRE Actions:** Investigate provider outage; adjust budgets if acceptable.  
**Recovery:** Once primary healthy, CB half-open probes resume; traffic returns gradually.  
**Why:** Protects deliverability and cost while enabling fallback.

---

### Scenario E — Massive Ingestion Spike & Kafka Lag Growth
**Symptoms:** Gold or Silver lag grows beyond threshold (e.g., Gold > 50k).  
**Automatic Actions:** Automated backpressure to DIST_QUOTA at ingress; Degradation Contract shedding: Tier3/4 shed first; alert SRE.  
**SRE Actions:** Scale consumers, add partitions if feasible, or trigger emergency reconfig.  
**Recovery:** After consumers catch up, gradually reopen shed tiers.  
**Why:** Prevents cascading failures across subsystems.

---

### Scenario F — GDPR Erasure Storm
**Symptoms:** Large-scale erasure request from regulatory process.  
**Automatic Actions:** Erasure Processor rate-limits deletes (max 10k/min); cascades to Hot/Warm/Cold per SLA to prevent cluster shock.  
**SRE Actions:** Monitor pipelines for backlog, prioritize critical services.  
**Recovery:** Rate-limited cascade completes per SLA; audit logs retained for compliance.  
**Why:** Avoids runaway deletion causing heavy I/O and cluster destabilization.

---

# Operational Runbooks (short pointers)

- **DLQ overflow**: Pause producer; identify poison message via schema registry; replay with idempotency key.  
- **Flink checkpoint failure**: Alert & rollback to previous savepoint; increase resources or tune RocksDB backend.  
- **Redis cold-start rebuild**: Rebuild cache from Postgres source-of-truth; ensure suppression bloom rehydration before re-opening paths.  
- **DR drill**: Execute failover in staging-alpha; validate data consistency in read-only mode then enable writes per runbook.

---

# How to Read This Doc (for Freshers → Seniors)

- Freshers: Read Executive Summary, Layer map, L0→L6 short decisions, and Failure Scenarios to understand the "why" and high-level flow.  
- Engineers: Read per-layer component lists, key settings, and runbooks for actionable configs.  
- SREs/Architects: Review Key Configs, Degradation Contract, DR RPO/RTO, and Failure Walkthroughs for operations and capacity planning.

---

# End-to-End Simplified View

```mermaid
flowchart TB
  CDN[CDN / Anycast] --> APIGW[API Gateway]
  APIGW --> AUTH[OIDC / RBAC]
  AUTH --> CTX[Tenant Context Injector]
  CTX --> CELL[Cell Router]
  CELL --> GOLD[Gold Kafka (critical)]
  CELL --> SILVER[Silver Kafka (bulk)]
  GOLD --> SAGA[Saga Orchestrator]
  SILVER --> CHOREO[Bulk Workers]
  SAGA & CHOREO --> FLINK[Stream Processor]
  FLINK --> HOT[ClickHouse Hot]
  FLINK --> WARM[S3 Iceberg Warm]
  HOT --> COLD[Glacier Cold]
  OBS[Observability Stack] -.-> APIGW & GOLD & SAGA & FLINK
```

---


