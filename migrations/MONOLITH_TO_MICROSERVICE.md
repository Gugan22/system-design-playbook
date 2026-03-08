# Monolith to Microservices — Migration Strategy

---

## TL;DR — For Stakeholders and Time-Pressed Engineers

Migrating from a monolith to microservices is one of the highest-risk architectural changes an engineering organisation can undertake. Done well, it enables independent team deployments, targeted scaling, and long-term velocity at scale. Done poorly, it produces a **distributed monolith** — all the operational complexity of microservices with none of the independence benefits — and takes 12–24 months of engineering time to unwind.

**The risk of migrating prematurely** is high: teams under 8 engineers, products under 12 months old, or organisations without a dedicated platform/DevOps capability routinely end up slower and more fragile after migration than before. **The risk of not migrating when genuinely ready** is also real: deployment bottlenecks, inability to scale individual components, and team coordination overhead compound with every new hire.

The decision must be driven by a named, specific engineering pain — not by industry trend. A successful first service extraction takes **8–14 weeks** end to end. A full platform migration typically spans **12–24 months** depending on monolith complexity and organisational readiness. This document is the complete field guide for doing it right.

---

## Table of Contents

1. [What Is a Monolith — and Why We Care](#1-what-is-a-monolith--and-why-we-care)
2. [When Should You Actually Migrate?](#2-when-should-you-actually-migrate)
3. [The Strangler Fig Pattern — Your Best Friend](#3-the-strangler-fig-pattern--your-best-friend)
4. [How to Pick the First Service to Extract](#4-how-to-pick-the-first-service-to-extract)
5. [Handling the Shared Database Problem](#5-handling-the-shared-database-problem)
6. [Feature Flags as a Migration Tool](#6-feature-flags-as-a-migration-tool)
7. [Service Communication & Data Consistency](#7-service-communication--data-consistency)
8. [Observability & Resilience](#8-observability--resilience)
9. [Security Between Services](#9-security-between-services)
10. [Service Discovery & Configuration Management](#10-service-discovery--configuration-management)
11. [Testing Strategy for Microservices](#11-testing-strategy-for-microservices)
12. [Deployment & Infrastructure](#12-deployment--infrastructure)
13. [The Distributed Monolith — The Anti-Pattern You Must Avoid](#13-the-distributed-monolith--the-anti-pattern-you-must-avoid)
14. [When NOT to Migrate](#14-when-not-to-migrate)
15. [Step-by-Step Migration Playbook](#15-step-by-step-migration-playbook)
16. [What I Have Seen Go Wrong](#16-what-i-have-seen-go-wrong)
17. [Quick Reference Cheat Sheet](#17-quick-reference-cheat-sheet)

---

## 1. What Is a Monolith — and Why We Care

If you have joined a team and found one giant codebase where the user interface, business logic, and database all live together and deploy together — that is a monolith.

> 💡 **Analogy:** Think of a monolith like a Swiss Army knife. Convenient at the start. But when 50 engineers all try to sharpen different blades at the same time, things get messy fast.

### What Monoliths Look Like in Practice

In early-stage products, a monolith is perfectly fine. The problems show up as the team and product grow:

- One small bug in the payments module can crash the entire app.
- Deploying a CSS fix requires testing everything — even unrelated features.
- The team working on search cannot deploy without coordinating with the team working on checkout.
- Scaling the whole app for a CPU-heavy feature wastes resources on everything else.

### What Microservices Look Like

Microservices split the monolith into small, independently deployable services. Each service owns its own data, its own logic, and its own deployment pipeline.

**Example: An e-commerce platform might split into:**

```
┌─────────────────────┐   ┌──────────────────────┐   ┌─────────────────────┐
│   User Service      │   │   Product Service    │   │   Order Service     │
│  login, profiles    │   │  catalogue, search   │   │  cart, checkout     │
└─────────────────────┘   └──────────────────────┘   └─────────────────────┘

┌─────────────────────┐   ┌──────────────────────┐
│ Notification Service│   │   Payment Service    │
│  email, SMS, push   │   │  billing, refunds    │
└─────────────────────┘   └──────────────────────┘
```

Each service is a separate codebase. A bug in Notifications cannot crash Payments.

Sounds great, right? It is — but only when the timing is right.

---

## 2. When Should You Actually Migrate?

The biggest mistake teams make is migrating too early. Microservices introduce real costs — operational complexity, network latency, distributed tracing, and service-to-service auth. Those costs are worth paying only when you hit specific pain points.

### Strong Signals That You Are Ready

| Signal | Why It Matters | Example |
|--------|---------------|---------|
| Deployment bottleneck | Multiple teams blocked waiting for one release train | Frontend team waits on backend team every Thursday |
| Scaling mismatch | One module needs 10x resources; rest need none | Search uses heavy CPU but billing module is idle |
| Team ownership confusion | More than 2 teams share the same codebase | 3 teams all editing the same auth module |
| Outage blast radius is huge | A minor bug kills the whole product | A broken report query crashes the entire API |
| Tech stack lock-in | One legacy stack blocks modern tooling | Need ML inference but entire app is PHP 5 |

### Weak Signals — Do Not Migrate Yet

- **"Our app is getting slow"** — Profile first. The bottleneck is likely one query, not the architecture.
- **"Everyone else is doing microservices"** — That is not a technical reason.
- **"The codebase is messy"** — Clean it up first. A messy monolith becomes messy microservices.
- **"We have under 10 engineers"** — The coordination overhead will slow you down, not speed you up.

> ⚠️ **Rule of Thumb:** If you cannot clearly name two independent teams who will own two independent services, you are probably not ready. Microservices are a team structure problem as much as a technical one.

---

## 3. The Strangler Fig Pattern — Your Best Friend

The Strangler Fig is the gold-standard approach to monolith migration. The name comes from a tropical vine that grows around a tree — eventually replacing it — while the tree stays standing throughout.

In software: you incrementally replace pieces of the monolith with new services, while the monolith continues running. Users never see downtime. The old code "dies" only after the new service is fully validated.

> 🌳 **Origin:** The term was coined by Martin Fowler in 2004. It remains one of the most cited migration patterns because it actually works in production — not just on whiteboards.

### How It Works — Step by Step

1. Stand up a routing layer (API Gateway or reverse proxy) in front of your monolith.
2. Build the new microservice alongside the monolith — do not rip anything out yet.
3. Shadow traffic: send real requests to both old and new, compare responses. Fix discrepancies.
4. Gradually shift traffic: 5% → 25% → 50% → 100% to the new service.
5. Once the new service handles 100% of traffic and is stable for two weeks, decommission the old code.

### Concrete Example

**Scenario: Extracting the Notification module from a monolith.**

```
Step 1 — Add an API Gateway (e.g. Kong, AWS API Gateway).
         All traffic still flows to the monolith. Nothing changes for users.

Step 2 — Build Notification Service separately.
         It has its own database and its own deploy pipeline.

Step 3 — Shadow mode: when the monolith sends a notification,
         also send the same event to the new service.
         Compare outputs. Log differences.

Step 4 — Route 5% of notification requests to the new service.
         Monitor error rates. Gradually increase to 100% over 2-4 weeks.

Step 5 — Delete the notification code from the monolith. Done.
```

### Why This Is Better Than a "Big Bang" Rewrite

| Approach | Risk | When It Works |
|----------|------|---------------|
| Strangler Fig (incremental) | Low — rollback is easy at any step | Almost always. Preferred default. |
| Big Bang Rewrite | Very high — entire system rebuilt at once | Only for tiny services with no production traffic |
| Parallel Run | Medium — run old and new simultaneously | When exact parity verification is legally required |

> 🚨 **Warning:** A Big Bang rewrite means your new system is untested under real production load until launch day. This is how projects fail publicly. Avoid unless the service is genuinely throwaway.

---

## 4. How to Pick the First Service to Extract

This is the most underestimated decision in any migration. Pick the wrong first service and your team loses confidence. Pick the right one and you build momentum.

### Criteria for a Good First Service

- **Low coupling** — it touches few other modules. Fewer dependencies = fewer surprises.
- **Well-defined boundary** — you can clearly say "this service owns exactly this data and these operations."
- **Moderate traffic** — not your highest-traffic path on day one. Give yourself room to stabilise.
- **Has a clear owner** — one team is responsible. No shared ownership nightmares.
- **Provides fast visible value** — something that unblocks another team or unlocks a roadmap item.

### Good First Candidates (Common Patterns)

| Service | Why It Is a Good Start | Watch Out For |
|---------|----------------------|---------------|
| Notifications / Email | Loosely coupled. Async. Easy to shadow-test. | Delivery guarantees — use a queue. |
| Authentication / SSO | Clear boundary. High impact if separated. | Every other service depends on it. Get tokens right. |
| File / Media Upload | Stateless. Easy to route. Low business risk. | Large file handling edge cases. |
| Reporting / Analytics | Read-only. No write risk to core data. | May share DB with monolith initially. |
| Search | Independent index. Easy to swap incrementally. | Index freshness — needs event-driven updates. |

### Bad First Candidates

- The core transaction / payment flow — too risky, too many dependencies.
- Anything that requires 3+ other services to work — you will spend months on integration alone.
- A module where ownership is disputed — you will get blocked on politics, not engineering.

> 💡 **Pro Tip:** Draw a simple dependency diagram. The node with the fewest arrows pointing *into* it is usually your best starting point. Low incoming dependencies = low coupling.

---

## 5. Handling the Shared Database Problem

This is the hardest technical challenge in any monolith migration. In a monolith, every module shares the same database and queries it directly. When you extract a service, you cannot just give it its own database overnight — other modules still depend on those tables.

> 🔥 **Real Problem:** You extract the User Service into its own deployment — but the Order module still does a `JOIN` on the `users` table. Now what?

### Phase 1 — Shared Database (Short Term)

While migrating, it is acceptable — temporarily — for the new service and the monolith to share the same database. This is a stepping stone, not a destination.

- Add a logical schema boundary: all tables the new service "owns" get a prefix or separate schema.
- The monolith only **reads** those tables, never writes to them directly.
- The new service is the **single writer**. The monolith calls the service's API if it needs to mutate that data.

### Phase 2 — Database Separation

Once the service is stable and the monolith no longer writes to the shared tables, you can split the database:

1. Export the owned tables to a new database. Migrate historical data.
2. Add a synchronisation layer (CDC — Change Data Capture) so the old DB stays in sync during cutover.
3. Switch the service to the new DB. Run both in parallel for 1–2 weeks.
4. Remove the sync layer. Decommission the old tables from the monolith's DB.

### Useful Tools

| Tool | What It Does | Best Used For |
|------|-------------|---------------|
| Debezium | Change Data Capture — streams DB changes as events | Live sync between old and new DB during cutover |
| Flyway / Liquibase | Schema migration versioning | Managing DB schema changes safely across environments |
| AWS DMS | Cloud-managed database migration | Moving data to cloud DBs with minimal downtime |
| pg_logical / pglogical | Logical replication for Postgres | Low-latency sync for Postgres-to-Postgres splits |

> ⚠️ **Anti-pattern:** Never allow two services to write to the same table. This is the fastest path to data corruption and debugging nightmares. **One table = one owner, always.**

### The Analytical Queries Problem — What Your BI Team Will Say

Here is the conversation that happens at every company after database separation:

> *"I need to run a report joining Users, Orders, and Payments for the Monday board deck."*
> *"Those are now in three separate databases across two services."*
> *"...So?"*

When your data lived in one database, a BI analyst could write a single `JOIN` query and get their answer. After DB separation, that query is impossible. The Users table is in the User Service database. Orders is in the Order Service database. Payments is somewhere else entirely.

**This is not a reason to abandon DB separation. It is a reason to build a data layer.**

You need a **Data Warehouse or Data Lake** that aggregates data from all services for analytical purposes. Transactional databases (your service DBs) are optimised for fast reads and writes of individual records. Analytical databases (Snowflake, BigQuery, Redshift) are optimised for aggregate queries across billions of rows.

```
Architecture:

  User Service DB ──────┐
  Order Service DB ──────┼── CDC (Debezium) ──→ Kafka ──→ ETL Pipeline ──→ Data Warehouse
  Payment Service DB ────┘                                                  (Snowflake / BigQuery)

BI Team queries Snowflake:
  SELECT u.name, o.total, p.status
  FROM users u
  JOIN orders o ON u.id = o.user_id
  JOIN payments p ON o.id = p.order_id
  WHERE o.created_at > '2025-01-01'

Works perfectly. And your transactional services are not affected at all.
```

**How to implement it:**
- Use Debezium to stream change events from each service's DB into Kafka.
- Use a stream processor (Kafka Streams, Flink, or a managed ETL tool like Fivetran) to land data into the warehouse.
- With a well-tuned pipeline, the warehouse typically reflects source data within **minutes to low tens of minutes** under normal load. Do not promise real-time — CDC pipelines have lag, especially under high write volume or during catch-up after an outage.
- **The initial historical backfill is a separate, time-consuming step.** Moving years of existing data into the warehouse is not covered by the streaming pipeline. Plan for a dedicated backfill job — this often takes days for large datasets and must be coordinated with the data team before any service DB is decommissioned.

| Warehouse | Best For |
|-----------|---------|
| Snowflake | Multi-cloud, easy to scale, excellent SQL. Most common choice. |
| BigQuery | Google Cloud native. Serverless. Pay-per-query. |
| Amazon Redshift | AWS native. Good for AWS-heavy organisations. |
| dbt + any warehouse | Transforms raw CDC data into clean analytical models on top of any warehouse. |

> 💡 **Tell your BI team on Day 1:** "We are separating the databases, but we are building a data pipeline so your queries will still work — and will actually be faster at scale." This converts a potential blocker into a stakeholder win.

---

## 6. Feature Flags as a Migration Tool

Feature flags (also called feature toggles) let you deploy new code to production without immediately activating it for all users. They are essential during migration because they give you a kill switch.

### How to Use Feature Flags During Migration

- Deploy the new microservice to production. Keep it **dark** (flag off).
- Enable it for 1% of traffic (internal users or a test cohort). Watch your metrics.
- Gradually increase the rollout percentage. Each step is a validation gate.
- If something breaks: **flip the flag off**. Traffic instantly routes back to the monolith. No rollback deploy needed.

### Example: Extracting the Search Service

```
Flag: 'use_new_search_service' = false for all users.

Week 1: Enable for internal team only. Validate results match monolith.
Week 2: Enable for 5% of users. Monitor p99 latency and error rate.
Week 3: Enable for 50% of users. No regressions observed.
Week 4: Enable for 100% of users. Monitor for 7 days.
Week 5: Remove flag. Delete old search code from monolith.
```

### Feature Flag Tools

- **LaunchDarkly** — Enterprise-grade. Excellent targeting and analytics.
- **Unleash** — Open source, self-hosted. Great for teams that want control.
- **AWS AppConfig / Azure Feature Manager** — Cloud-native options.
- **Simple DB/Redis flag** — For small teams. Works fine for most use cases.

> 💡 **Important:** Feature flags are not just for migrations. Once your team starts using them, they become indispensable for all risky releases. Treat flagging as a first-class engineering practice.

---

## 7. Service Communication & Data Consistency

This is the section most migration docs skip — and then teams discover the hard way that splitting services is only half the job. How services talk to each other, and how you keep data consistent across them, determines whether your architecture is resilient or a house of cards.

### Sync vs Async Communication

Every inter-service call is a choice between two fundamentally different models:

| Pattern | How It Works | When to Use | The Tradeoff |
|---------|-------------|-------------|--------------|
| REST / HTTP | Service A calls Service B and waits | Caller needs result immediately | Tight coupling. If B is slow, A is slow. If B is down, A fails. |
| gRPC | Like REST but binary, typed, and faster | High-throughput internal APIs with defined contracts | Requires protobuf schema management. Harder to debug. |
| Event-driven (Kafka, SQS, RabbitMQ) | Service A publishes an event. B consumes it later | When workflows tolerate eventual consistency | Decoupled and resilient, but async flows are harder to trace. |

**The golden rule:** default to async (events) wherever you can tolerate eventual consistency. Reserve sync calls for interactions where the user is waiting and needs an immediate answer.

### Network Topology — The Hidden Latency Tax

Most communication docs stop at "use REST or gRPC." They skip the question of *where* your services are running relative to each other — and that omission shows up as a line item on the cloud bill and a latency graph that makes no sense.

### The Egress Tax — You Are Donating to Your Cloud Provider

Every byte that crosses an Availability Zone or region boundary is **billed data transfer**. This is not a hypothetical. On AWS, cross-AZ data transfer costs ~$0.01/GB each way. That sounds trivial until you have a chatty checkout flow making 50 inter-service calls per request at 10,000 requests per second.

```
AWS Data Transfer Pricing (approximate, 2025):
  Same AZ:           $0.00/GB   ← free
  Cross-AZ:          $0.01/GB   ← billed both directions = $0.02/GB effective
  Cross-region:      $0.02–$0.09/GB depending on regions

Chatty checkout example:
  50 inter-service calls × 5KB average payload = 250KB per checkout
  10,000 checkouts/second = 2.5GB/second of inter-service traffic
  If services are cross-AZ: 2.5 GB/s × $0.02 = $0.05/second
                           = $4,320/day
                           = ~$130,000/month
  Just in data transfer. Before compute. Before storage.
```

That is not a performance problem. That is a budget line that appears with no warning after you deploy.

**The AZ Topology Diagram**

Here is what a chatty microservices deployment looks like with and without AZ awareness:

```
❌ AZ-Unaware Deployment (the default if nobody thinks about it):

  us-east-1a                    us-east-1b                    us-east-1c
  ┌─────────────────┐           ┌─────────────────┐           ┌─────────────────┐
  │  Order Service  │──────────▶│ Payment Service │           │                 │
  │                 │   $$$      └────────┬────────┘           │                 │
  │                 │                    │ $$$                 │                 │
  └─────────────────┘                    ▼                     │                 │
                                ┌─────────────────┐           │Inventory Service│
                                │  User Service   │──────────▶│                 │
                                └─────────────────┘    $$$    └─────────────────┘

  Every arrow crossing AZ boundary = billable data transfer, both directions.
  Your checkout: 3 cross-AZ hops × every request × millions of requests = expensive surprise.


✅ AZ-Aware Deployment (co-locate chattty services):

  us-east-1a                                      us-east-1b (failover)
  ┌────────────────────────────────────────┐      ┌────────────────────────────────┐
  │  Order Service  ──▶  Payment Service  │      │  Order Service  (replica)      │
  │       │                               │      │  Payment Service (replica)     │
  │       ▼                               │      │  Inventory Service (replica)   │
  │  Inventory Service                    │      └────────────────────────────────┘
  │  User Service                         │
  └────────────────────────────────────────┘

  Same-AZ traffic: free.
  Cross-AZ only for failover, not for every request.
```

**Rule: services that communicate frequently must be co-located in the same availability zone.** Use your cloud provider's placement groups or pod affinity rules to enforce this — it will not happen automatically.

```yaml
# Kubernetes: Pod Affinity — co-locate Order Service with Payment Service
affinity:
  podAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          labelSelector:
            matchExpressions:
              - key: app
                operator: In
                values: ["payment-service"]
          topologyKey: "topology.kubernetes.io/zone"
```

**Service Mesh for Local-First Routing**

Even with affinity rules, load balancers can route requests to instances in other AZs during scaling events or pod restarts. A service mesh (Istio, Linkerd) adds **locality-aware routing**: requests are preferentially sent to the nearest healthy instance before falling back across zone boundaries.

```
Without locality routing:
  Order Service (us-east-1a) → load balancer picks us-east-1c instance at random → $0.02/GB

With Istio locality routing:
  Order Service (us-east-1a) → routes to us-east-1a Payment instance first (free)
  Only crosses AZ if local instances are unhealthy (failover, not default path)
```

This requires zero application code changes. It is a mesh configuration. The egress savings on a chatty system at scale can offset the mesh's own infrastructure cost within weeks.

### The Fan-Out Problem

A single user-facing request triggers a chain of synchronous downstream calls:

```
Client Request
  → API Gateway
    → Order Service        (sync)
      → User Service       (sync)
        → Payment Service  (sync)
          → Inventory Service (sync)
```

Total latency = sum of all hops. One slow service stalls the entire chain. One down service fails the entire request.

**How to avoid fan-out cascades:**
- Make independent downstream calls parallel (fetch User and Inventory concurrently, not sequentially).
- Convert non-critical steps to async events (fire-and-forget after the primary response is returned).
- Use a BFF (Backend for Frontend) layer to aggregate data, so the client makes one call instead of many.

### Distributed Transactions — The ACID Problem

In a monolith with one database, ACID transactions are free. Write to three tables; if anything fails, it all rolls back.

Once services have separate databases, that safety net is gone:

```
Step 1: Order Service    writes order record        success
Step 2: Payment Service  charges customer card      success
Step 3: Inventory Service reserves stock            FAILS

Result: customer was charged, order exists, stock was never reserved.
"Oops" is not a valid database state.
```

### The Saga Pattern

The Saga pattern replaces the single ACID transaction with a sequence of local transactions, each paired with a **compensating transaction** that undoes it if something later fails.

**Choreography** — services react to each other's events. No central coordinator.

```
Order Service      → publishes OrderCreated
Payment Service    → charges card, publishes PaymentCompleted
Inventory Service  → reserves stock, publishes StockReserved
Order Service      → marks order confirmed

IF Inventory fails → publishes StockFailed
  Payment Service  → issues refund  (compensating transaction)
  Order Service    → cancels order
```

*Good for:* simple, low-step flows where teams want maximum decoupling.

*The reality:* choreography does not stay clean. It becomes **event spaghetti**.

```
Real choreography after 6 months of feature additions:

OrderCreated
  → Payment charges card → PaymentCompleted
    → Loyalty service awards points → PointsAwarded
      → Notification sends email → EmailSent
        → Analytics logs event → ...
          → OrderCreated again?? (circular trigger, found at 3am)

No one can answer: "Why is this order stuck?"
You have to reconstruct the flow by reading 6 different service codebases.
```

**For anything involving money, use Orchestration — not Choreography.**

### The DIY Orchestrator Trap

Before reaching for Temporal or Step Functions, most developers do the same thing: they build their own orchestrator using a database table and a cron job.

```
// The "I'll just use a DB table" orchestrator (do not do this)

CREATE TABLE order_workflow (
  order_id UUID,
  current_step VARCHAR,   -- 'CHARGE_CARD', 'RESERVE_STOCK', 'CONFIRM_ORDER'
  status VARCHAR,         -- 'PENDING', 'RUNNING', 'FAILED'
  last_updated TIMESTAMP
);

-- Cron job runs every 30 seconds:
-- "Find all PENDING orders, try the next step, update status."
```

This looks reasonable for five minutes. Then reality arrives:

- The cron job fires twice during a network hiccup. Two workers pick up the same order. The customer is charged twice.
- A step fails halfway through. The cron job retries it. Was the previous attempt fully rolled back? Nobody knows.
- The cron job is down for 20 minutes during a deploy. Orders silently pile up. No alerting.
- After 6 months: the `order_workflow` table has 12 columns, 4 different engineers have added "just one more status," and nobody can explain what `PENDING_RETRY_AFTER_PARTIAL_REFUND` means.

This is the path that generates double-charge support tickets. Use a purpose-built engine.

**Orchestration** — a central Saga Orchestrator acts as a state machine. It tells each service what to do, waits for the result, and handles failures explicitly. There is one place to look when something goes wrong.

**Happy Path vs Compensating Transaction Path — Side by Side**

```
HAPPY PATH (everything succeeds):

  Orchestrator
      │
      ├─[1]──▶ Payment Service: charge $99
      │              │ PaymentCompleted ✅
      │◀─────────────┘
      │
      ├─[2]──▶ Inventory Service: reserve item #A42
      │              │ StockReserved ✅
      │◀─────────────┘
      │
      ├─[3]──▶ Order Service: mark CONFIRMED
      │              │ OrderConfirmed ✅
      │◀─────────────┘
      │
      └── Workflow complete. State: CONFIRMED.


COMPENSATING PATH (Inventory fails after payment succeeded):

  Orchestrator
      │
      ├─[1]──▶ Payment Service: charge $99
      │              │ PaymentCompleted ✅
      │◀─────────────┘
      │
      ├─[2]──▶ Inventory Service: reserve item #A42
      │              │ StockFailed ❌  (out of stock)
      │◀─────────────┘
      │
      ├─[C1]─▶ Payment Service: REFUND $99  ← compensating transaction
      │              │ RefundCompleted ✅
      │◀─────────────┘
      │
      ├─[C2]─▶ Order Service: mark CANCELLED ← compensating transaction
      │              │ OrderCancelled ✅
      │◀─────────────┘
      │
      └── Workflow complete. State: CANCELLED. Customer refunded. No orphaned data.
```

The orchestrator knows exactly which compensating transactions to run because the failure state is explicit. There is no event chain to reconstruct. No database archaeology. The entire saga history is queryable from one place.

**Temporal state machine equivalent:**

```
Saga Orchestrator (state machine):

  State: ORDER_CREATED
    → Tell Payment Service: charge card
    → On success: move to PAYMENT_COMPLETED
    → On failure: move to PAYMENT_FAILED → cancel order → DONE

  State: PAYMENT_COMPLETED
    → Tell Inventory Service: reserve stock
    → On success: move to CONFIRMED → DONE
    → On failure: move to STOCK_FAILED
                  → issue refund (compensating)
                  → cancel order (compensating)
                  → DONE
```

When an order is stuck, you query the orchestrator. You see exactly which state it is in, what step failed, and what compensating actions have been taken. No archaeology required.

*Good for:* any multi-step flow involving money, inventory, or user-visible state changes.
*Watch out for:* the orchestrator's **code** must be stateless — it cannot hold in-memory state between workflow steps, because Temporal may replay it from the beginning on restart. However, Temporal persists the **workflow execution history** to a durable store, so the overall workflow survives crashes and restarts automatically. Stateless code, durable execution — these are not contradictory, they are the design.

**Use a purpose-built durable execution engine:**

| Tool | What It Is | Best For |
|------|-----------|----------|
| **Temporal** | Open-source durable workflow engine. Handles retries, timeouts, state persistence automatically. | Self-hosted or cloud. Recommended for most teams. |
| **AWS Step Functions** | Managed state machine service on AWS. Visual workflow editor. | AWS-native teams who want zero infrastructure. |
| **Conductor** (Netflix OSS) | Microservice orchestration engine. Battle-tested at Netflix scale. | Large-scale, complex workflow graphs. |

```
// Temporal example: order saga that survives restarts and retries automatically
@WorkflowImpl
public class OrderSagaWorkflowImpl implements OrderSagaWorkflow {
  public void processOrder(Order order) {
    try {
      activities.chargePayment(order);       // retried automatically on failure
      activities.reserveInventory(order);    // state persisted between steps
      activities.confirmOrder(order);
    } catch (ActivityFailure e) {
      activities.refundPayment(order);       // compensating transaction
      activities.cancelOrder(order);
    }
  }
}
// If this process crashes mid-saga, Temporal replays the workflow from where it left off.
// No manual state recovery. No lost transactions.
```

### The Outbox Pattern — Reliable Event Publishing

What if your service writes to its DB successfully, but crashes before publishing the event?

```
Payment Service:
  Writes "payment_success" to DB  — success
  Crashes before publishing event — event never sent
  Downstream services never find out
```

The **Outbox Pattern** solves this. Write the event to an `outbox` table *inside the same database transaction* as your business write. A separate relay process reads and publishes it.

```
Payment Service DB transaction (atomic):
  INSERT INTO payments (status = 'success')
  INSERT INTO outbox   (event = 'PaymentCompleted', payload = {...})

Outbox relay (Debezium or custom):
  Reads unprocessed outbox rows
  Publishes to Kafka
  Marks rows as processed
```

Now your event publishing is as reliable as your database write.

### Idempotency — Safe Retries

In distributed systems, retries are inevitable. A timeout fires — was the request processed or not? The caller retries. If your service is not idempotent, the customer gets charged twice.

**Rule:** every write operation that can be retried must be idempotent. The same request, sent twice, produces the same result.

```
// Bad: two calls = two charges
POST /payments { amount: 100 }

// Good: two calls with same idempotency key = one charge
POST /payments { amount: 100, idempotency_key: "order-789-attempt-1" }
```

Store the `idempotency_key` in your DB. On a duplicate request, return the stored result instead of processing again.

---

## 8. Observability & Resilience

In a monolith, one log file, one stack trace, one fix. In microservices, a request hops through five services. The error surfaces in Service E, but the root cause is in Service B. Without proper observability, you are flying blind.

### The Observability Stack You Need

This is not optional. Set it up before your first service goes to production.

| Component | What It Does | Tools |
|-----------|-------------|-------|
| Distributed Tracing | Follow a single request across all services — see every hop, every latency | Jaeger, Zipkin, Grafana Tempo, AWS X-Ray |
| Centralised Logging | Aggregate all service logs in one searchable place | ELK Stack, Grafana Loki |
| Metrics | Track error rates, latency (P50/P95/P99), throughput per service | Prometheus + Grafana, Datadog |
| Correlation IDs | A unique ID generated at the gateway, passed through every service via HTTP header | OpenTelemetry auto-instrumentation, custom middleware |

### Correlation IDs — The Minimum You Must Do

Every incoming request gets a `X-Correlation-ID` header at the API Gateway. Every service logs this ID on every log line. Every outgoing call passes it downstream.

```
Client Request hits API Gateway
  Gateway generates: X-Correlation-ID: "req-7f4a2b"

Order Service receives it → logs it → passes to Payment Service
Payment Service logs it → passes to Inventory Service
...

When an error occurs in Inventory Service:
  grep all logs for "req-7f4a2b"
  Instantly see the complete request journey across all services
```

Without this, debugging means manually correlating timestamps across five different log streams at 2am.

### Context Propagation — The Silent Killer of Traces

Here is the uncomfortable truth: manually passing correlation IDs does not work at scale. One developer forgets to forward the header in an async helper. One new Kafka consumer does not extract it from the message envelope. One gRPC interceptor is missing. The trace breaks. Your "complete request journey" becomes disconnected islands, and you are back to guessing.

```
Request: "req-7f4a2b"

API Gateway → Order Service ✅ (header passed)
Order Service → Payment Service ✅ (header passed)
Payment Service → async worker ❌ (developer forgot)
  async worker → Inventory Service ❌ (context lost)

Trace in Jaeger:
  req-7f4a2b: Gateway → Order → Payment  (trace ends here)
  ???-unknown: Inventory recorded something  (orphaned, no parent trace)

"Something failed somewhere." You are debugging blind again.
```

**The fix: do not trust developers to manually propagate context. Use auto-instrumentation.**

### OpenTelemetry — Use the Agent, Not the SDK Alone

Use **OpenTelemetry** as your instrumentation layer. But the critical distinction is *how* you instrument: use the **auto-instrumentation agent**, not just the manual SDK. The agent instruments your HTTP clients, Kafka producers/consumers, gRPC calls, and database drivers automatically — at the bytecode level, without code changes.

```
// Java: attach OTel agent at startup — zero code changes
java -javaagent:opentelemetry-javaagent.jar \
     -Dotel.service.name=payment-service \
     -Dotel.exporter.otlp.endpoint=http://otel-collector:4317 \
     -jar payment-service.jar

// Python: use the distro — wraps requests, sqlalchemy, kafka automatically
opentelemetry-instrument python app.py

// Node.js: require the SDK at entrypoint
require('@opentelemetry/auto-instrumentations-node').register();
```

With auto-instrumentation, context propagation happens at the framework level. The developer does not pass any headers manually — the HTTP client injects them, the Kafka producer embeds them in message headers, the consumer extracts them. A developer forgetting to forward a header is no longer a failure mode.

```
With auto-instrumentation:
  Payment Service sends Kafka message → OTel agent injects trace context into message headers automatically
  Inventory Consumer receives message → OTel agent extracts trace context automatically
  Trace in Jaeger: Gateway → Order → Payment → Inventory (unbroken)
```

**Setup checklist:**

- [ ] Attach OTel auto-instrumentation agent to **every** service — not just the ones you remember
- [ ] Configure a central OTel Collector — services send to one endpoint, collector fans out to backends
- [ ] Export to Jaeger or Grafana Tempo (local dev), Datadog or Honeycomb (production)
- [ ] Define SLOs **per service type** — a payment API and a search autocomplete have different latency budgets (example: payments P99 < 800ms, search autocomplete P99 < 200ms)
- [ ] Alert when SLOs are breached — not just when the system is already on fire

### Resilience Patterns

Once a method call becomes a network call, failures are normal. Your system must handle them gracefully.

**Circuit Breaker**

Wraps calls to a downstream service. When that service starts failing, the circuit "opens" — calls fail immediately instead of waiting to time out.

```
Normal state (closed):
  Order Service → Payment Service → response

Payment Service starts timing out:
  After 5 failures in 10 seconds → circuit opens
  Order Service → Circuit Breaker → immediate failure (fast fail)
  System stops waiting. Threads freed. Other services unaffected.
  After 60s → circuit half-opens → sends one probe request
  If probe succeeds → circuit closes → normal operation resumes
```

Tools: **Resilience4j** (Java), **Polly** (.NET).

**Retry with Exponential Backoff**

Transient failures can often be resolved with a retry. But naive immediate retries amplify load on an already struggling service.

```
// Bad: immediate retries hammer the struggling service
retry → retry → retry → retry

// Good: exponential backoff with jitter
retry after 1s → retry after 2s → retry after 4s → give up
Add random jitter (±200ms) so all callers don't retry simultaneously
```

**Timeouts**

Every outbound call must have a timeout. No exceptions. Without timeouts, a slow service holds your threads open indefinitely — eventually exhausting your thread pool.

```
DB queries:              2s timeout
Internal service calls:  3s timeout
External payment APIs:  10s timeout
```

**Bulkheads**

Isolate failure domains so one failing integration cannot consume all resources and take everything down.

```
Without bulkheads:
  All service calls share one thread pool (100 threads)
  Payment goes slow → consumes all 100 threads → other calls queue → system freezes

With bulkheads (isolated thread pools per downstream):
  Payment calls:   20 threads
  Inventory calls: 20 threads
  User calls:      20 threads
  Payment goes slow → only Payment pool exhausted → other services keep working
```

**The Classic Cascading Failure — Stopped**

```
Without resilience patterns:
  Inventory slow → Order waits → Order threads exhausted → Gateway queues → System down

With circuit breaker + timeout + bulkhead:
  Inventory slow → circuit opens after threshold
  Order fast-fails Inventory calls → returns degraded response
  System keeps running. Inventory recovers. Circuit closes. Normal operation.
```

---

## 9. Security Between Services

In a monolith, a method call is a method call. In microservices, every call is a network call. The internal network is not safe to treat as trusted.

> 🔐 **Zero Trust Principle:** Never trust a request simply because it comes from inside your own network. Verify every caller's identity on every request.

### JWT Propagation

When a user authenticates at the API Gateway, they receive a JWT. This token must flow downstream with every inter-service call. Each service validates it independently.

```
Client authenticates at API Gateway
  Gateway issues JWT: { user_id, roles, expiry }

API Gateway → Order Service (Authorization: Bearer <JWT>)
  Order Service validates JWT signature, checks user permissions

Order Service → Payment Service (Authorization: Bearer <JWT>)
  Payment Service validates same JWT — does not trust Order Service blindly
```

**Rules:**
- Validate the JWT signature on **every** service, not just at the gateway.
- Keep JWT expiry short (15–60 minutes). Use refresh tokens for long sessions.
- Never pass credentials (username/password) between services — only tokens.

### mTLS — Mutual TLS

JWT identifies *who the user is*. mTLS identifies *which service is calling*. Both the caller and the receiver present certificates. A service without a valid certificate cannot connect.

```
Without mTLS:
  Any process on the internal network can call Payment Service
  Compromised Notification Service → attacker calls Payment Service directly

With mTLS:
  Payment Service only accepts connections from services with a valid cert
  Notification Service has no cert for Payment API → connection refused
```

**Options:**
- **Service Mesh (Istio / Linkerd)** — handles mTLS automatically at the infrastructure level. Services get certs automatically. No code changes needed. Recommended at scale.
- **Manual mTLS** — manage certs yourself. Works but operationally expensive.

### Zero Trust Is a Practice, Not a Header

mTLS with long-lived certificates is not Zero Trust. It is a locked front door with the key hidden under the mat.

Here is the real threat model that most teams miss:

```
Attacker compromises Notification Service pod.

Inside the pod:
  cat /var/run/secrets/kubernetes.io/serviceaccount/token
  → Service account token: eyJhbGci...  (valid, not expired, full cluster permissions)

The attacker now has:
  - The Notification Service's mTLS certificate (valid for 1 year)
  - The Kubernetes service account token (valid until rotated, possibly never)
  - Network access to call any internal API that trusts this certificate

With a 1-year cert validity, "Mutual TLS" is just a permanent back door
with a padlock on the front and an open window around back.
```

True Zero Trust requires **short-lived credentials with automatic rotation**. The standard for this in the cloud-native world is **SPIFFE/SPIRE**.

### The Identity Bootstrapping Problem

Before you can rotate certificates, you have a more fundamental problem: how does a service get its *first* certificate?

This is called the **bootstrapping problem**, and most teams solve it badly without realising it.

```
Bad approaches teams actually use:

Option A — Hardcode certs into the Docker image:
  FROM node:20-alpine
  COPY payment-service.crt /etc/certs/   ← cert baked into image
  COPY payment-service.key /etc/certs/   ← private key baked into image
  CMD ["node", "server.js"]

  Problem: cert is in your image registry. Every engineer with pull access
  has the private key. Rotating it means rebuilding and redeploying the image.
  This is 2010 PKI practice in a container wrapper.

Option B — Mount certs as Kubernetes Secrets at deploy time:
  Problem: who creates the Secret? Usually a human. With what cert?
  Usually one that was generated manually and lives in someone's ~/Downloads folder.
  When does it expire? Nobody knows. Who rotates it? "We'll set a reminder."

Option C — Long-lived certs from your internal CA issued once per service:
  Problem: this is exactly the "1-year cert = 9-month backdoor" scenario above.
  The cert identity is correct — but the exposure window after a compromise is
  measured in months, not minutes.
```

The root issue: **manual cert provisioning does not scale to 50+ services**, and any human-touched certificate is an operational risk. You need a system that can cryptographically attest "this is genuinely the payment-service running in the production namespace" — at pod startup, automatically, without a human in the loop.

### SPIFFE/SPIRE — Identity for Workloads

**SPIFFE** (Secure Production Identity Framework for Everyone) is an open standard for workload identity. Every service gets a cryptographically verifiable identity called a SPIFFE ID:

```
spiffe://company.com/service/payment-service
spiffe://company.com/service/order-service
```

**SPIRE** (the SPIFFE Runtime Environment) is the implementation. It automatically issues and rotates short-lived X.509 certificates to every workload — no manual cert management.

```
Without SPIRE (manual or mesh-issued long-lived certs):
  cert issued: January 1
  cert expires: December 31  (1 year validity)
  Attacker compromises pod on March 1 → has valid cert for 9 more months

With SPIRE (auto-rotating short-lived certs):
  cert issued: every hour (configurable — can be minutes)
  cert expires: 1 hour later
  Attacker compromises pod on March 1 → cert expires in < 60 minutes
  By the time they pivot to another service, the cert is invalid
  Blast radius: contained
```

**How SPIRE works in practice:**

```
1. SPIRE Server runs in your cluster (or managed via HCP Vault, AWS SPIRE, etc.)
2. SPIRE Agent runs as a DaemonSet on every node
3. On pod startup:
   → SPIRE Agent attests the workload identity (pod labels, namespace, service account)
   → Issues a short-lived X.509 SVID (SPIFFE Verifiable Identity Document)
   → Delivers it to the workload via a Unix socket (no secrets in env vars)
4. Certificates auto-rotate before expiry — the workload never handles rotation logic
5. mTLS between services uses these SPIFFE SVIDs — each service can verify exactly
   which workload identity it is talking to, not just which certificate
```

Istio integrates natively with SPIFFE/SPIRE. If you are running Istio, enabling SPIRE as the CA backend replaces Istio's default cert issuance with SPIFFE-compliant short-lived identities — zero application changes.

**Cert rotation rules (minimum bar):**

| Environment | Max Cert Lifetime | Rotation Trigger |
|-------------|------------------|-----------------|
| Production (payment, auth, PII services) | 1 hour | Automatic via SPIRE |
| Production (general services) | 24 hours | Automatic via mesh CA |
| Development | 7 days | Acceptable for dev speed |
| Never acceptable | > 90 days | A cert that lives this long is a security debt |

> 🔐 **The test:** Can you rotate every service certificate in your cluster right now, with zero downtime, without any engineer doing anything manually? If not, your mTLS implementation has a hole in it. SPIRE + a service mesh with auto-rotation closes that hole.

### Service Mesh — Honest Cost vs Benefit

A service mesh runs a **sidecar proxy** alongside every pod. It handles mTLS, retries, timeouts, and circuit breaking at the infrastructure level — without your application code needing to implement any of it.

```
Without service mesh:
  Each team implements auth, retries, timeouts, tracing in their own service.
  Inconsistent. Error-prone. Hard to enforce standards across 20 teams.

With Istio (sidecar model):
  Sidecar proxy intercepts all inbound and outbound traffic per pod.
  Handles mTLS, retries, circuit breaking, and tracing automatically.
  Teams write business logic only. Platform team manages policies centrally.
```

*Good for:* teams at scale with a dedicated platform team.

**But be honest about the cost before you commit.**

Every sidecar is an additional process running inside every pod — consuming real CPU and memory:

```
Typical Istio sidecar overhead per pod:
  CPU:    50–100m CPU (millicores) at idle, up to 200–500m under load
  Memory: 50–100MB per sidecar

At 200 microservices:
  200 sidecars × 75m CPU (avg)    = 15 additional CPU cores just for proxies
  200 sidecars × 75MB memory      = 15GB additional RAM just for proxies

That is real infrastructure cost, running 24/7, before a single line of
your business logic executes.
```

This overhead is usually worth it at scale — the security, observability, and reliability benefits are real. But walk into it with eyes open, not with the assumption that "it's just a sidecar."

**Ambient Mesh — The Sidecar-Less Alternative**

Istio introduced **Ambient Mesh** (stable as of Istio 1.22) to address exactly this overhead. Instead of injecting a sidecar into every pod, Ambient Mesh moves the proxy functionality to two shared per-node components:

```
Sidecar Model (traditional):
  Pod A: [app container] + [envoy sidecar]  ← sidecar per pod
  Pod B: [app container] + [envoy sidecar]
  Pod C: [app container] + [envoy sidecar]
  ...200 sidecars for 200 pods

Ambient Mesh Model:
  Node 1: [ztunnel]  ← one lightweight L4 proxy per node (handles mTLS)
           ↓
          [waypoint proxy]  ← one L7 proxy per namespace (handles policies, retries)
  Pod A: [app container only]  ← no sidecar
  Pod B: [app container only]
  Pod C: [app container only]
  ...zero sidecars
```

Ambient Mesh provides the same mTLS, observability, and traffic management at a fraction of the per-pod overhead. If you are starting a new service mesh deployment today, evaluate Ambient Mesh before defaulting to the sidecar model.

**Linkerd** also offers a lighter-weight sidecar than Istio (written in Rust, ~5MB memory per proxy vs Istio's ~50MB). If the full Istio feature set is not required, Linkerd is a well-regarded simpler alternative.

| Option | Overhead | Features | When to Use |
|--------|---------|---------|-------------|
| Istio (sidecar) | High (~75MB/pod) | Full feature set | Large orgs needing advanced traffic policies |
| Istio (Ambient) | Low (per-node only) | Full feature set | New deployments — preferred over sidecar model |
| Linkerd | Low (~5MB/proxy) | Core mTLS + observability | Teams wanting simplicity over feature breadth |
| No mesh | Zero | DIY per service | Small teams, early-stage — revisit at 20+ services |

### Secrets Management

Never hardcode API keys, database passwords, or certificates in code or environment variables committed to source control.

| Tool | Use Case |
|------|----------|
| HashiCorp Vault | Self-hosted. Secrets are dynamically generated and short-lived. |
| AWS Secrets Manager | Cloud-native. Integrates with IAM. Easy for AWS-heavy teams. |
| Kubernetes Secrets + Sealed Secrets | K8s-native. **Important:** raw Kubernetes Secrets are base64-encoded, not encrypted at rest by default. Always enable envelope encryption or use Sealed Secrets before treating them as secure. |

### Authorization at Scale — The IDOR Problem

mTLS tells you *which service* is calling. JWT tells you *which user* is authenticated. Neither tells you whether that user is **authorised to access the specific resource being requested**.

This gap is where Insecure Direct Object Reference (IDOR) vulnerabilities live in microservices — and it is systematically worse than in monoliths.

In a monolith, authorisation logic was written once, in one place. In microservices, every service makes its own authorisation decisions. In practice, what actually happens is this:

```
Scenario: User A tries to view User B's order history.

Request path:
  Client (authenticated as User A)
    → API Gateway  ✅ validates JWT, confirms User A is authenticated
      → Order Service  ← receives request with user_id=A, order_id=789
          → "Let me fetch this order..."
          → calls User Service: GET /users/B/profile
              → User Service checks: "Is the caller authenticated?" YES (valid JWT)
              → User Service returns User B's profile to Order Service
              → Order Service returns User B's data to User A

User A just read User B's profile. No error was thrown.
User Service trusted the internal caller (Order Service) without checking
whether the *original user* (A) had permission to access User B's data.
```

This is not a hypothetical. It is the default behaviour when teams assume that internal service calls are pre-authorised. The check that should have happened — "does User A have permission to read User B's profile?" — was never done because both the API Gateway and User Service assumed someone else had checked it.

**The monolith did this correctly by accident.** Permission checks lived in shared middleware that ran on every request. In microservices, that middleware is gone. Every service re-invents it — or skips it.

### Policy-as-Code — Open Policy Agent (OPA)

The fix is to move authorisation logic out of application code and into a **central policy engine**. Every service delegates the "can User X do Action Y on Resource Z?" question to the same engine, written in the same language, enforced consistently.

**Open Policy Agent (OPA)** is the cloud-native standard. Policies are written in Rego (a declarative language), versioned in git, and deployed as a sidecar or central service.

```
// OPA policy: who can read a user profile? (policies/user_profile.rego)

package authz.user_profile

default allow = false

# Users can read their own profile
allow {
    input.method == "GET"
    input.path == ["users", input.user_id, "profile"]
    input.jwt.sub == input.user_id        # caller's JWT subject must match
}

# Admins can read any profile
allow {
    input.method == "GET"
    "admin" in input.jwt.roles
}

# Internal service calls must STILL pass a user context — no blanket trust
allow {
    input.caller_service == "order-service"
    input.method == "GET"
    input.jwt.sub == input.user_id        # original user context still required
}
```

The policy is the same regardless of which service calls User Service. The check is not "is the caller authenticated?" — it is "does the original user have permission for this specific resource?"

**Two deployment patterns:**

```
Pattern A — OPA as a sidecar (per-service):
  Every pod gets an OPA sidecar.
  Service sends authorisation query to localhost:8181 before processing any request.
  Decision happens in-process, no network hop.

  User Service pod:
    [User Service app] → [OPA sidecar at localhost:8181]
    App: "Can JWT sub=A access /users/B/profile?"
    OPA: "No. Deny."
    App returns 403. User B's profile is never loaded.

Pattern B — OPA as a centralised policy engine:
  One OPA cluster. All services query it.
  Easier to manage policies centrally.
  Adds a network call per authorisation decision — ensure it is on the same AZ.
```

**Why this matters beyond IDOR:**

OPA eliminates the scenario where 20 teams write 20 different authorisation implementations in 20 different languages, and one of them gets it slightly wrong on an edge case at 2am.

```
Without OPA:
  Team A's service (Java):   checks roles in JWT claims  ✅ correct
  Team B's service (Python): checks roles from DB lookup  ✅ correct but slow
  Team C's service (Go):     checks "if user_id == resource.owner_id"  ⚠️ misses role-based cases
  Team D's service (Node):   "it's an internal call, must be fine"  ❌ IDOR vulnerability

With OPA:
  All four services query the same policy.
  Policy is in git. Policy has tests. Policy changes go through code review.
  A vulnerability in the policy affects all services — and is fixed for all services simultaneously.
```

| Deployment | Latency | Ops Complexity | When to Use |
|-----------|---------|---------------|-------------|
| OPA sidecar (per pod) | ~1ms (localhost) | Higher — sidecar per pod | High-throughput services where latency matters |
| OPA centralised | ~5–15ms (network) | Lower — one cluster | Starting out, or < 20 services |
| Service mesh (Istio AuthorizationPolicy) | ~0ms (inline) | Zero extra infra | When already running Istio — use it for coarse-grained policies |

> 🔐 **The rule:** no service may return data about Resource R to caller C without checking whether the *original authenticated user* has permission for R. The fact that the caller is an internal service is not authorisation. It is identity — a different thing entirely.

---

## 10. Service Discovery & Configuration Management

You have split your services. Now Order Service needs to talk to Payment Service. What address does it use?

If the answer is "we hardcode the IP" — that is a production incident waiting to happen. IPs change. Services restart. Containers move to new nodes. You need service discovery.

### Service Discovery

| Pattern | How It Works | Example |
|---------|-------------|---------|
| DNS-based | Services register under a DNS name. Callers resolve the name. | Kubernetes — `payment-service.default.svc.cluster.local` |
| Registry-based | Services register IP/port on startup. Callers query the registry. | Consul, Eureka |
| Service Mesh | The mesh handles routing. Services call a logical name. Sidecar resolves it. | Istio, Linkerd |

**On Kubernetes:** DNS-based discovery is built in. Every service gets a DNS name automatically. Use it.

```
# Order Service calls Payment Service by DNS name — no hardcoded IPs
http://payment-service.payments.svc.cluster.local:8080/charge

# If Payment pods restart or move, DNS still resolves correctly
```

**Not on Kubernetes:** use **Consul**. Lightweight, battle-tested, works across any infrastructure.

### Centralised Configuration Management

12 microservices, each with their own copy of the Stripe API key. Someone rotates the key. How many places do you update? How many deployments do you trigger?

Centralised config: one place to manage all configuration. Services pull config at startup (or dynamically at runtime).

| Tool | Best For |
|------|----------|
| AWS AppConfig | AWS teams. Supports feature flags too. |
| HashiCorp Consul KV | Already using Consul for discovery? Use it for config too. |
| Spring Cloud Config | Java/Spring teams. Git-backed config server. |
| Kubernetes ConfigMaps | Simple config in K8s. |

**Rules:**
- No secrets in config files committed to git. Use a secrets manager (see [Section 9](#9-security-between-services)).
- Config changes should not require a service redeployment — support dynamic reload.
- Every service logs which config version it loaded at startup. This saves hours during incident debugging.

### API Versioning Between Services

When services evolve independently, contracts can break. Order Service v2 changes a field name — Payment Service still expects the old schema. Production breaks.

```
// Bad: rename field, deploy, downstream services explode
{ "amount": 100 }  becomes  { "total": 100 }

// Good: add new field, deprecate old one, give consumers a migration window
{ "amount": 100, "total": 100 }  // both present during transition

// After all consumers have migrated to "total": remove "amount"
```

**Rules:**
- Never delete or rename a shared API field without a deprecation window.
- Version your endpoints: `/v1/charge` and `/v2/charge` can coexist.
- Add a `Sunset` header to deprecated endpoints so clients know the removal date: `Sunset: Sat, 31 Dec 2025 23:59:59 GMT`. Log a warning every time the deprecated version is called — this creates a paper trail and pressures consumer teams to migrate.
- Remove old versions only after **confirming zero consumer traffic** via your API Gateway's per-version request metrics. Do not rely on teams self-reporting migration completion — check the numbers.
- Use **Consumer-Driven Contract Testing** (Pact, Spring Cloud Contract) to catch breaking changes in CI before they reach production.

---

## 11. Testing Strategy for Microservices

Contract tests are a start, but microservices need a full, layered testing strategy.

### The Testing Pyramid

```
         /‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾\
        /   Chaos / Load     \   "What breaks under stress?"
       /‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾\
      /    End-to-End Tests    \  "Does the full user journey work?"
     /‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾\
    /   Contract Tests (CDC)    \  "Do services agree on the API?"
   /‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾\
  /    Integration Tests         \  "Does the service work with its DB / queue?"
 /‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾\
/          Unit Tests               \  "Does this function work in isolation?"
‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾
         Many    Fast    Cheap  →  Few  Slow  Expensive
```

**Unit Tests** — Test individual functions in isolation. No external dependencies. Fast. The bulk of your suite.

**Integration Tests** — Test a service with its real database, real message queue, real cache. Use `TestContainers` to spin up Docker containers for dependencies in CI. Catches DB query bugs, migration issues, serialisation problems.

**Contract Tests (Consumer-Driven)** — The most important layer unique to microservices. Each consumer defines the API contract it expects. The provider runs those contracts in its own CI pipeline.

```
Order Service (consumer) defines:
  "I POST to /payments with { amount, currency, user_id }
   and expect { transaction_id, status } back"

Payment Service (provider) runs this contract in its own CI:
  If Payment changes the response schema → contract test fails → caught in CI
  Not caught in production at 2am
```

Tools: **Pact**, **Spring Cloud Contract**.

**End-to-End Tests** — Spin up the full system and test real user journeys. Keep the suite small — critical paths only (checkout, login, core workflows). These are slow and expensive to maintain.

**Chaos / Resilience Tests** — Intentionally kill services, inject latency, exhaust resources. Validate that circuit breakers, timeouts, and fallbacks behave as designed.

```
Example chaos test:
  Kill Inventory Service
  → Does Order Service circuit breaker open within 10s?
  → Does the user receive a graceful degraded response (not a 500)?
  → Does PagerDuty alert fire within 2 minutes?
```

Tools: **Chaos Monkey** (Netflix), **Gremlin**, **Litmus** (Kubernetes-native).

---

## 12. Deployment & Infrastructure

How services run matters as much as how they are built.

### Containerisation First

Before worrying about orchestration, containerise every service with Docker. The container image is the unit of deployment — the same image runs in local dev, staging, and production.

```dockerfile
FROM node:20-alpine
WORKDIR /app
COPY package*.json ./
RUN npm ci --only=production
COPY . .
EXPOSE 3000
CMD ["node", "server.js"]
```

### Orchestration Platform

| Platform | Best For |
|----------|----------|
| Kubernetes (K8s) | Industry standard. Scheduling, scaling, discovery, rolling deploys. Steep learning curve. |
| AWS ECS / Fargate | Simpler than K8s. No cluster management. Good for AWS-native teams. |
| Nomad | Lighter weight. Good for teams already using HashiCorp tooling. |

> 💡 For most teams: ECS/Fargate or a managed K8s service (EKS, GKE, AKS) gives 90% of the benefit at a fraction of the operational burden of self-managed Kubernetes.

### Rollout Strategies

**Blue/Green Deployment** — Two identical environments. Switch traffic 100% to the new version. Old version stays live for instant rollback.

```
Traffic: 100% → Blue (old version)
Deploy new version to Green, run smoke tests
Traffic: 100% → Green  (instant switch)
Issue found? Traffic: 100% → Blue  (instant rollback)
```

**Canary Release** — Route a small percentage of real traffic to the new version. Observe. Gradually increase.

```
1% → new version  (30 min observation)
5% → new version  (1 hour)
50% → new version (24 hours)
100% → new version
```

**Rolling Deployment** — Replace instances of the old version one at a time. Requires v1 and v2 to be backward compatible during the rollout window.

### Autoscaling

Define scaling rules per service based on its traffic profile.

```yaml
# Kubernetes HPA example
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
spec:
  minReplicas: 2
  maxReplicas: 20
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          averageUtilization: 70
```

---

## 13. The Distributed Monolith — The Anti-Pattern You Must Avoid

This is the most common outcome of failed microservice migrations. The team did all the work — split the codebase, set up separate deployments, gave each service a name — and ended up with something worse than what they started with.

### What a Distributed Monolith Looks Like

```
"Microservices" that are actually a distributed monolith:

  Order Service  --sync-->  User Service  --sync-->  Auth Service
       |
     sync
       v
  Payment Service  --sync-->  Inventory Service
       |
       +--- all five services still sharing the same Postgres database
```

### Symptoms

- **Synchronous call chains everywhere** — no service can respond without calling two others first.
- **Shared database still exists** — the "temporary" shared DB became permanent.
- **Deployments must be coordinated** — "we can't deploy Order Service until Payment Service is deployed first."
- **One service down = everything down** — no resilience despite being "microservices."
- **Latency worse than the monolith** — what was a function call is now 5 synchronous network hops.
- **Shared libraries containing business logic** — every service imports `common-utils`, and updating it requires redeploying all 20 services simultaneously.

### The Shared Library Trap — A Monolith in a Trench Coat

This last symptom deserves its own section because it is invisible until it is too late.

The impulse is completely natural: "We should not repeat this auth helper in every service. Let's put it in a shared library." Six months later, `common-utils` contains auth helpers, date formatters, payment validation logic, domain model classes, and half of your business rules. Every service depends on it.

Now someone needs to change how the `Order` model is represented. They update `common-utils` v2.1. Suddenly:

```
common-utils v2.1 is released

Services that must now be updated and redeployed:
  order-service        ← uses Order model
  payment-service      ← uses Order model
  notification-service ← uses Order model
  reporting-service    ← uses Order model
  admin-service        ← uses Order model
  ...

Congratulations: you have a monolith. It is just distributed across 20 deployments
and takes 4 hours to release instead of one.
```

**DRY (Don't Repeat Yourself) is a trap in microservices.**

DRY is the right principle within a single service. Across service boundaries, it creates coupling. The cure is worse than the disease.

**What to share vs what to duplicate:**

| Category | Share or Duplicate? | Reason |
|----------|-------------------|--------|
| HTTP client boilerplate, retry logic, auth middleware | ✅ Share as a thin infrastructure library | No business logic. Rarely changes. Changes do not affect service behaviour. |
| Logging / tracing setup, config loading utilities | ✅ Share | Pure infrastructure. Stable. |
| Domain model classes (Order, Payment, User) | ❌ Duplicate per service | Each service should own its own representation. A Payment Service's view of an Order is different from a Notification Service's view. |
| Business logic, validation rules, calculation functions | ❌ Duplicate or extract into its own service | Business logic belongs in one place — its service. Sharing it creates invisible coupling. |
| Database schema, ORM models | ❌ Never share across service boundaries | This is just a shared database with extra steps. |

**The rule:** if updating the shared library requires any consuming service to change its behaviour or redeploy, the library contains too much. Strip it back to pure infrastructure utilities with no business logic.

### Why It Happens

The distributed monolith is not usually a deliberate choice. It is the default outcome when teams skip the hard architectural work and default to familiar patterns.

- Domain boundaries were never defined. Services were split by technical layer (controller/service/repo) rather than by business domain.
- Teams defaulted to sync REST because it was familiar.
- The shared database was never split because "we'll do it next quarter."
- No one enforced the rule: each service owns its own data.

### How to Avoid It

- Define domain boundaries **before** splitting. If you cannot draw a clean boundary, do not split yet.
- Default to **async events** for cross-service communication wherever you can.
- Enforce database ownership from day one. One service = one database.
- Treat synchronous runtime dependencies as a coupling red flag. **If a service has more than 2 synchronous runtime dependencies on other services, it is a warning sign** — either the service boundary is wrong, or those calls should be async.

> 🚨 **The test:** Can you deploy Service A without coordinating with Service B's team? If not, you have a distributed monolith — regardless of how many services you have.

---

## 14. When NOT to Migrate

This is the most honest section of this document — and arguably the most valuable. Many teams migrated and deeply regretted it.

### Do Not Migrate If...

| Condition | What Actually Happens | Better Alternative |
|-----------|----------------------|-------------------|
| Small team (< 8 engineers) | Ops overhead swamps feature work. On-call is brutal. | Well-structured or modular monolith. |
| Early-stage product (< 1 year) | Requirements change fast. Service contracts slow you down. | Monolith with good module boundaries. Split later. |
| Monolith is poorly tested | You migrate bugs into distributed bugs. Harder to debug. | Add tests + clean architecture first. |
| No DevOps / Platform team | Service discovery, secrets, tracing, CI/CD all need owners. | Invest in platform capability before splitting services. |
| No clear domain boundaries | Services become mini-monoliths calling each other in circles. | Run Event Storming workshop first. |
| "We want to use Kubernetes" | Infra tooling is not a reason to redesign your architecture. | Run the monolith on Kubernetes if you must. |

### The Modular Monolith — Often the Right Answer

A modular monolith is a monolith with strict internal boundaries. Each module has its own package, service class, and data layer — but it all deploys together.

You get most of the team independence benefits at a fraction of the operational cost. Shopify and Stack Overflow have stayed on modular monoliths at enormous scale.

> 🎯 **The Real Test:** Ask yourself: *what specific engineering pain are we solving with this migration?* If you cannot name it precisely, you are not ready.

---

## 15. Step-by-Step Migration Playbook

Use this as a checklist. Each phase has clear exit criteria. Never skip one under time pressure — that is how migrations go sideways.

---

### Phase 0 — Preparation *(Weeks 1–4)*

1. Map the monolith: document all modules, their responsibilities, and their data dependencies.
2. Identify domain boundaries using Event Storming or similar workshops.
3. Agree on the first target service — use the criteria from [Section 4](#4-how-to-pick-the-first-service-to-extract).
4. Set up baseline observability: error rates, latency, throughput for the monolith.
5. Set up your API Gateway / proxy layer in front of the monolith (no traffic changes yet).
6. Decide on communication patterns: which cross-service calls will be sync vs async?
7. Set up centralised config management and secrets manager. *(Note: this is a non-trivial infrastructure task — allocate 2–3 weeks, not a single sprint item. It must be done before any service handles production traffic.)*

> ✅ **Exit Criterion:** You can route traffic for one specific endpoint through the gateway with zero behaviour change, and you have dashboards showing it.

---

### Phase 1 — Build the New Service *(Weeks 4–8)*

1. Build the new service. Scope it narrowly — do not over-engineer it.
2. Set up its CI/CD pipeline. It must deploy independently.
3. If it needs data, use the shared DB temporarily (see [Section 5](#5-handling-the-shared-database-problem)).
4. Add OpenTelemetry instrumentation. Ensure correlation IDs flow through all calls.
5. Implement circuit breakers and timeouts for all outbound calls.
6. Write consumer-driven contract tests for every API this service calls or exposes.
7. Configure JWT validation middleware.

> ✅ **Exit Criterion:** New service passes all contract tests. Deploys independently. Has its own monitoring dashboard. Circuit breakers and timeouts are in place.

---

### Phase 2 — Shadow Traffic *(Weeks 8–10)*

1. Enable shadow mode: route real traffic to **both** old and new. Do not use new service responses yet.
2. Log all discrepancies between old and new responses.
3. Fix every discrepancy. Do not proceed until divergence is below 0.1%.

> ✅ **Exit Criterion:** Less than 0.1% divergence over 7 days of shadow traffic.

---

### Phase 3 — Incremental Cutover *(Weeks 10–14)*

1. Enable feature flag. Start at **1%** real traffic to new service.
2. Monitor: error rate, P50/P95/P99 latency, downstream impact.
3. Increment: 1% → 5% → 25% → 50% → 100%. Spend at least 48 hours at each step.
4. Define rollback trigger: if error rate exceeds X%, auto-disable the flag.

> ✅ **Exit Criterion:** 100% traffic on new service for 14 days with no P1 incidents.

---

### Phase 4 — Database Separation *(Weeks 14–20, if applicable)*

1. Follow the DB separation steps from [Section 5](#5-handling-the-shared-database-problem).
2. Use CDC to sync old and new DB during the transition window.
3. Decommission monolith's copy of the data only after 2 weeks of clean operation.
4. Confirm the data warehouse pipeline is receiving events from the new service DB before decommissioning the old tables.

> ✅ **Exit Criterion:** New service DB is the sole source of truth for its domain. Old tables are decommissioned. Data warehouse pipeline is receiving live updates from the new DB with no gaps in data.

---

### Phase 5 — Cleanup

1. Delete the migrated module's code from the monolith.
2. Remove the feature flag.
3. Remove shadow routing.
4. Update architecture diagrams and runbooks.
5. Run a chaos test: kill the new service, confirm the system degrades gracefully.
6. Write a migration retrospective. What went well? What would you do differently?

---

## 16. What I Have Seen Go Wrong

Theory is clean. Production is not.

---

### Failure Pattern 1: Migrating Into Chaos

> **What Happened:** A team extracted three services simultaneously from a tangled codebase. The services called each other in circular loops. Latency tripled. A bug in one cascaded through all three, making debugging nearly impossible.
>
> **Root Cause:** They migrated entangled modules, not services with clean domain boundaries.
>
> **Lesson:** Clean your domain boundaries inside the monolith first. A modular monolith is a valid intermediate step. Migrate only well-isolated modules.

---

### Failure Pattern 2: The Database Was Never Actually Split

> **What Happened:** Seven services were extracted over 18 months. All still shared the same Postgres database. The "temporary" shared DB became permanent. One bad migration script locked tables and took down all seven services at once.
>
> **Root Cause:** DB separation was always "next quarter's problem."
>
> **Lesson:** Put database separation on the roadmap from Day 1. Permanent shared DB is a trap.

---

### Failure Pattern 3: No Rollback Plan Under Enterprise Pressure

> **What Happened:** A migration was approved by multiple stakeholders. When issues appeared during cutover, the team hesitated to rollback — "too many people are watching." They pushed forward. The incident lasted 4 hours.
>
> **Root Cause:** Rollback was seen as failure, not as engineering hygiene.
>
> **Lesson:** Define your rollback trigger before the migration starts. Write it in the runbook. Make it automatic. Rollback is not failure — it is the plan working.

---

### Failure Pattern 4: Observability Added as an Afterthought

> **What Happened:** A team fully migrated to microservices and had their first major production incident across five services. No distributed tracing. No correlation IDs. The on-call engineer spent 3 hours manually correlating timestamps across five log dashboards. A single misconfigured timeout took a full day to find.
>
> **Root Cause:** Observability was treated as "we'll add it later." Later became never.
>
> **Lesson:** Distributed tracing and correlation IDs are the foundation. Set them up before your first service goes to production — not after your first incident.

---

### Failure Pattern 5: Internal APIs Treated as Trusted

> **What Happened:** Services communicated internally with no authentication. When one service was exploited, the attacker could call any internal API — including payment and admin services — with no friction. The blast radius was the entire internal network.
>
> **Root Cause:** "It is internal so it is safe" — a monolith assumption carried into a distributed system.
>
> **Lesson:** Validate JWTs on every service. Implement mTLS or a service mesh. Zero Trust is not paranoia — it is the correct security model for distributed systems.

---

### Failure Pattern 6: Choreography Turned into Event Spaghetti

> **What Happened:** A team used Choreography Sagas for all multi-step workflows, including the payment and order confirmation flow. Over 18 months of feature additions, new services started listening to existing events and publishing their own in response. Eventually, an OrderCreated event from the Loyalty Service triggered a downstream chain that, through three intermediate services, published another OrderCreated event — creating an infinite loop. It ran undetected for two hours, duplicating records and charging test accounts.
>
> **Root Cause:** Choreography has no central state. Nobody owned the full picture of what listened to what. The event graph was only discoverable by reading all service codebases simultaneously.
>
> **Lesson:** Use Orchestration (Temporal, AWS Step Functions) for any flow involving money, inventory, or irreversible actions. Use Choreography only for simple, stable, low-stakes notification workflows. If you cannot draw the full event flow on a whiteboard from memory, it is too complex for Choreography.

---

### Failure Pattern 7: Internal Calls Treated as Pre-Authorised

> **What Happened:** A B2B SaaS platform migrated to microservices. Each service validated the JWT on inbound requests — but internal service-to-service calls passed the JWT along without re-checking resource ownership at each hop. A bug in the Order Service constructed a request to User Service with a mismatched `user_id` parameter. User Service received a valid JWT (authenticated) and an internal caller it trusted — and returned the wrong user's data without error. The issue was not discovered until a customer complained about seeing another customer's address pre-populated in their checkout.
>
> **Root Cause:** Authentication (is this caller who they say they are?) was confused with authorisation (does this caller have permission for this specific resource?). Services assumed that internal callers had already done the permission check upstream. None had.
>
> **Lesson:** Every service must check whether the original authenticated user has permission for the specific resource being requested — regardless of whether the caller is a user or an internal service. OPA (Open Policy Agent) as a sidecar enforces this consistently without re-implementing it per service.

---

## 17. Quick Reference Cheat Sheet

### Pattern Summary

| Pattern | When to Use | Risk |
|---------|-------------|------|
| Strangler Fig | Always — default migration approach | 🟢 Low |
| Feature Flags | Every migration touching user-facing behaviour | 🟢 Low |
| Shadow Traffic | Before any cutover — validate parity | 🟢 Very Low |
| Circuit Breaker | All synchronous inter-service calls | 🟢 Must-have |
| Saga (Choreography) | Simple, stable, low-stakes notification flows only | 🟡 Medium |
| Saga (Orchestration) | Any flow involving money, inventory, or irreversible state | 🟢 Preferred for critical flows |
| Temporal / Step Functions | Durable orchestration — never build your own with a cron job | 🟢 Strongly recommended |
| Outbox Pattern | When DB write and event publish must be atomic | 🟡 Medium |
| Shared DB (Phase 1) | While new service is being stabilised | 🟡 Temporary |
| CDC / DB Split + Data Warehouse | After service stable — with analytics pipeline and backfill plan | 🟡 Medium |
| OTel Auto-instrumentation | Every service — do not rely on manual header passing | 🟢 Must-have |
| AZ Co-location + Pod Affinity | Chatty services must share an AZ — enforce with affinity rules | 🟢 Cost-critical |
| Service Mesh (Istio Ambient / Linkerd) | mTLS, locality routing, observability — use Ambient to avoid sidecar tax | 🟢 Must-have at scale |
| SPIFFE/SPIRE | Short-lived cert rotation + bootstrapping — cert lifetime > 24h is a security debt | 🟢 Must-have for security-sensitive services |
| OPA / Policy-as-Code | Centralise all "can User X do Action Y?" logic — never re-implement per service | 🟢 Must-have to prevent IDOR at scale |
| Big Bang Rewrite | Never in production | 🔴 Very High |
| Distributed Monolith | Never — the anti-pattern to avoid | 🔴 Very High |
| Shared Business Logic Library | Never across service boundaries — DRY is a trap in microservices | 🔴 Very High |
| DIY Orchestrator (DB table + cron) | Never — use Temporal or Step Functions | 🔴 Double-charge incident waiting to happen |
| Long-lived mTLS certs (> 90 days) | Never for production security boundaries | 🔴 Permanent back door |
| Hardcoded certs in Docker images | Never — this is not cert management, it is cert abandonment | 🔴 Very High |
| Trusting internal callers as pre-authorised | Never — authentication ≠ authorisation | 🔴 IDOR vulnerability |

### Decision Checklist

- [ ] Can you name the specific pain point this migration solves?
- [ ] Do you have at least 8 engineers and a dedicated platform or DevOps team?
- [ ] Have you mapped all domain boundaries and dependency graphs?
- [ ] Have you chosen a first service with low coupling and clear ownership?
- [ ] Do you have an API Gateway in front of the monolith?
- [ ] Do you have baseline monitoring for the monolith?
- [ ] Do you have a feature flag system?
- [ ] Is your rollback trigger defined and written in the runbook?
- [ ] Do you have consumer-driven contract tests for each service boundary?
- [ ] Is OpenTelemetry **auto-instrumentation** (not just the SDK) deployed to every service?
- [ ] Are traces unbroken across async Kafka/queue boundaries — not just HTTP?
- [ ] Are circuit breakers and timeouts configured for all outbound calls?
- [ ] Is JWT validation running on every service (not just the gateway)?
- [ ] Is there a policy engine (OPA or equivalent) enforcing resource-level authorisation — not just authentication?
- [ ] Does every internal service call carry original user context so downstream services can check resource permissions?
- [ ] Is centralised config management in place (no hardcoded credentials)?
- [ ] Is service discovery configured (no hardcoded IPs)?
- [ ] Are frequently communicating services co-located in the same AZ with pod affinity rules enforced?
- [ ] Have you estimated cross-AZ data transfer costs for your chatty service pairs before deploying?
- [ ] Is certificate bootstrapping automated (SPIRE or mesh CA) — no manually provisioned or image-baked certs?
- [ ] Are mTLS certificates short-lived (< 24h for prod payment/auth services) with automatic rotation?
- [ ] Can you rotate all service certificates right now with zero downtime and zero manual steps?
- [ ] For multi-step flows involving money or inventory: are you using Temporal / Step Functions — **not** a cron job + DB table?
- [ ] Is a data warehouse pipeline planned (with backfill strategy) for analytical queries across service databases?
- [ ] Does your shared library contain **only** infrastructure utilities — zero business logic?

### Observability Minimum Bar

Before any service goes to production:

| Requirement | Tool Options |
|-------------|-------------|
| Distributed tracing | OpenTelemetry + Jaeger / Grafana Tempo / Datadog |
| Centralised logs with correlation ID | ELK Stack / Grafana Loki |
| Metrics + dashboards | Prometheus + Grafana / Datadog |
| Alerting on SLOs | Grafana Alerts / PagerDuty |

### Recommended Reading

- *Building Microservices* — Sam Newman (the definitive reference)
- *Monolith to Microservices* — Sam Newman (migration-specific, highly practical)
- *Designing Distributed Systems* — Brendan Burns (patterns with concrete examples)
- *Release It!* — Michael Nygard (resilience, circuit breakers, production readiness)
- *Strangler Fig Application* — Martin Fowler, martinfowler.com
- *SPIFFE/SPIRE* — spiffe.io (workload identity standard and implementation)
- *Istio Ambient Mesh* — istio.io/docs (sidecar-less mesh, stable since Istio 1.22)

---
