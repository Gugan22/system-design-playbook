# Platform Architecture – Multi-Tenant Campaign Delivery System

## 📌 Overview

This document explains the **platform architecture** for the multi-tenant campaign management system. It provides a high-level view of components, domain separation, and rationale behind architectural choices.

---

## 🏗 Architecture Domains

The platform is structured into the following domains:

1. **Edge Layer**
   - Global DNS, Web Application Firewall, Load Balancer, API Gateway
   - Responsibilities: Tenant identification, JWT validation, per-tenant rate limiting

2. **Identity & Access Domain**
   - Auth, User, Organization, RBAC services
   - Tenant-aware authentication and access control

3. **Control Plane**
   - Tenant metadata DB, routing table, policy DB, credential vault
   - Centralized management of tenants, quotas, feature flags

4. **CRM Integration Domain**
   - CRM connector, import processor, deduplication engine, contacts database
   - Handles contact ingestion and mapping for multi-tenant use

5. **Campaign Authoring Domain**
   - Campaign Service, Template Builder, Segmentation Engine, Outbox
   - Manages campaign creation, audience selection, and publishes events

6. **Event Streaming Layer**
   - Kafka topics for campaign details, audience, status, analytics
   - Guarantees order per tenant, scalable, resilient, and partitioned

7. **Delivery Domain**
   - Delivery orchestrator, workers, composer, provider adapter, delivery store
   - Handles campaign execution, state machine tracking, retries, circuit breaking

8. **External Channel Providers**
   - Email, SMS, WhatsApp, social media, push notifications
   - Provider abstraction layer ensures pluggability

9. **Status Ingestion**
   - Webhook gateway, signature validator, normalizer
   - Captures delivery and engagement updates

10. **Tracking Layer**
    - Click redirect and open pixel tracking
    - Publishes engagement events for analytics

11. **Analytics & Reporting**
    - Stream processing, aggregation DB, reporting API, intelligence engine
    - Provides metrics and actionable insights per tenant

12. **Observability**
    - Metrics, logging, tracing, alerting
    - Ensures operational visibility and SLA adherence

---

## 🔒 Tenant Isolation Strategy

Isolation is enforced at multiple layers:

| Layer | Mechanism |
|-------|-----------|
| Authentication | Tenant ID in JWT |
| API Gateway | Rate limiting per tenant |
| Database | Schema-per-tenant or tenant-partitioned tables |
| Kafka | Partitioned by tenant |
| Delivery | Idempotency scoped per tenant |
| Analytics | Tenant-scoped queries |

---

## 🧠 Design Decisions

### Why Event-Driven Architecture?
- Decouples campaign creation from delivery
- Handles millions of messages asynchronously
- Enables near real-time analytics
- Improves fault tolerance

### Why Control Plane / Data Plane Separation?
- Control plane handles tenant management and configuration
- Data plane handles high-volume delivery events
- Improves scalability and fault isolation

### Why Outbox and Saga Patterns?
- Guarantees reliable event publishing from transactional DB operations
- Ensures exactly-once message delivery semantics
- Handles retries and failures without message loss

### Why Multi-Tenant Partitioning?
- Guarantees isolation across tenants
- Supports high concurrency without interference
- Enables per-tenant throttling and SLA enforcement

### Why Observability Layer?
- Critical for multi-tenant platforms
- Tracks per-tenant failures, SLA violations, and operational metrics
- Supports debugging and performance tuning

---

## ⚡ Summary

This architecture is **enterprise-grade**, designed for:

- High scalability
- Multi-channel campaign delivery
- Multi-tenant isolation
- Event-driven reliability
- Observability and fault-tolerance

It is intended to support **millions of users and contacts**, provide actionable analytics, and maintain operational safety for enterprise deployments.

---
