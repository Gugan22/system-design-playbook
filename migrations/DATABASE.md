# Database Migration Strategies

---

## Who Is This Document For?

This guide is written for **everyone who touches a database** — from someone who just joined the team to a senior engineer planning a large-scale migration. No prior migration experience is needed. Complex concepts are explained with plain-language examples before the technical detail.

> **📋 Quick Orientation**
>
> | Your Level | Where to Start |
> |---|---|
> | 🟢 **New to databases?** | Start at Section 1. Read everything. |
> | 🔵 **Junior developer?** | Sections 1–7 give you a solid foundation. |
> | 🟡 **Mid-level engineer?** | Sections 2–14 are your core reference. |
> | 🔴 **Senior / Tech Lead?** | Sections 15–17 (JSONB, ENUM, Sentinels) and Sections 18–25 (systems, runbooks, failure patterns) are where the advanced nuance lives. |

---

## ⚡ Key Points — Read This First

Before diving into any section, internalise these 18 principles. Every pattern in this document flows from them. If you read nothing else, read this table.

> **🔑 The 18 Laws of Safe Migration**
>
> | # | Law | What Happens If You Break It |
> |---|---|---|
> | 1 | **Never lock a large table.** | `ALTER TABLE` on 50M+ rows without `CONCURRENTLY` or Expand/Contract holds an exclusive lock for minutes — every read and write queues up and your service appears frozen. |
> | 2 | **Write the rollback script before the migration runs.** | Mid-incident is the worst time to figure out how to undo a schema change. Engineers writing reversal SQL under pager pressure make mistakes. |
> | 3 | **Test at production row counts, not staging row counts.** | A migration that takes 0.3s on 50K rows can take 11 minutes on 80M rows. Staging gives you false confidence. |
> | 4 | **Every migration must work with both the current (N) and previous (N-1) app version.** | Rolling deploys mean old and new pods run simultaneously. Dropping a column the old version still reads causes an immediate crash in a subset of pods. |
> | 5 | **Idempotency is non-negotiable.** | Migrations fail and restart. A non-idempotent migration that crashes halfway leaves the database in a broken state where the re-run fails with a *different* error. |
> | 6 | **Backfill in batches, never in bulk.** | One `UPDATE` touching 50M rows locks the table, spikes CPU, saturates replication lag, and causes an outage. |
> | 7 | **Run a divergence check before every read cutover.** | A backfill completing without errors does not mean the data is correct. Switching reads to wrong data is silent — no errors, wrong answers served to users. |
> | 8 | **Separate DDL from long-running data operations.** | Combining `ALTER TABLE` and a 10M-row `UPDATE` in one transaction holds the DDL lock for the full duration of the data operation — potentially 45+ minutes. |
> | 9 | **Always use `CONCURRENTLY` for index operations on PostgreSQL.** | `CREATE INDEX` without `CONCURRENTLY` takes a full table lock. On a 100M-row table this blocks all queries for 10–30 minutes. MySQL uses `ALGORITHM=INPLACE, LOCK=NONE` for the same goal. |
> | 10 | **Use `NOT VALID` + `VALIDATE CONSTRAINT` for foreign keys (PostgreSQL).** | A plain `ADD CONSTRAINT FOREIGN KEY` on a large table scans every row with an `AccessExclusiveLock`, blocking all reads and writes for the entire scan duration. |
> | 11 | **Every migration gets a version number and is immutable.** | Editing a migration that already ran in any environment means your history is a lie — you can no longer reproduce or audit the schema state. |
> | 12 | **Monitor replication lag throughout every migration.** | Heavy writes cause replicas to fall behind. Read-replicas silently serve stale data — users see missing orders, wrong balances, vanished records — with no errors in the logs. |
> | 13 | **Set `lock_timeout` before every DDL statement.** | A waiting `ALTER TABLE` queues behind the blocking query and then blocks every subsequent query behind itself — a cascade that can fill your connection pool in 30 seconds. |
> | 14 | **Monitor dead tuples and autovacuum health during any backfill.** | Every `UPDATE` creates a dead tuple. If autovacuum cannot keep up, a "safe" backfill produces index and table bloat that silently kills query performance long after the migration finishes. |
> | 15 | **Track your column count and transaction ID horizon.** | Dropped columns still count toward PostgreSQL's 1,600-column limit until `VACUUM FULL` runs. Aggressive or long-running migrations can accelerate TXID wraparound — a hard limit that shuts the database down entirely. |
> | 16 | **Never modify an ENUM type inside a transaction block.** | `ALTER TYPE ... ADD VALUE` cannot execute inside `BEGIN/COMMIT` in PostgreSQL. If your migration runner wraps all statements in a transaction, the entire deployment will fail with a hard error. Removing or renaming an ENUM value requires replacing the type entirely — which cannot be done online without a full Expand/Contract cycle. |
> | 17 | **Define sentinel values before any JSONB-to-column backfill.** | Using `NULL` as the "not yet migrated" marker fails when business logic already uses `NULL` to mean "intentionally empty." Without a distinct sentinel, your backfill silently overwrites intentional data — and the divergence check cannot tell the difference. |
> | 18 | **Tune `autovacuum_work_mem` for the migration window on backfill-heavy tables.** | The default `autovacuum_work_mem` (-1, inheriting `maintenance_work_mem`) may be insufficient for processing millions of dead tuples quickly. An under-resourced vacuum runs slowly, the TXID horizon closes in, and the database shuts down — regardless of how well-batched the backfill itself was. |

---

## ⚠️ Migration Risk Reference — Know Before You Start

The table below describes failure modes and their documented characteristics. **Percentage-based frequency figures have been removed from this document** — no current, methodology-transparent study provides per-failure-mode rates specifically for schema migrations in live production systems. Claims such as "35% of backfills experience replication lag" are not traceable to any published source and have been excised rather than left as false precision.

What the industry *does* document: the Caylent 2024 survey of 300+ IT leaders found that only 6% of organisations completed their most challenging database migrations on time, and only 6% achieved zero downtime — underscoring that migration risk is systematic, not edge-case. *(Source: Caylent, "The Database Migration Crisis", 2024, https://caylent.com/blog/the-database-migration-crisis-why-94-of-organizations-are-missing-their-deadlines)*

| Migration Failure Cause | Root Cause | Severity When It Occurs | Recovery Difficulty |
|---|---|---|---|
| **Replication Lag / Stale Reads** | Backfill writes outpacing replica replay | Silent — no errors, wrong data served | Low (pause backfill; wait for replica to catch up) |
| **N+1 Rollback Failure** (old app + new schema conflict) | Phase 3 dropped while old pods still running | Crash loop on old-version pods | High (risk of data corruption on reverse) |
| **Connection Pool Saturation** | `lock_timeout` not set; DDL queues and cascades | **Total site outage (0% availability)** | Moderate (kill blocking query; pool drains) |
| **Index / Table Bloat** | Dead tuples from backfill + autovacuum unable to keep pace | Silent query degradation post-migration | Moderate (`VACUUM ANALYZE` + `REINDEX CONCURRENTLY`) |
| **Catalog Bloat** | Dropped columns accumulating without `VACUUM FULL` | Slow query plan degradation over months | Hard (fix requires `VACUUM FULL` with exclusive lock) |
| **WAL Disk Full** | Long-running backfill + active replication slots preventing WAL deletion | DB enters read-only mode or crashes | Severe (immediate disk intervention required) |
| **TXID Wraparound** | Unvacuumed transactions approaching the 2-billion limit | **Database refuses all writes; shutdown** | Extreme (requires downtime; see Section 14) |

> **💡 Connection pool saturation is the fastest path to total site unavailability in this list — a single `ALTER TABLE` waiting on a lock with no `lock_timeout` can cascade to 0% availability in under 60 seconds. TXID wraparound is the only failure mode on this list that causes the database itself to shut down; it warrants continuous monitoring independent of any migration activity.**

---

## Table of Contents

**Foundations**
1. [What Is a Database Migration — and Why Is It Scary?](#1-what-is-a-database-migration--and-why-is-it-scary)
2. [Zero-Downtime Schema Migrations — The Expand/Contract Pattern](#2-zero-downtime-schema-migrations--the-expandcontract-pattern)
3. [Big Bang vs Incremental Migration](#3-big-bang-vs-incremental-migration)

**Core Techniques**
4. [Dual-Write Strategy for Critical Data](#4-dual-write-strategy-for-critical-data)
5. [Backfill Patterns Without Locking Tables](#5-backfill-patterns-without-locking-tables)
6. [Idempotent Migrations](#6-idempotent-migrations)
7. [Rollback Planning](#7-rollback-planning)

**Database-Specific Hazards**
8. [Index Migrations](#8-index-migrations)
9. [Long Transactions](#9-long-transactions)
10. [Foreign Key Validation](#10-foreign-key-validation)
11. [Replication Impact](#11-replication-impact)
12. [Disk Space During Migrations](#12-disk-space-during-migrations)
13. [Lock Diagnostics](#13-lock-diagnostics)
14. [VACUUM, Bloat, and Transaction ID Wraparound](#14-vacuum-bloat-and-transaction-id-wraparound)
15. [JSONB to Structured Column Migrations](#15-jsonb-to-structured-column-migrations)
16. [ENUM Type Migrations](#16-enum-type-migrations)
17. [Sentinel Values in Dual-Write Migrations](#17-sentinel-values-in-dual-write-migrations)

**Systems & Infrastructure**
18. [Versioned Migration System](#18-versioned-migration-system)
19. [Central Migration Registry](#19-central-migration-registry)
20. [Migration Runner](#20-migration-runner)
21. [Infrastructure-Level Migrations](#21-infrastructure-level-migrations)

**Tooling**
22. [Migration Tooling — The Full Landscape](#22-migration-tooling--the-full-landscape)

**Reference**
23. [Migration Runbook Template](#23-migration-runbook-template)
24. [What Goes Wrong — Six Patterns From Production](#24-what-goes-wrong--six-patterns-from-production)
25. [Quick Reference — Pre-Migration Checklist](#25-quick-reference--pre-migration-checklist)

---

## 1. What Is a Database Migration — and Why Is It Scary?

### The Plain English Version

Imagine your database is a warehouse. Your application is the forklift driver working inside it **24 hours a day, 7 days a week**. A migration is when you need to *rearrange the shelves* — while the forklift is still moving.

You cannot shut down the warehouse. Customers are waiting. Orders are coming in. You have to rearrange the shelves carefully, section by section, without tipping anything over.

That is what a database migration is.

> **📘 Real Example: What counts as a migration?**
>
> - Adding a new column (e.g., add a `phone_number` column to the `users` table)
> - Renaming a column (e.g., rename `email` to `email_address`)
> - Splitting one table into two
> - Changing a column's data type (e.g., storing dates as `TIMESTAMP` instead of `VARCHAR`)
> - Adding an index to speed up a slow query
> - Adding a `NOT NULL` constraint or a foreign key to an existing column

### Why Is It Different From Deploying Code?

When you deploy bad application code, you can roll it back in seconds. The code reverts to the previous version. Nothing is permanently changed.

Database migrations are different. Once you rename a column or move data, that change is **permanent** unless you have a prepared reversal script ready. Writing that reversal mid-incident, while your pager is going off, is exactly when engineers make mistakes.

> **📘 Real Example: What an explicit reversal looks like**
>
> You rename a column from `email` to `email_address`:
>
> ```sql
> -- Forward migration: migrations/V2__rename_email_column.sql
> ALTER TABLE users RENAME COLUMN email TO email_address;
> ```
>
> The moment this runs, any code still executing `SELECT email FROM users` crashes with `column "email" does not exist`. If you prepared the reversal *before* running the forward migration:
>
> ```sql
> -- Rollback script — written and reviewed BEFORE V2 ran
> -- Stored at: migrations/rollback/V2__rollback.sql
> ALTER TABLE users RENAME COLUMN email_address TO email;
> ```
>
> | Scenario | Resolution time |
> |---|---|
> | Code rollback via `kubectl rollout undo` | ~30 seconds |
> | Database rollback with a prepared script | ~1–2 minutes |
> | Database rollback with no prepared script (writing SQL under pressure at 2am) | Unknown — and mistakes get made |

> **The Core Difference**
>
> | | Code Deployment | Database Migration |
> |---|---|---|
> | **Rollback speed** | Seconds — revert the binary | Requires a prepared reversal script |
> | **When things go wrong** | Revert the binary | What data exists in the new format? Can old code still read it? |
> | **Staging vs production** | Staging ≈ production | **Staging ≠ production** — row count matters enormously |
> | **Rollback safety** | Always safe | May cause data loss if new-format data has already been written |

> **💡 Tip:** Most production migration outages are not caused by incorrect SQL. They are caused by correct SQL that was never tested at production scale — because staging had 5,000 rows and production had 50 million.

---

## 2. Zero-Downtime Schema Migrations — The Expand/Contract Pattern

### What Is a Table Lock?

Before explaining the pattern, you need to understand what a table lock is — because the entire point of Expand/Contract is to avoid holding one for any significant duration.

A **table lock** prevents two operations from conflicting with each other. When you run certain DDL commands (`ALTER TABLE`, `CREATE INDEX`), the database acquires an exclusive lock on the entire table. While that lock is held, **every other query that tries to read from or write to that table queues up and waits**.

On a small table (10,000 rows), the lock is held for a fraction of a second — imperceptible. On a large table (100 million rows), the same operation may hold the lock for **minutes**. Every request your application sends to that table during those minutes is blocked. Connection pools fill up. Timeouts cascade. Users see errors.

> **📘 Real Example: One ALTER TABLE, one outage**
>
> A team adds a `verified_at` timestamp column to the `users` table:
>
> ```sql
> -- ❌ Dangerous on tables with millions of rows
> ALTER TABLE users ADD COLUMN verified_at TIMESTAMP NOT NULL DEFAULT NOW();
> ```
>
> On 100 million rows this rewrites every row with the new default, holding an exclusive lock the entire time — potentially many minutes. Every login attempt fails. Every profile update fails. Connection pool fills. Monitoring fires everywhere.
>
> The fix is not faster SQL. The fix is to **never do this in one shot on a large live table**.

### The "Ghost Column" Limit

Before adopting Expand/Contract as a long-term practice, teams must understand one structural limit in PostgreSQL: **a table can have at most 1,600 columns**. The critical catch is that **dropped columns still count toward this limit** until a `VACUUM FULL` is run — which requires an `AccessExclusiveLock` and is effectively a full-table rewrite.

> **⚠️ The Risk for Expand/Contract Teams**
>
> If your team applies the Contract phase 50+ times to the same table, you may eventually find you cannot add any new columns — even though only a fraction of those 1,600 slots are visibly occupied. The only fix is `VACUUM FULL`, which blocks all reads and writes for the entire duration of the table rewrite (potentially hours on a large table).
>
> ```sql
> -- Check how many columns (including dropped) a table is consuming
> SELECT COUNT(*) AS total_column_slots
> FROM pg_attribute
> WHERE attrelid = 'users'::regclass
>   AND attnum > 0;         -- attnum > 0 filters out system columns
> -- attisdropped = TRUE columns count toward the 1,600 limit
>
> -- See which slots are dropped
> SELECT attname, attisdropped
> FROM pg_attribute
> WHERE attrelid = 'users'::regclass
>   AND attnum > 0
> ORDER BY attnum;
> ```
>
> **Rule of thumb:** If your column slot count approaches 1,200 (giving a buffer of 400), schedule a `VACUUM FULL` during a planned maintenance window before it becomes urgent.

### The Expand/Contract Pattern

Expand/Contract breaks one dangerous migration into **three small, safe steps**. Each step is independently deployable. Each holds a lock for at most a millisecond.

---

#### 🟢 Phase 1: EXPAND — Add the New Structure Without Breaking Anything

Add the new column as **nullable** (`NULL` is allowed). This is safe because:

- PostgreSQL 11+ stores the nullable default in the catalog — it does **not** rewrite existing rows
- Old application code that doesn't know about the new column still works — it ignores it
- The lock is held for a millisecond, not minutes

```sql
-- ✅ SAFE on any size table — no row rewrite, no extended lock
ALTER TABLE users ADD COLUMN verified_at TIMESTAMP NULL;
```

At the same time, update the application to **write to both the old and new columns** on every relevant operation (the Dual-Write pattern — covered fully in [Section 4](#4-dual-write-strategy-for-critical-data)).

> **💡 Tip:** Deploy Phase 1 and let it run for at least 24 hours before moving to Phase 2. You want new writes to have been populating the new column long enough to validate the behaviour before starting the backfill.

---

#### 🟡 Phase 2: MIGRATE — Backfill Existing Rows

Existing rows still have `NULL` in the new column. Run a **background job** to populate them without downtime. See [Section 5](#5-backfill-patterns-without-locking-tables) for the full safe batched backfill pattern and [Section 14](#14-vacuum-bloat-and-transaction-id-wraparound) for how to monitor dead-tuple bloat throughout.

Once the backfill completes, run a **divergence check** to verify the data is correct, then flip the application to read from the new column.

> **⚠️ Watch Out:** Do not flip reads until the divergence check passes. A backfill that ran without errors is not the same as a backfill that produced correct data — especially if rows were updated during a multi-day backfill run.

---

#### 🔴 Phase 3: CONTRACT — Remove the Old Structure

Only once the application has been reading exclusively from the new column for several days with no issues, remove the old one:

```sql
-- ✅ SAFE — nothing reads is_verified anymore
ALTER TABLE users DROP COLUMN is_verified;

-- Optionally enforce NOT NULL now that all rows are populated
ALTER TABLE users ALTER COLUMN verified_at SET NOT NULL;
-- PostgreSQL 11+: this is a catalog-only change — no row rewrite, no long lock
```

> **⚠️ Remember:** The dropped `is_verified` column still occupies a column slot. Track cumulative dropped columns on frequently-migrated tables (see the Ghost Column Limit above).

---

### The Full Picture

| Phase | What Application Writes To | What Application Reads From | When To Move On |
|---|---|---|---|
| **1 — Expand** | OLD + NEW columns | OLD column only | After ≥ 24h of clean writes to new column |
| **2 — Migrate** | OLD + NEW columns | NEW column only | After backfill complete + divergence check passes |
| **3 — Contract** | NEW column only | NEW column only | Old column dropped; migration complete |

Each phase is independently rollback-safe. You can pause between any two phases. Phase 1 can be rolled back with a simple `DROP COLUMN` — no data has been moved yet.

---

## 3. Big Bang vs Incremental Migration

A **Big Bang migration** does everything at once during a scheduled maintenance window when the system is taken offline.

An **Incremental migration** spreads the change across multiple deployments over days or weeks with the system live throughout.

> **📘 Real Example: Splitting `full_name` into `first_name` and `last_name`**
>
> **Big Bang approach:** Schedule a 2am Saturday maintenance window. Take the site offline. Run the migration. Restart. If anything goes wrong, restore from backup and lose hours of data.
>
> **Incremental approach:** Week 1 — add `first_name` and `last_name` as nullable columns. Week 2 — deploy code that writes to all three. Weeks 3–4 — run batched backfill. Week 5 — cut reads after divergence check. Week 6 — drop `full_name`. Site never went offline.

### When To Use Each

| Use Big Bang When... | Use Incremental When... |
|---|---|
| Internal tool — users can tolerate 30 min downtime | Customer-facing service with an uptime SLA |
| Table has fewer than ~100K rows | **Table has over 1 million rows** |
| Batch pipeline — not customer-facing | Financial, identity, or session data |
| Greenfield — no production data yet | Multi-region or multi-service deployment |

> **⚠️ Watch Out:** Teams choose Big Bang because it is simpler to reason about — one script, one window, one outcome. The risk is compressing every possible failure into a single moment at 2am with maximum blast radius. If anything goes wrong, rollback is a 4-hour point-in-time restore.

**The hidden cost of incremental:** Teams that rush leave migrations in **"Phase 2 limbo"** — the backfill ran but the contract phase never shipped, leaving two columns with diverging data coexisting in the schema indefinitely.

**Preventing Phase 2 limbo — three concrete controls:**
1. **Track migration phase in your Central Migration Registry** (Section 19). A migration is not "done" until the registry shows Phase 3 complete. Any migration sitting in Phase 2 for more than two sprints is a blocker.
2. **Make dual-write cost visible.** Every extra write path is another failure point and an ongoing CPU cost. Add a "migrations in flight" count to your team's engineering dashboard — friction reduces lingering.
3. **Set a calendar reminder** at Phase 2 cutover for "drop old column" 30 days out. The window gives time for confidence. The calendar invite ensures it isn't quietly forgotten.

---

## 4. Dual-Write Strategy for Critical Data

### What Is Dual-Write?

During a migration, there is a period when your old schema and your new schema must both stay in sync. **Dual-write** keeps them aligned: every write goes to both the old location and the new location simultaneously.

Think of it like forwarding your mail when you move house. During the transition, you tell the post office to deliver to both addresses so nothing gets lost while you are still moving boxes.

> **📘 Real Example: An e-commerce order service migrating how it stores delivery addresses**
>
> **Old design:** A single `delivery_address` text column.
>
> **New design:** Three structured columns — `delivery_street`, `delivery_city`, `delivery_country`.
>
> ```python
> def place_order(order):
>     db.execute("""
>         INSERT INTO orders (
>             delivery_address,
>             delivery_street, delivery_city, delivery_country
>         ) VALUES (%s, %s, %s, %s)
>     """, (order.full_address, order.street, order.city, order.country))
> ```

### Controlling Dual-Write With Feature Flags

Feature flags let you control dual-write without a code deployment — ramp from 5% → 50% → 100% of writes, or kill it instantly if something looks wrong.

```sql
CREATE TABLE feature_flags (
    flag_name   VARCHAR(100) PRIMARY KEY,
    is_enabled  BOOLEAN      DEFAULT FALSE,
    description TEXT,
    updated_at  TIMESTAMP    DEFAULT NOW()
);

INSERT INTO feature_flags (flag_name, is_enabled, description) VALUES
    ('dual_write_order_address_v2', FALSE, 'Write to new delivery address columns'),
    ('read_from_order_address_v2',  FALSE, 'Read from new delivery address columns');
```

```python
class OrderRepository:
    def save_order(self, order):
        self._write_legacy_address(order)
        if is_feature_enabled("dual_write_order_address_v2"):
            self._write_new_address_columns(order)

    def get_order(self, order_id):
        if is_feature_enabled("read_from_order_address_v2"):
            return self._read_new_address_columns(order_id)
        return self._read_legacy_address(order_id)
```

### The Four States of a Dual-Write Migration

Move through these states strictly in order. **Never skip ahead.**

| State | Writes To | Reads From | When You Are Here |
|---|---|---|---|
| **1** | OLD only | OLD only | Before migration starts — normal operation |
| **2** | OLD + NEW | OLD only | After Expand phase — new columns being populated |
| **3** | OLD + NEW | NEW only | After backfill verified — new columns are source of truth |
| **4** | NEW only | NEW only | After Contract phase — migration complete |

### Consistency During Dual-Write — What Can Actually Go Wrong

> **📘 Scenario 1: Bank balance — wrap both writes in one transaction**
>
> ```python
> with db.transaction():
>     db.execute(
>         "UPDATE accounts SET balance = balance - %s WHERE id = %s",
>         (amount, account_id)
>     )
>     db.execute(
>         "INSERT INTO account_ledger (account_id, delta, balance_after) VALUES (%s, %s, %s)",
>         (account_id, -amount, new_balance)
>     )
> ```

> **📘 Scenario 2: Session migration — fall back to old location on a miss**
>
> ```python
> def get_session(session_id: str):
>     session = redis.get(session_id)
>     if session is None:
>         session = db.query_session(session_id)
>     return session
> ```

> **📘 Scenario 3: Cache serving stale schema — invalidate on every write**
>
> ```python
> def update_price(product_id: int, new_price: float):
>     db.execute("UPDATE products SET product_price = %s WHERE id = %s", (new_price, product_id))
>     db.execute(
>         "INSERT INTO pricing (product_id, price) VALUES (%s, %s) "
>         "ON CONFLICT (product_id) DO UPDATE SET price = EXCLUDED.price",
>         (product_id, new_price)
>     )
>     cache.delete(f"product:{product_id}")
> ```

### What To Monitor During Dual-Write

| Metric | Why It Matters | Alert Threshold |
|---|---|---|
| Write success rate (new location) | A drop means new schema is rejecting writes | < 99.9% |
| Divergence rate (old vs new disagree) | Any divergence post-cutover is data corruption | > 0% **after** dual-write is fully settled; transient divergence during the write-cutover window itself is expected — do not alert on this window, only on steady-state |
| Read fallback rate | How often new location misses and old is used | > 1% |
| Cache hit rate | Sudden drop may indicate broken cache invalidation | Sudden change |
| Replication lag | Lag in new target creates inconsistency windows | > 500ms |

---

## 5. Backfill Patterns Without Locking Tables

A backfill populates data in the new column or table for rows that **existed before the migration started**. New writes are handled by dual-write. Old rows need separate treatment.

> **🚨 Never Do This on a Large Live Table:**
> ```sql
> UPDATE users SET verified_at = created_at WHERE verified_at IS NULL;
> ```
> On 50M rows this locks the table, spikes CPU, saturates replication lag, and causes an outage. It also creates 50M dead tuples — see [Section 14](#14-vacuum-bloat-and-transaction-id-wraparound) for why that matters.

### The Safe Pattern: Adaptive Backfill with Live Health Checks

A fixed sleep between batches is a blunt instrument. It does not respond to what the database is actually experiencing — a healthy system is throttled unnecessarily, a struggling system is not throttled enough. The production-grade pattern measures four health signals before every batch and adjusts pace dynamically.

The four signals checked before each batch:

- **Replication lag** — if replicas are falling behind, reads served from them are stale
- **WAL disk pressure** — if retained WAL is growing, the primary may be approaching read-only mode
- **Dead-tuple accumulation** — if autovacuum cannot keep pace, index and table bloat is building silently
- **Active lock contention** — if other sessions are waiting on locks against the target table, a batch now will extend those waits

```python
import time
import dataclasses
from typing import Optional

@dataclasses.dataclass
class BackfillHealth:
    """Snapshot of database health metrics relevant to backfill safety."""
    replication_lag_s:  float   # seconds; 0.0 if no replicas
    wal_retained_gb:    float   # GB of WAL retained by slowest replication slot
    dead_tup_pct:       float   # dead tuples as % of live + dead on target table
    lock_waiters:       int     # sessions currently waiting on a lock on the target table

class AdaptiveBackfill:
    """
    Runs a batched backfill with per-batch health checks and dynamic throttling.

    Throttle levels (evaluated in priority order):
      PAUSE   — health critical; stop and wait until resolved
      SLOW    — health degraded; reduce batch size and increase sleep
      NORMAL  — health acceptable; run at configured pace
      FAST    — all signals green; can push slightly harder if desired

    Usage:
        backfill = AdaptiveBackfill(db, table="users", id_column="id",
                                    update_sql=UPDATE_SQL, progress_key="backfill_verified_at")
        backfill.run()
    """

    # ── Thresholds ────────────────────────────────────────────────────────────
    PAUSE_REPLICATION_LAG_S  = 30.0   # pause if any replica is >30s behind
    SLOW_REPLICATION_LAG_S   =  5.0   # slow down if any replica is >5s behind
    PAUSE_WAL_RETAINED_GB    = 20.0   # pause if slowest slot retains >20 GB of WAL
    SLOW_WAL_RETAINED_GB     =  5.0   # slow down if >5 GB retained
    PAUSE_DEAD_TUP_PCT       = 40.0   # pause if >40% of rows are dead tuples
    SLOW_DEAD_TUP_PCT        = 20.0   # slow down if >20% dead tuples
    PAUSE_LOCK_WAITERS       =  5     # pause if 5+ sessions waiting on target table locks

    # ── Batch parameters per throttle level ──────────────────────────────────
    BATCH_PARAMS = {
        "FAST":   {"size": 2_000, "sleep_s": 0.01},
        "NORMAL": {"size": 1_000, "sleep_s": 0.05},
        "SLOW":   {"size":   250, "sleep_s": 0.50},
        "PAUSE":  {"size":     0, "sleep_s": 15.0},   # size=0 skips the batch
    }

    def __init__(self, db, table: str, id_column: str, update_sql: str,
                 progress_key: str, start_id: int = 0):
        self.db           = db
        self.table        = table
        self.id_col       = id_column
        self.update_sql   = update_sql   # must accept %(ids)s and include idempotency guard
        self.progress_key = progress_key
        self.last_id      = start_id
        self.total_done   = 0
        self._ensure_progress_table()

    # ── Health sampling ───────────────────────────────────────────────────────

    def _sample_health(self) -> BackfillHealth:
        lag_row = self.db.execute("""
            SELECT COALESCE(
                EXTRACT(EPOCH FROM MAX(replay_lag)), 0
            ) AS lag_s
            FROM pg_stat_replication
        """).fetchone()

        wal_row = self.db.execute("""
            SELECT COALESCE(
                MAX(
                    pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn)
                ) / 1073741824.0,   -- bytes -> GB
                0.0
            ) AS retained_gb
            FROM pg_replication_slots
            WHERE active = TRUE
        """).fetchone()

        bloat_row = self.db.execute("""
            SELECT
                CASE
                    WHEN n_live_tup + n_dead_tup = 0 THEN 0.0
                    ELSE ROUND(
                        n_dead_tup::DECIMAL / (n_live_tup + n_dead_tup) * 100, 1
                    )
                END AS dead_pct
            FROM pg_stat_user_tables
            WHERE relname = %(table)s
        """, {"table": self.table}).fetchone()

        lock_row = self.db.execute("""
            SELECT COUNT(*) AS waiters
            FROM pg_locks l
            JOIN pg_stat_activity a USING (pid)
            WHERE l.relation = %(table)s::regclass
              AND l.granted  = FALSE
        """, {"table": self.table}).fetchone()

        return BackfillHealth(
            replication_lag_s = float(lag_row["lag_s"]          or 0),
            wal_retained_gb   = float(wal_row["retained_gb"]     or 0),
            dead_tup_pct      = float(bloat_row["dead_pct"]      or 0) if bloat_row else 0.0,
            lock_waiters      = int(lock_row["waiters"]           or 0),
        )

    def _throttle_level(self, h: BackfillHealth) -> str:
        if (h.replication_lag_s >= self.PAUSE_REPLICATION_LAG_S or
                h.wal_retained_gb   >= self.PAUSE_WAL_RETAINED_GB   or
                h.dead_tup_pct      >= self.PAUSE_DEAD_TUP_PCT       or
                h.lock_waiters      >= self.PAUSE_LOCK_WAITERS):
            return "PAUSE"
        if (h.replication_lag_s >= self.SLOW_REPLICATION_LAG_S or
                h.wal_retained_gb   >= self.SLOW_WAL_RETAINED_GB    or
                h.dead_tup_pct      >= self.SLOW_DEAD_TUP_PCT):
            return "SLOW"
        # All signals green
        return "NORMAL"

    def _log_health(self, level: str, h: BackfillHealth) -> None:
        print(
            f"  [{level:6s}] lag={h.replication_lag_s:.1f}s  "
            f"wal={h.wal_retained_gb:.2f}GB  "
            f"dead={h.dead_tup_pct:.1f}%  "
            f"lock_waiters={h.lock_waiters}  "
            f"done={self.total_done:,}  last_id={self.last_id}"
        )

    # ── Progress persistence ──────────────────────────────────────────────────

    def _ensure_progress_table(self) -> None:
        self.db.execute("""
            CREATE TABLE IF NOT EXISTS backfill_jobs (
                job_name          VARCHAR(100) PRIMARY KEY,
                last_processed_id BIGINT       DEFAULT 0,
                total_processed   BIGINT       DEFAULT 0,
                status            VARCHAR(20)  DEFAULT 'running',
                started_at        TIMESTAMP    DEFAULT NOW(),
                updated_at        TIMESTAMP    DEFAULT NOW()
            )
        """)
        # Resume from last checkpoint if this job was interrupted
        row = self.db.execute(
            "SELECT last_processed_id, total_processed FROM backfill_jobs WHERE job_name = %s",
            (self.progress_key,)
        ).fetchone()
        if row:
            self.last_id    = row["last_processed_id"]
            self.total_done = row["total_processed"]
            print(f"Resuming from id={self.last_id} ({self.total_done:,} rows already done)")

    def _save_progress(self) -> None:
        self.db.execute("""
            INSERT INTO backfill_jobs (job_name, last_processed_id, total_processed)
            VALUES (%(key)s, %(last_id)s, %(done)s)
            ON CONFLICT (job_name) DO UPDATE
            SET last_processed_id = EXCLUDED.last_processed_id,
                total_processed   = EXCLUDED.total_processed,
                updated_at        = NOW()
        """, {"key": self.progress_key, "last_id": self.last_id, "done": self.total_done})

    # ── Main loop ─────────────────────────────────────────────────────────────

    def run(self) -> None:
        print(f"Starting adaptive backfill on '{self.table}' from id={self.last_id}")
        consecutive_pauses = 0

        while True:
            health = self._sample_health()
            level  = self._throttle_level(health)
            params = self.BATCH_PARAMS[level]
            self._log_health(level, health)

            if level == "PAUSE":
                consecutive_pauses += 1
                if consecutive_pauses >= 10:
                    # 10 consecutive pauses (2.5 minutes) = something is structurally wrong
                    self._save_progress()
                    raise RuntimeError(
                        f"Backfill suspended after {consecutive_pauses} consecutive PAUSE "
                        f"cycles. Last health: {health}. Investigate before resuming."
                    )
                # ⚠️ Kubernetes note: a RuntimeError causes the pod to exit non-zero.
                # If running as a Kubernetes Job, set backoffLimit: 0 in the Job spec
                # to prevent automatic restart loops. Alert on Job failure instead.
                # The checkpoint in backfill_jobs ensures a manual re-run is safe.
                time.sleep(params["sleep_s"])
                continue

            consecutive_pauses = 0

            # Fetch next batch of IDs
            rows = self.db.execute(f"""
                SELECT {self.id_col} AS id FROM {self.table}
                WHERE {self.id_col} > %(last_id)s
                ORDER BY {self.id_col} ASC
                LIMIT %(batch_size)s
            """, {"last_id": self.last_id, "batch_size": params["size"]}).fetchall()
            # Idempotency filtering belongs in self.update_sql (e.g. WHERE verified_at IS NULL),
            # not here. The ID range fetch is intentionally broad; the UPDATE is the guard.

            if not rows:
                self.db.execute(
                    "UPDATE backfill_jobs SET status = 'complete', updated_at = NOW() "
                    "WHERE job_name = %s", (self.progress_key,)
                )
                print(f"Backfill complete — {self.total_done:,} rows updated.")
                return

            ids    = [r["id"] for r in rows]
            result = self.db.execute(self.update_sql, {"ids": ids})

            self.total_done += result.rowcount
            self.last_id     = ids[-1]
            self._save_progress()
            time.sleep(params["sleep_s"])


# ── Usage ─────────────────────────────────────────────────────────────────────

UPDATE_SQL = """
    UPDATE users
    SET verified_at = created_at
    WHERE id = ANY(%(ids)s)
      AND verified_at IS NULL    -- idempotency guard: skip already-processed rows
"""

# db must be a connection object whose .execute() returns a result with .rowcount
backfill = AdaptiveBackfill(
    db           = db,
    table        = "users",
    id_column    = "id",
    update_sql   = UPDATE_SQL,
    progress_key = "backfill_verified_at",
)
backfill.run()
```

> **📘 Why keyset pagination (`id > last_id`) instead of `OFFSET`?**
> `LIMIT 1000 OFFSET 5000` shifts as rows are updated during the backfill, causing rows to be skipped or re-processed. `id > last_id` always resumes from exactly the last processed row regardless of concurrent updates, and is significantly faster via the primary key index.

### What Each Health Signal Protects Against

| Signal | Threshold → SLOW | Threshold → PAUSE | What Goes Wrong If Ignored |
|---|---|---|---|
| **Replication lag** | > 5s | > 30s | Read-replicas serve stale data — wrong balances, missing records, no errors in logs |
| **WAL retained (GB)** | > 5GB | > 20GB | Disk fills on primary; database enters read-only mode or crashes |
| **Dead tuple %** | > 20% | > 40% | Index and table bloat silently degrades query performance long after migration ends |
| **Lock waiters** | — | ≥ 5 sessions | Running a batch now extends lock waits for all queued sessions — connection pool cascade |

### Tuning the Backfill

The defaults above are conservative starting points. Increase batch size only after confirming all four signals remain healthy for at least 15 minutes at the current setting.

| Setting | Conservative (Start Here) | Moderate | Aggressive |
|---|---|---|---|
| Batch size | 500 | 1,000–5,000 | 10,000+ |
| Sleep between batches | 100ms | 25–50ms | 0–10ms |
| Run during | Off-peak hours | Any time | Any time |
| Replication lag tolerance | < 1s | < 5s | < 30s |

> **⚠️ Watch Out:** Each batch creates dead tuples. At aggressive settings, autovacuum may not keep up. Monitor `n_dead_tup` during the backfill — see [Section 14](#14-vacuum-bloat-and-transaction-id-wraparound).

### Tracking Backfill Progress

```sql
CREATE TABLE backfill_jobs (
    job_name          VARCHAR(100) PRIMARY KEY,
    started_at        TIMESTAMP,
    last_processed_id BIGINT  DEFAULT 0,
    total_processed   BIGINT  DEFAULT 0,
    status            VARCHAR(20) DEFAULT 'running',
    updated_at        TIMESTAMP   DEFAULT NOW()
);

SELECT
    total_processed,
    (SELECT COUNT(*) FROM users WHERE verified_at IS NULL) AS remaining,
    ROUND(
        total_processed::DECIMAL /
        NULLIF(total_processed + (SELECT COUNT(*) FROM users WHERE verified_at IS NULL), 0)
        * 100, 1
    ) AS pct_done
FROM backfill_jobs
WHERE job_name = 'backfill_verified_at';
```

---

## 6. Idempotent Migrations

### What Does Idempotent Mean?

An idempotent migration produces **the same result whether it runs once or ten times**. Migrations fail in production — network timeouts, deployment restarts mid-run. A non-idempotent migration that crashes halfway can leave the database in a state where the re-run fails with a *different* error — turning a recoverable failure into a crisis.

**Table creation:**
```sql
CREATE TABLE IF NOT EXISTS subscriptions (...);
```

**Column addition (PostgreSQL):**
```sql
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'users' AND column_name = 'verified_at'
    ) THEN
        ALTER TABLE users ADD COLUMN verified_at TIMESTAMP NULL;
    END IF;
END $$;
```

**Index creation:**
```sql
-- PostgreSQL
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email ON users(email);

-- MySQL
ALTER TABLE users
ADD INDEX IF NOT EXISTS idx_users_email (email)
ALGORITHM=INPLACE, LOCK=NONE;
```

**Data migration / backfill:**
```sql
-- ✅ Skips already-processed rows
UPDATE users SET verified_at = created_at WHERE verified_at IS NULL;
```

**Constraint addition (PostgreSQL):**
```sql
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_name = 'fk_user' AND table_name = 'orders'
    ) THEN
        ALTER TABLE orders ADD CONSTRAINT fk_user
        FOREIGN KEY (user_id) REFERENCES users(id) NOT VALID;
    END IF;
END $$;
ALTER TABLE orders VALIDATE CONSTRAINT fk_user;
```

### Why You Cannot Rely Solely on the Migration Runner

Migration runners track applied migrations in a history table, but this breaks down when a migration partially ran and the runner crashed (not recorded, so it re-runs from the start), or when two nodes race to apply the same migration before advisory locking fires. Write every migration as if the runner might execute it twice.

---

## 7. Rollback Planning

> **🚨 The Most Important Rule in This Document**
>
> If you cannot describe the rollback procedure **before** starting a migration, **you are not ready to run the migration.**

### The Three Levels of Rollback

#### ✅ Level 1: Application Rollback — Always Try This First

```bash
kubectl rollout undo deployment/api-server
kubectl rollout status deployment/api-server
```

**Target time: under 2 minutes.** Safe during Phase 1 and Phase 2 because the new column is nullable — old code ignores it.

#### ⚠️ Level 2: Schema Rollback — Use With Caution

```sql
-- Safe for Phase 1 only — column is empty, nothing reads it yet
ALTER TABLE users DROP COLUMN verified_at;
```

> **⚠️ Watch Out:** Before running a schema rollback, confirm exactly which application version is deployed and whether any pod reads the new column. Also confirm the column is truly empty — a column added with `NOT NULL DEFAULT` was populated on creation, and dropping it loses that data. The Expand/Contract pattern uses `ADD COLUMN ... NULL` precisely because this rollback is always safe.

#### 🚨 Level 3: Point-In-Time Restore — Last Resort Only

Restore from a snapshot. **You lose every row written since the snapshot.** Use only when Levels 1 and 2 are not viable.

---

## 8. Index Migrations

### PostgreSQL: Use `CONCURRENTLY`

```sql
-- ❌ DANGEROUS — acquires AccessExclusiveLock, blocks all reads and writes
CREATE INDEX idx_orders_user_id ON orders(user_id);

-- ✅ SAFE — builds index in background, reads and writes continue
CREATE INDEX CONCURRENTLY idx_orders_user_id ON orders(user_id);
```

**Three important constraints of `CONCURRENTLY`:** cannot run inside a transaction block; leaves an `INVALID` index if it fails mid-build; only one concurrent build per table at a time.

```sql
-- Detect INVALID indexes left by a failed concurrent build
SELECT i.indexrelid::regclass AS index_name, i.indrelid::regclass AS table_name
FROM pg_index i
WHERE i.indisvalid = FALSE;

DROP INDEX CONCURRENTLY idx_orders_user_id;
CREATE INDEX CONCURRENTLY idx_orders_user_id ON orders(user_id);
```

> **💡 After a heavy backfill, run `REINDEX CONCURRENTLY` to reclaim index bloat caused by dead tuples created during the `UPDATE` batches.**
> ```sql
> REINDEX INDEX CONCURRENTLY idx_orders_user_id;
> ```

### MySQL: Use `ALGORITHM=INPLACE, LOCK=NONE`

```sql
ALTER TABLE orders
ADD INDEX idx_orders_user_id (user_id)
ALGORITHM=INPLACE, LOCK=NONE;
```

### Composite Index Column Order

The leftmost column must appear in the `WHERE` clause for the index to be used:

```sql
CREATE INDEX CONCURRENTLY idx_orders_user_created ON orders(user_id, created_at);
-- Helps: WHERE user_id = X AND created_at > Y
-- Helps: WHERE user_id = X
-- Does NOT help: WHERE created_at > Y
```

### Partial Indexes (PostgreSQL)

```sql
CREATE INDEX CONCURRENTLY idx_orders_active_user
ON orders(user_id)
WHERE status = 'active';
```

---

## 9. Long Transactions

### Why Long Transactions Are Dangerous

A long transaction holds all locks for its full duration, prevents VACUUM from reclaiming dead rows (causing bloat), forces WAL retention that can fill disk, and blocks logical replication slot advancement.

> **📘 DDL + backfill in one transaction — 45-minute freeze**
>
> ```sql
> -- ❌ DDL lock held for the entire 45-minute UPDATE
> BEGIN;
> ALTER TABLE orders ADD COLUMN shipped_at TIMESTAMP NULL;
> UPDATE orders SET shipped_at = created_at WHERE status = 'shipped';
> COMMIT;
>
> -- ✅ Separate DDL from data operations
> ALTER TABLE orders ADD COLUMN shipped_at TIMESTAMP NULL;
> -- Then backfill in batches (see Section 5)
> ```

### Detecting Long Transactions

```sql
-- PostgreSQL: transactions open for more than 5 minutes
SELECT pid, now() - xact_start AS transaction_age, state, LEFT(query, 120) AS query_preview
FROM pg_stat_activity
WHERE xact_start IS NOT NULL AND (now() - xact_start) > INTERVAL '5 minutes'
ORDER BY transaction_age DESC;
```

### Setting Timeouts

```sql
-- For application queries (prevent runaway SELECT / UPDATE in app code):
SET statement_timeout = '30s';
SET idle_in_transaction_session_timeout = '10min';

-- For migration runner connections (long backfills legitimately take hours):
-- SET statement_timeout = '2h';   -- set on the migration runner session only
-- SET lock_timeout = '5s';        -- always set regardless of session type
```
> **⚠️ Do not apply `statement_timeout = '30s'` to a migration runner session** — a batched backfill will be killed mid-run. Use a higher value (e.g. `'2h'`) on the runner connection and `'30s'` only on application query connections.

---

## 10. Foreign Key Validation

### The Hidden Cost — Including Parent Table Impact

Adding a foreign key normally causes the database to validate every existing row, scanning the child table with an `AccessExclusiveLock` on both tables for the entire scan duration.

> **⚠️ Additional Risk: Parent Table Contention**
>
> Foreign key validation does not only lock the child table — it requires a **shared lock on the parent table** for every row check during `VALIDATE CONSTRAINT`. If the parent is a high-traffic table like `users` or `orders`, the validation scan can cause sluggishness or lock contention on the parent, even when using `NOT VALID` + `VALIDATE CONSTRAINT`. Schedule `VALIDATE CONSTRAINT` during the lowest-traffic window possible for tables with high-volume parents.

### PostgreSQL: `NOT VALID` + `VALIDATE CONSTRAINT`

```sql
-- Step 1: Declare constraint without scanning existing rows (fast)
ALTER TABLE comments
ADD CONSTRAINT fk_comment_user
FOREIGN KEY (user_id) REFERENCES users(id)
NOT VALID;

-- Step 2: Validate existing rows — run during low-traffic window
-- Uses ShareUpdateExclusiveLock — allows concurrent reads and DML
ALTER TABLE comments VALIDATE CONSTRAINT fk_comment_user;
```

| Approach | Lock Acquired | Blocks |
|---|---|---|
| `ADD CONSTRAINT FK` (normal) | `AccessExclusiveLock` on both tables | ALL reads and writes for full scan duration |
| `ADD CONSTRAINT ... NOT VALID` | `ShareRowExclusiveLock` | DDL only |
| `VALIDATE CONSTRAINT` | `ShareUpdateExclusiveLock` | Concurrent DDL only — but still holds shared lock on parent |

### MySQL

```sql
ALTER TABLE comments
ADD CONSTRAINT fk_comment_user FOREIGN KEY (user_id) REFERENCES users(id),
ALGORITHM=INPLACE, LOCK=NONE;
```

---

## 11. Replication Impact

Most production databases replicate writes to one or more replicas. Migrations that generate heavy writes create **replication lag** — replicas fall behind and serve stale data invisibly (no errors, wrong answers).

### Measuring Replication Lag

```sql
-- PostgreSQL primary: check lag for all connected replicas
SELECT client_addr, write_lag, flush_lag, replay_lag,
       pg_size_pretty(pg_wal_lsn_diff(sent_lsn, replay_lsn)) AS bytes_behind
FROM pg_stat_replication ORDER BY replay_lag DESC NULLS LAST;

-- PostgreSQL replica: check own current lag
SELECT now() - pg_last_xact_replay_timestamp() AS replication_lag;
```

### Replication-Aware Backfill

```python
def run_backfill_with_lag_check(db, max_lag_seconds: int = 5):
    BATCH_SIZE = 1_000
    last_id    = 0
    while True:
        row = db.execute("""
            SELECT EXTRACT(EPOCH FROM MAX(replay_lag)) AS lag_sec
            FROM pg_stat_replication
        """).fetchone()
        lag = float(row["lag_sec"] or 0)
        if lag > max_lag_seconds:
            print(f"Replica lag {lag:.1f}s — pausing 10s...")
            time.sleep(10)
            continue
        rows = db.execute("""
            SELECT id FROM users WHERE verified_at IS NULL AND id > %(last_id)s
            ORDER BY id ASC LIMIT %(batch_size)s
        """, {"last_id": last_id, "batch_size": BATCH_SIZE}).fetchall()
        if not rows:
            break
        ids = [r["id"] for r in rows]
        db.execute("""
            UPDATE users SET verified_at = created_at
            WHERE id = ANY(%(ids)s) AND verified_at IS NULL
        """, {"ids": ids})
        last_id = ids[-1]
        time.sleep(0.05)
```

---

## 12. Disk Space During Migrations

### Four Sources of Unexpected Disk Usage

**Table rewrites** create a full copy before dropping the original (2× table size). **Concurrent index builds** hold both the old and new index on disk simultaneously. **MVCC dead-row bloat** (PostgreSQL) accumulates during heavy backfills as autovacuum struggles to keep up — the degree depends on row size, update frequency, and autovacuum configuration. **WAL retention** from heavy writes can grow to gigabytes until replicas catch up.

### Which PostgreSQL Operations Cause a Full Table Rewrite

| Operation | Full Rewrite? | Notes |
|---|---|---|
| `ADD COLUMN ... NULL` | **No** | The safe expand/contract approach |
| `ADD COLUMN ... NOT NULL DEFAULT <constant>` | **No** (PG 11+) | Constant default stored in catalog |
| `ADD COLUMN ... NOT NULL DEFAULT <function>` | **Yes** | Function-computed defaults still rewrite |
| `ALTER COLUMN TYPE` | **Yes** | Always rewrites unless cast is trivial |
| `VACUUM FULL` | **Yes** | Creates a new compact copy |
| `CREATE INDEX CONCURRENTLY` | **No** | Builds new index without rewriting table |

### Monitoring Disk and Bloat

```sql
SELECT tablename,
       pg_size_pretty(pg_total_relation_size(tablename::regclass)) AS current_size,
       n_dead_tup AS dead_rows, n_live_tup AS live_rows,
       last_autovacuum, last_autoanalyze
FROM pg_stat_user_tables
WHERE tablename = 'orders';
```

---

## 13. Lock Diagnostics

### Set `lock_timeout` Before Every DDL Statement

> **🚨 This is non-negotiable. Add it to every migration script.**
>
> An `ALTER TABLE` waiting for a lock silently queues all subsequent queries behind it. Within 30 seconds, your connection pool can be full.
>
> ```sql
> -- ✅ Required at the top of every migration script
> SET lock_timeout = '5s';
> ALTER TABLE users ADD COLUMN verified_at TIMESTAMP NULL;
> -- On timeout: ERROR: canceling statement due to lock timeout
> -- → find the blocking query, wait for it to finish, then retry
> ```

If you cannot acquire the lock in 5 seconds, **fail fast and retry** rather than queuing and cascading.

### Finding the Blocking Chain (PostgreSQL)

```sql
SELECT
    blocked.pid AS blocked_pid, LEFT(blocked.query, 80) AS blocked_query,
    blocking.pid AS blocking_pid, LEFT(blocking.query, 80) AS blocking_query,
    now() - blocked.query_start AS blocked_for
FROM pg_catalog.pg_locks blocked_locks
JOIN pg_catalog.pg_stat_activity blocked ON blocked.pid = blocked_locks.pid
JOIN pg_catalog.pg_locks blocking_locks
    ON blocking_locks.locktype = blocked_locks.locktype
    AND blocking_locks.relation = blocked_locks.relation
    AND blocking_locks.pid != blocked_locks.pid
JOIN pg_catalog.pg_stat_activity blocking ON blocking.pid = blocking_locks.pid
WHERE NOT blocked_locks.granted
ORDER BY blocked_for DESC;
```

**Once you have the blocking PID, decide how to unblock:**

```sql
-- Step 1: Try a clean cancel first (sends SIGINT — the query aborts, transaction rolls back)
SELECT pg_cancel_backend(<blocking_pid>);

-- Step 2: Only if pg_cancel_backend has no effect (session is idle-in-transaction,
-- not actively running a query): terminate the connection entirely
SELECT pg_terminate_backend(<blocking_pid>);
```

> **⚠️ Before terminating:** inspect `blocking_query` in the diagnostic output above. Terminating a session mid-write to a financial table can leave partial data. `pg_cancel_backend` is always safer — it signals a clean abort. Use `pg_terminate_backend` only when the session shows `idle in transaction` state (holding a lock but no active query).

### Lock Mode Compatibility

| Operation | Lock Acquired | What It Blocks |
|---|---|---|
| `SELECT` | `AccessShareLock` | Only `AccessExclusiveLock` |
| `INSERT` / `UPDATE` / `DELETE` | `RowExclusiveLock` | `Share`, `ShareRowExclusive`, `Exclusive`, `AccessExclusive` |
| `CREATE INDEX CONCURRENTLY` | `ShareUpdateExclusiveLock` | `ShareUpdateExclusive` and stronger |
| `VALIDATE CONSTRAINT` | `ShareUpdateExclusiveLock` | Concurrent DDL only |
| `ALTER TABLE` (most forms) | `AccessExclusiveLock` | **Everything** — all reads and writes |

---

## 14. VACUUM, Bloat, and Transaction ID Wraparound

> **🆕 This section is essential reading before any large backfill.**

### Dead Tuples: The Hidden Cost of Every UPDATE

Every `UPDATE` in PostgreSQL creates a **dead tuple** — the old row version is retained in the table until VACUUM reclaims it. This is by design (MVCC multi-version concurrency), but it has serious consequences at migration scale.

When you run a batched backfill across 50 million rows, you are creating up to **50 million dead tuples**. If autovacuum cannot keep up, those dead tuples remain in the table and its indexes permanently (until the next VACUUM), causing:

- **Table bloat** — the physical table file grows far beyond its logical data size, causing slower sequential scans
- **Index bloat** — index pages fill with dead-row pointers, increasing the size and depth of every index, slowing every indexed query
- **Autovacuum thrashing** — a runaway autovacuum process competing with live queries for I/O

> **📘 Real-World Scale**
>
> **Illustrative scale estimate:** A 100M-row table with an average row size of 200 bytes occupies roughly 20GB on disk. A full-table backfill (every row updated once) can temporarily create a comparable volume of dead tuples until autovacuum reclaims them — exact bloat depends on row size, index count, and autovacuum throughput. These are illustrative approximations; measure your specific table using the disk queries in Section 12 before planning for migration disk headroom.

### Monitoring Dead Tuples During a Backfill

```sql
-- Run this in a separate session while the backfill is in progress
SELECT
    n_live_tup                AS live_rows,
    n_dead_tup                AS dead_rows,
    ROUND(
        n_dead_tup::DECIMAL / NULLIF(n_live_tup + n_dead_tup, 0) * 100, 1
    )                         AS dead_pct,
    last_autovacuum,
    last_autoanalyze,
    pg_size_pretty(pg_total_relation_size('users')) AS total_size
FROM pg_stat_user_tables
WHERE relname = 'users';
```

**Alert threshold:** If `dead_pct` climbs above 20% and autovacuum is not running, **pause the backfill** and run a manual VACUUM.

### Tuning Autovacuum for the Migrated Table

By default, autovacuum triggers when dead tuples exceed 20% of the table (`autovacuum_vacuum_scale_factor = 0.2`). On a 100M-row table, that is 20 million dead tuples before autovacuum fires. During a heavy backfill, this threshold may never be crossed until after the damage is done.

**Reduce the threshold for the specific table during your migration:**

```sql
-- Lower the vacuum trigger for this table: fire when 5% of rows are dead
-- (instead of the default 20%)
ALTER TABLE users SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_analyze_scale_factor = 0.02,
    autovacuum_vacuum_cost_delay = 2     -- ms between vacuum I/O operations; lower = faster
);

-- After migration completes, restore defaults
ALTER TABLE users RESET (
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor,
    autovacuum_vacuum_cost_delay
);
```

### Running Manual VACUUM During a Backfill

Do not wait for autovacuum to catch up on a large backfill — run VACUUM manually in a separate session:

```sql
-- Non-locking: runs concurrently with reads and writes
VACUUM ANALYZE users;
```

> **⚠️ Do NOT use `VACUUM FULL` on a live table.** `VACUUM FULL` creates a fully rewritten copy of the table and holds an `AccessExclusiveLock` for the entire duration — effectively the same as the dangerous `ALTER TABLE` you were trying to avoid. Reserve `VACUUM FULL` for maintenance windows only.

### Checking Index Bloat After a Backfill

After a heavy backfill completes, inspect index bloat and consider reindexing:

```sql
-- Estimate index bloat using pg_stat_user_indexes
SELECT
    indexrelname AS index_name,
    pg_size_pretty(pg_relation_size(indexrelid)) AS index_size,
    idx_scan,
    idx_tup_read,
    idx_tup_fetch
FROM pg_stat_user_indexes
WHERE relname = 'users'
ORDER BY pg_relation_size(indexrelid) DESC;

-- Non-blocking reindex after backfill (PostgreSQL 12+)
REINDEX INDEX CONCURRENTLY idx_users_email;
```

---

### Transaction ID (TXID) Wraparound — The Database Shutdown Risk

This is the failure mode that most engineers never encounter — but when they do, it causes a complete database shutdown.

**How PostgreSQL assigns transaction IDs:** Every transaction gets a 32-bit integer transaction ID (TXID). The XID space is circular: of the ~4.3 billion possible 32-bit values, PostgreSQL treats approximately **2 billion as "the past"** and the other 2 billion as "the future" using modular arithmetic. This means the practical limit before wraparound protection kicks in is approximately **2 billion unvacuumed transactions** per table. *(Source: PostgreSQL official documentation — Routine Vacuuming, https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-WRAPAROUND)*

**The risk:** If VACUUM does not keep up, the gap between the oldest unfrozen transaction and the current counter closes. PostgreSQL begins issuing `WARNING` log messages when approximately 10 million transactions remain before the limit. When protection triggers, **PostgreSQL refuses all write operations and requires a manual VACUUM before writes resume** — in modern PostgreSQL versions this typically does not require single-user mode, but it does require immediate unplanned maintenance. *(Source: Google Cloud SQL PostgreSQL documentation — Overcome TXID wraparound protection, https://cloud.google.com/sql/docs/postgres/txid-wraparound; Percona PMM documentation, https://docs.percona.com/percona-monitoring-and-management/3/advisors/checks/postgresql-transaction-id-wraparound-is-approaching.html)*

> **📘 Why Migrations Accelerate Wraparound**
>
> Aggressive backfills consume transaction IDs quickly — thousands per second. Long-running transactions (Section 9) prevent VACUUM from advancing the oldest-transaction horizon. A combination of both can advance toward wraparound faster than routine autovacuum can compensate.

### Monitoring TXID Horizon

```sql
-- Check how far away wraparound is for each database
-- age() returns the number of transactions since the oldest unfrozen XID
SELECT
    datname,
    age(datfrozenxid)                                           AS txid_age,
    2000000000 - age(datfrozenxid)                              AS txids_remaining,
    ROUND(age(datfrozenxid)::DECIMAL / 2000000000 * 100, 2)    AS pct_consumed
FROM pg_database
ORDER BY age(datfrozenxid) DESC;
-- Note: 2,000,000,000 is the practical autovacuum_freeze_max_age ceiling PostgreSQL
-- enforces before triggering aggressive vacuuming. Use 2B as your monitoring sentinel.
```

```sql
-- Check the most at-risk tables (tables with the oldest unfrozen XID)
SELECT
    relname AS table_name,
    age(relfrozenxid) AS table_txid_age
FROM pg_class
WHERE relkind = 'r'
ORDER BY age(relfrozenxid) DESC
LIMIT 20;
```

**Alert thresholds** *(derived from PostgreSQL documentation and Crunchy Data operational guidance — https://www.crunchydata.com/blog/managing-transaction-id-wraparound-in-postgresql):*

| `txid_age` | Status | Action |
|---|---|---|
| < 500M | ✅ Safe | Autovacuum operating normally |
| 500M – 1B | ⚠️ Monitor | Verify autovacuum is running; check for long-running transactions blocking freeze |
| 1B – 1.9B | 🔴 High Risk | Manually run `VACUUM FREEZE` on the most-aged tables; reduce write load if possible |
| > 1.9B | 🚨 Critical | PostgreSQL issues `WARNING` logs when ~10M transactions remain; writes will be blocked before the 2B limit is reached — immediate `VACUUM FREEZE` required |

### Forcing TXID Freeze on At-Risk Tables

```sql
-- VACUUM FREEZE marks rows as "frozen" — their XID no longer ages
-- This is a non-locking operation (unlike VACUUM FULL)
VACUUM FREEZE ANALYZE users;

-- Check that the freeze was effective
SELECT relname, age(relfrozenxid) AS age_after_freeze
FROM pg_class WHERE relname = 'users';
```


### Tuning `autovacuum_work_mem` for Heavy Backfills

By default, autovacuum inherits `maintenance_work_mem` for its working memory (`autovacuum_work_mem = -1`). On systems where `maintenance_work_mem` is conservatively set, autovacuum processes large dead-tuple arrays too slowly to keep pace with a 50M-row backfill.

> **VP-level concern:** "I don't care if the backfill takes 4 days. I care if it triggers a forced database shutdown on day 3."

Increase it for the duration of the migration window, then restore it:

```sql
-- Temporarily increase autovacuum working memory (default = -1, often 64-256MB)
-- 1GB gives autovacuum enough headroom to process large dead-tuple arrays quickly
ALTER SYSTEM SET autovacuum_work_mem = '1GB';
SELECT pg_reload_conf();    -- takes effect on the next autovacuum worker start

-- After migration complete: restore the default
ALTER SYSTEM SET autovacuum_work_mem = -1;
SELECT pg_reload_conf();
```

For a manual VACUUM run, set the session-level equivalent:

```sql
SET maintenance_work_mem = '1GB';
VACUUM ANALYZE users;
-- Resets automatically when the session ends
```

**Monitoring whether autovacuum is keeping pace:**

```sql
-- Is autovacuum currently running on the target table?
SELECT pid, query, now() - query_start AS running_for
FROM pg_stat_activity
WHERE query LIKE '%autovacuum%' AND query LIKE '%users%';

-- Sample dead-tuple growth rate: run twice, ~60s apart
SELECT n_dead_tup, now() AS sampled_at FROM pg_stat_user_tables WHERE relname = 'users';
```

If dead tuples are growing faster than they are being reclaimed: reduce the backfill batch rate, increase `autovacuum_work_mem` further, or run manual `VACUUM ANALYZE` in a separate session after every 10 batches.

### What To Do During an Aggressive Backfill

1. **Before starting:** Check `txid_age` baseline — if already above 1B, delay the backfill until VACUUM FREEZE has been run.
2. **During the backfill:** Monitor `txid_age` every hour. If it rises faster than expected, reduce batch frequency or pause.
3. **After the backfill:** Run `VACUUM ANALYZE` on the target table. Re-check `txid_age` and `n_dead_tup` before declaring the migration complete.
4. **Long-term:** Ensure autovacuum is not disabled or starved of resources on any table involved in migration-heavy workloads.

---

## 15. JSONB to Structured Column Migrations

### Why This Is Different

Splitting a plain text column is mechanical. Extracting fields from a JSONB blob is a **schema archaeology problem**: the field may be absent from some rows, spelled differently across document versions, or nested at varying depths. Getting the backfill wrong is silent — no constraint violation, no type error, just incorrect data in the new column.

This is now one of the most common migration patterns in production — most modern applications accumulate JSONB blobs before eventually needing queryable, indexed structured columns.

### Phase 1: Understand the JSONB Shape Before Writing Any SQL

Never assume uniform structure. Sample the actual distribution first:

```sql
-- What keys exist, and how often?
SELECT
    key,
    COUNT(*) AS occurrences,
    ROUND(COUNT(*)::DECIMAL / (SELECT COUNT(*) FROM events) * 100, 1) AS pct_rows
FROM events, jsonb_object_keys(payload) AS key
GROUP BY key
ORDER BY occurrences DESC;

-- What types does a specific field actually contain?
SELECT jsonb_typeof(payload->'amount') AS value_type, COUNT(*)
FROM events WHERE payload ? 'amount'
GROUP BY 1;

-- Sample rows where the field is missing
SELECT id, payload FROM events WHERE payload->>'amount' IS NULL LIMIT 20;
```

This reveals which rows will be `NULL` in the new column (field absent), which need type coercion, and whether naming inconsistencies exist.

### Phase 2: Add the New Structured Columns (Expand)

```sql
-- Always nullable at first — rows without the field will stay NULL
ALTER TABLE events ADD COLUMN amount_cents INTEGER NULL;
ALTER TABLE events ADD COLUMN currency     VARCHAR(3) NULL;
```

### Phase 3: Backfill Using a Batched FROM-Subquery — Not a Correlated Subquery

> **The CPU Trap: Correlated Subquery vs Batched FROM-Subquery**
>
> The intuitive approach is a correlated subquery. It will destroy performance on large tables:
>
> ```sql
> -- DANGEROUS: correlated subquery re-evaluates for every row independently
> UPDATE events
> SET amount_cents = (
>     SELECT (e2.payload->>'amount')::INTEGER
>     FROM events e2 WHERE e2.id = events.id
> );
> -- On 20M rows: re-executes 20M times. CPU spikes to 100%.
> -- API latency doubles within minutes.
> ```
>
> Use a batched `FROM`-subquery instead — PostgreSQL evaluates it once per batch, not once per row:

```python
import time

def backfill_jsonb_to_columns(db):
    BATCH_SIZE = 1_000
    last_id    = 0

    while True:
        result = db.execute("""
            UPDATE events e
            SET
                amount_cents = (src.amount_raw::NUMERIC * 100)::INTEGER,
                currency     = src.currency_raw
            FROM (
                SELECT id,
                       payload->>'amount'   AS amount_raw,
                       payload->>'currency' AS currency_raw
                FROM events
                WHERE id > %(last_id)s
                  AND amount_cents IS NULL
                ORDER BY id
                LIMIT %(batch_size)s
            ) AS src
            WHERE e.id = src.id
        """, {"last_id": last_id, "batch_size": BATCH_SIZE})

        if result.rowcount == 0:
            break

        last_id = db.execute(
            "SELECT MAX(id) FROM events WHERE id > %s AND amount_cents IS NOT NULL",
            (last_id,)
        ).fetchone()[0] or last_id
        time.sleep(0.05)
```

### Phase 4: Classify NULL Rows Before Cutover

Do not assume `NULL` in the new column means the backfill missed it. Categorise every remaining `NULL`:

```sql
SELECT
    CASE
        WHEN payload IS NULL                    THEN 'null_payload'
        WHEN NOT (payload ? 'amount')           THEN 'field_absent'
        WHEN payload->>'amount' = ''            THEN 'empty_string'
        WHEN payload->>'amount' !~ '^[0-9.]+$' THEN 'non_numeric'
        ELSE 'backfill_missed'
    END         AS null_reason,
    COUNT(*)    AS row_count
FROM events
WHERE amount_cents IS NULL
GROUP BY 1
ORDER BY row_count DESC;
```

Each category requires a business decision before flipping reads. `field_absent` may legitimately map to `0` or `NULL` — but that is a product decision, not a migration assumption.

### Phase 5: Divergence Check

```sql
-- Verify extracted values match the source blob on a random 1% sample
-- On a 100M-row table, BERNOULLI(1) samples ~1M rows — adequate for general validation.
-- For financial or compliance-sensitive data, use BERNOULLI(10) or a full-table check.
SELECT id, payload->>'amount' AS blob_value, amount_cents
FROM events
TABLESAMPLE BERNOULLI(1)
WHERE amount_cents IS NOT NULL
  AND (payload->>'amount')::NUMERIC * 100 != amount_cents
LIMIT 100;
-- Zero rows = extraction consistent on sampled rows. Any rows = fix the backfill first.
```

---

## 16. ENUM Type Migrations

### The Transaction Block Problem

PostgreSQL ENUM types have a hard constraint: **`ALTER TYPE ... ADD VALUE` cannot execute inside a transaction block**.

This is not a locking concern — it is a parser-level restriction. If your migration runner wraps scripts in `BEGIN ... COMMIT` (as Flyway, Alembic, and most runners do by default), adding a value to an ENUM will fail immediately:

```
ERROR: ALTER TYPE ... ADD VALUE cannot run inside a transaction block
```

The entire deployment halts. The runner marks the migration as failed and stops.

### Safe: Adding an ENUM Value (Non-Transactional)

```sql
-- Must be a standalone autocommit statement — NOT inside BEGIN/COMMIT
ALTER TYPE order_status ADD VALUE IF NOT EXISTS 'refunded' AFTER 'completed';
```

**In Flyway**, annotate the migration file to disable the implicit transaction:

```sql
-- V7__add_refunded_status.sql
-- flyway:nonTransactional
ALTER TYPE order_status ADD VALUE IF NOT EXISTS 'refunded' AFTER 'completed';
```

**In Alembic**, use `AUTOCOMMIT` isolation for the specific statement:

```python
def upgrade():
    connection = op.get_bind()
    connection.execution_options(isolation_level="AUTOCOMMIT").execute(
        "ALTER TYPE order_status ADD VALUE IF NOT EXISTS 'refunded' AFTER 'completed'"
    )
```

### Dangerous: Removing or Renaming an ENUM Value

PostgreSQL has **no direct DDL for removing or renaming an ENUM value**:

```
ERROR: cannot drop value from an enum type
```

The only safe path is a full Expand/Contract cycle on the type:

```sql
-- Step 1: Create a new type with the desired final values
CREATE TYPE order_status_v2 AS ENUM ('pending', 'completed', 'refunded');

-- Step 2: Add a new column using the new type (nullable, for Expand)
ALTER TABLE orders ADD COLUMN status_v2 order_status_v2 NULL;

-- Step 3: Backfill — map old values to new with explicit business rules
UPDATE orders
SET status_v2 = CASE
    WHEN status::TEXT = 'cancelled' THEN 'completed'   -- map removed value
    ELSE status::TEXT::order_status_v2
END
WHERE status_v2 IS NULL;

-- Step 4: After divergence check + read cutover (Contract):
ALTER TABLE orders DROP COLUMN status;
ALTER TABLE orders RENAME COLUMN status_v2 TO status;
DROP TYPE order_status;
ALTER TYPE order_status_v2 RENAME TO order_status;
```

> **Only `ALTER TYPE ... ADD VALUE` requires autocommit. All other steps in this cycle execute normally inside transactions.**

### ENUM vs VARCHAR + CHECK — When to Choose Each

| Approach | Add Value | Remove Value | Rename Value | Cross-DB Portable |
|---|---|---|---|---|
| `ENUM` type | Non-transactional DDL | Full Expand/Contract | Full Expand/Contract | No |
| `VARCHAR` + `CHECK` | New migration (in-transaction) | New migration | New migration | Yes |
| Reference table (FK) | `INSERT` — no DDL at all | `DELETE` or soft-delete | `UPDATE` — no DDL | Yes |

If ENUM values change more than twice a year, a reference table or `VARCHAR + CHECK` is almost always lower-risk.

---

## 17. Sentinel Values in Dual-Write Migrations

### The NULL Ambiguity Problem

Section 4 uses `NULL` as the signal that a row has not yet been processed by the new logic. This works when `NULL` has no pre-existing business meaning. It breaks when `NULL` is already a valid, intentional data state.

> **Real Example: Migrating `notification_preferences`**
>
> Old schema: a JSONB `preferences` blob. New schema: `email_opt_in BOOLEAN`.
>
> Business rules: `NULL` means "user has not yet set a preference" (valid, ongoing). `TRUE` = opted in. `FALSE` = opted out.
>
> The backfill guard `WHERE email_opt_in IS NULL` processes unmigrated rows correctly. But after cutover, new users are also written with `NULL` — which is a valid ongoing state. The backfill job begins re-processing live users. The divergence check cannot distinguish "not yet migrated" from "genuinely unset preference."

### The Fix: Use a Dedicated Migration-Tracking Column

Do not overload `NULL`. Add an explicit `_migrated` boolean that carries the migration state independently from the data value:

```sql
ALTER TABLE users ADD COLUMN email_opt_in          BOOLEAN NULL;
ALTER TABLE users ADD COLUMN _email_pref_migrated  BOOLEAN NOT NULL DEFAULT FALSE;
```

```sql
-- Backfill: process only unmigrated rows, not business-logic NULLs
UPDATE users
SET
    email_opt_in         = (preferences->>'email_notifications')::BOOLEAN,
    _email_pref_migrated = TRUE
WHERE _email_pref_migrated = FALSE
  AND id > %(last_id)s
ORDER BY id
LIMIT %(batch_size)s;
```

```sql
-- Divergence check: are migrated rows consistent with source?
SELECT COUNT(*) FROM users
WHERE _email_pref_migrated = TRUE
  AND email_opt_in IS DISTINCT FROM (preferences->>'email_notifications')::BOOLEAN;
-- Expected: 0
```

Application code during transition falls back to the old source for unmigrated rows:

```python
def get_email_preference(user_id):
    row = db.execute(
        "SELECT email_opt_in, _email_pref_migrated FROM users WHERE id = %s", (user_id,)
    ).fetchone()
    if row["_email_pref_migrated"]:
        return row["email_opt_in"]
    return read_from_preferences_blob(user_id)   # fall back to JSONB
```

### Out-of-Range Sentinels for Numeric and Text Columns

When a separate tracking column is impractical, use an out-of-range value that cannot appear in production data:

```sql
-- INTEGER: -1 is unambiguous for count/amount columns
ALTER TABLE orders ADD COLUMN item_count_v2 INTEGER NOT NULL DEFAULT -1;
-- Backfill guard: WHERE item_count_v2 = -1

-- TEXT: a magic string no real value would ever match
ALTER TABLE products ADD COLUMN sku_v2 VARCHAR(50) NOT NULL DEFAULT '__PENDING_MIGRATION__';
-- Backfill guard: WHERE sku_v2 = '__PENDING_MIGRATION__'
```

### Sentinel Lifecycle — Clean Up After Migration

```sql
-- Verify no sentinels remain before decommissioning the tracking infrastructure
SELECT COUNT(*) FROM users WHERE _email_pref_migrated = FALSE;   -- Expected: 0
SELECT COUNT(*) FROM orders WHERE item_count_v2 = -1;            -- Expected: 0
```

> **If sentinels remain:** Do NOT proceed to the Contract phase. Remaining sentinels mean either (a) the backfill did not complete, (b) new rows are being inserted without going through the new write path, or (c) the dual-write code has a bug. Investigate which rows remain and why before touching the schema.

```sql
-- Drop tracking column only after the count above returns 0
ALTER TABLE users DROP COLUMN _email_pref_migrated;
```

Sentinels left in production become sources of future confusion and incorrect query results. Clean-up is a required step of the Contract phase, not an afterthought.


## 18. Versioned Migration System

Every schema change should be a **versioned, immutable artifact** — tracked in version control and in a database history table so you always know exactly what state the schema is in.

### File Naming Convention

```
migrations/
├── V1__create_users_table.sql
├── V2__add_email_index.sql
├── V3__add_verified_at_column.sql
└── rollback/
    └── V3__rollback.sql
```

**Three immutable rules:** Never edit a migration that has been applied in any environment. Never delete one. Never apply out of order.

### Schema History Table

```sql
CREATE TABLE schema_migrations (
    version        VARCHAR(50)  PRIMARY KEY,
    description    TEXT         NOT NULL,
    script         TEXT         NOT NULL,
    checksum       VARCHAR(64),
    applied_by     VARCHAR(100),
    applied_at     TIMESTAMP    DEFAULT NOW(),
    execution_ms   INTEGER,
    status         VARCHAR(20)  DEFAULT 'success'
);
```

### Checksum Enforcement

```python
import hashlib

def verify_migration_integrity(db, migrations_dir: str) -> None:
    applied = db.execute(
        "SELECT script, checksum FROM schema_migrations WHERE status = 'success'"
    ).fetchall()
    for row in applied:
        filepath = f"{migrations_dir}/{row['script']}"
        current  = hashlib.sha256(open(filepath, "rb").read()).hexdigest()
        if current != row["checksum"]:
            raise RuntimeError(f"INTEGRITY VIOLATION: {row['script']} was modified after being applied.")
    print(f"✅ {len(applied)} migration checksums verified.")
```

---

## 19. Central Migration Registry

In a microservices architecture, a **Central Migration Registry** gives the platform team a single view across all services — which are mid-migration, which have failed, and whether cross-service dependencies are satisfied.

### Registry Schema

```sql
CREATE TABLE migration_registry (
    id               SERIAL       PRIMARY KEY,
    service_name     VARCHAR(100) NOT NULL,
    version          VARCHAR(50)  NOT NULL,
    status           VARCHAR(20)  NOT NULL,
    environment      VARCHAR(20)  NOT NULL,
    started_at       TIMESTAMP,
    completed_at     TIMESTAMP,
    duration_ms      INTEGER,
    applied_by       VARCHAR(100),
    UNIQUE (service_name, version, environment)
);
```

### Enforcing Cross-Service Dependencies

```python
def assert_dependencies_met(db, service_name: str, version: str) -> None:
    deps = db.execute("""
        SELECT depends_on_service, depends_on_version
        FROM migration_dependencies
        WHERE service_name = %s AND version = %s
    """, (service_name, version)).fetchall()
    for dep in deps:
        result = db.execute("""
            SELECT status FROM migration_registry
            WHERE service_name = %s AND version = %s AND environment = 'production'
        """, (dep["depends_on_service"], dep["depends_on_version"])).fetchone()
        if not result or result["status"] != "success":
            raise RuntimeError(
                f"Cannot run {service_name} {version}: "
                f"requires {dep['depends_on_service']} {dep['depends_on_version']} first."
            )
```

---

## 20. Migration Runner

A migration runner executes migrations in version order, records results, and halts on failure. It must guarantee: mutual exclusion, history awareness, checksum verification, ordered application, and stop-on-failure.

### Minimal Runner (Python + PostgreSQL)

```python
import os, hashlib, time, psycopg2, psycopg2.extras

class MigrationRunner:
    ADVISORY_LOCK_KEY = 9876543210

    def __init__(self, db_url, migrations_dir):
        # db_url must come from an environment variable or secrets manager — never hardcode.
        # For AWS RDS, prefer IAM authentication:
        #   token = boto3.client('rds').generate_db_auth_token(host, port, region, user)
        #   db_url = f"postgresql://{user}:{token}@{host}/{db}?sslmode=require"
        self.conn = psycopg2.connect(db_url, cursor_factory=psycopg2.extras.DictCursor)
        self.dir  = migrations_dir
        self._ensure_history_table()

    def _acquire_lock(self):
        with self.conn.cursor() as cur:
            cur.execute("SELECT pg_try_advisory_lock(%s)", (self.ADVISORY_LOCK_KEY,))
            acquired = cur.fetchone()[0]
        if not acquired:
            raise RuntimeError(
                "Another migration runner holds the advisory lock. "
                "Only one runner may execute migrations at a time."
            )

    def _release_lock(self):
        with self.conn.cursor() as cur:
            cur.execute("SELECT pg_advisory_unlock(%s)", (self.ADVISORY_LOCK_KEY,))

    def run(self):
        self._acquire_lock()
        try:
            for filename in self._pending_files():
                version  = filename.split("__")[0]
                filepath = os.path.join(self.dir, filename)
                checksum = hashlib.sha256(open(filepath, "rb").read()).hexdigest()
                t0 = time.time()
                try:
                    with self.conn.cursor() as cur:
                        cur.execute(open(filepath).read())
                        cur.execute(
                            "INSERT INTO schema_migrations (version, checksum, duration_ms) VALUES (%s,%s,%s)",
                            (version, checksum, int((time.time()-t0)*1000))
                        )
                    self.conn.commit()
                    print(f"✅  {version} ({int((time.time()-t0)*1000)}ms)")
                except Exception as e:
                    self.conn.rollback()
                    print(f"❌  {version} FAILED: {e}")
                    raise
        finally:
            self._release_lock()
```

### CI/CD Integration

```yaml
jobs:
  deploy:
    steps:
      - name: Run database migrations
        # DATABASE_URL must be injected as a CI/CD secret — never hardcode credentials.
        # For AWS RDS: prefer IAM auth token over password-based connection strings.
        env:
          DATABASE_URL: ${{ secrets.DATABASE_URL }}
        run: python migrate.py

      - name: Verify schema state
        env:
          DATABASE_URL: ${{ secrets.DATABASE_URL }}
        run: |
          python migrate.py --check
          # --check: prints any pending migrations; exits non-zero if any remain.
          # Implement this flag so silent runner failures are caught before app deploy.

      - name: Deploy application
        run: kubectl apply -f k8s/deployment.yaml
```

---

## 21. Infrastructure-Level Migrations

### Database Engine Migration (e.g., MySQL → PostgreSQL)

| Phase | Key Actions |
|---|---|
| Schema Translation | Translate DDL; handle type differences (`TINYINT` → `SMALLINT`, `AUTO_INCREMENT` → `SERIAL`) |
| Data Migration | Offline: `pg_dump` + `pgLoader`; Live: AWS DMS or Debezium CDC |
| Application Cutover | Update connection strings; audit every query for dialect differences |
| Validation | Row counts, checksum sampling, error rate baseline |

| Tool | Source → Target | Notes |
|---|---|---|
| **AWS DMS** | MySQL/Oracle/MSSQL → PostgreSQL | Managed; supports full-load and CDC. **Critical:** the DMS replication instance must be sized for peak CDC throughput — an undersized instance (e.g. `dms.t3.medium` for a high-write-volume table) will fall behind during full-load and never recover, stalling the migration silently. Size the replication instance at least as large as your RDS instance. |
| **pgLoader** | MySQL/SQLite/CSV → PostgreSQL | Open source; fast bulk load |
| **Debezium** | MySQL/PostgreSQL/Oracle → Kafka | CDC-based; zero-downtime |
| **gh-ost** | MySQL → MySQL | Online schema changes without table locks |

### Adding a Connection Pooler (e.g., PgBouncer)

Migrate incrementally — deploy alongside, test one low-traffic service first, then migrate remaining services one at a time.

> **⚠️ Watch Out:** PgBouncer in `transaction` mode does **not** support prepared statements or session-level advisory locks. Applications using either must switch to `session` mode or refactor before migration.

---

## 22. Migration Tooling — The Full Landscape

### SQL Database Tools

| Tool | Ecosystem | Best For |
|---|---|---|
| **Flyway** | Any (SQL-first) | Teams starting with migrations; plain SQL |
| **Liquibase** | Any (YAML/XML/SQL) | Multi-DB; built-in rollback; enterprise audit |
| **Alembic** | Python / SQLAlchemy | Python-native teams |
| **Django Migrations** | Python / Django | Auto-generated from model changes |
| **Prisma Migrate** | TypeScript / Node.js | Declarative schema |
| **gh-ost** | MySQL | Online schema changes without locks |
| **AWS DMS** | Managed | Heterogeneous migrations with CDC |

### NoSQL Migration (Mongock)

```java
@ChangeUnit(id = "add-verified-at", order = "002", author = "gugan")
public class AddVerifiedAtMigration {
    @Execution
    public void execute(MongoDatabase db) {
        db.getCollection("users").updateMany(
            Filters.not(Filters.exists("verified_at")),
            Updates.set("verified_at", new Date())
        );
    }
    @RollbackExecution
    public void rollback(MongoDatabase db) {
        db.getCollection("users").updateMany(
            Filters.exists("verified_at"), Updates.unset("verified_at")
        );
    }
}
```

### Kafka Schema Migration

| Compatibility Mode | Enforces | When to Use |
|---|---|---|
| `BACKWARD` (default) | New schema reads old messages | Upgrade consumers before producers |
| `FORWARD` | Old schema reads new messages | Upgrade producers before consumers |
| `FULL` | Both directions | Safest; most restrictive |
| `NONE` | No check | Development only — never production |

### Vector Database Migration

Embeddings are **model-specific** — vectors from one model are numerically incompatible with another. When changing embedding models, every vector must be regenerated from source text.

```python
def migrate_vectors(source_db, target_db, new_model, batch_size=100):
    offset = 0
    while True:
        docs = source_db.query(
            "SELECT id, document_text, metadata FROM documents ORDER BY id LIMIT %s OFFSET %s",
            (batch_size, offset)
        ).fetchall()
        if not docs:
            break
        new_vectors = new_model.embed_batch([d["document_text"] for d in docs])
        target_db.upsert(collection_name="documents", points=[
            {"id": d["id"], "vector": v, "payload": d["metadata"]}
            for d, v in zip(docs, new_vectors)
        ])
        offset += batch_size
        time.sleep(0.1)
```

---

## 23. Migration Runbook Template

Every migration touching a live table or collection with more than 100K rows deserves a written runbook. A second engineer must review it before anything runs.

```markdown
# Migration Runbook: [Short Description]

## Summary
One paragraph: what does this migration do, why is it needed, what is the expected impact if wrong?

## Risk Level
- [ ] Low    — Additive only (nullable column, new table, CONCURRENTLY index)
- [ ] Medium — Data backfill required; schema change on active table
- [ ] High   — Destructive change; FK or check constraint on large table; type change

## Pre-Migration Checklist
- [ ] Row count checked on production: ___________
- [ ] Estimated duration recorded (tested on staging at prod scale): ___________
- [ ] Disk space verified: ___________ (2× table size free for rewrites; 1.5× for backfills)
- [ ] Autovacuum health checked: n_dead_tup baseline recorded, autovacuum not disabled
- [ ] TXID age checked: age(datfrozenxid) < 1B before starting
- [ ] Snapshot taken — ID: ___________
- [ ] Rollback script written, reviewed, and tested on staging
- [ ] `lock_timeout = '5s'` set at top of migration script
- [ ] On-call engineer available and aware
- [ ] Monitoring dashboards open: DB CPU, replication lag, n_dead_tup, lock wait count, app error rate
- [ ] Column slot count checked (if using Expand/Contract on a frequently-migrated table)
- [ ] No ongoing incidents or high-risk deployments in flight

## Migration Steps

### Step 1: [Description]
```sql
SET lock_timeout = '5s';   -- REQUIRED on every DDL step
-- migration SQL here
```
Expected duration: ___
Verification query and expected output:
```sql
SELECT COUNT(*) FROM users WHERE verified_at IS NULL;
-- Expected: 0
```

### Step 2: [Backfill / Validation]
Monitor during backfill:
```sql
SELECT n_dead_tup, last_autovacuum FROM pg_stat_user_tables WHERE relname = 'users';
-- If n_dead_tup > 20% of n_live_tup: pause and run VACUUM ANALYZE
```

## Post-Migration Verification
- [ ] Row counts match expected
- [ ] No INVALID indexes (`SELECT * FROM pg_index WHERE indisvalid = FALSE`)
- [ ] Application error rate returned to baseline
- [ ] Replication lag returned to baseline
- [ ] Backfill divergence check passed (if applicable)
- [ ] VACUUM ANALYZE run after heavy backfill
- [ ] Index bloat checked; REINDEX CONCURRENTLY run if bloat > 30%
- [ ] TXID age checked post-migration: not significantly higher than pre-migration baseline

## Rollback Triggers (fill in real numbers — never leave blank)

> **How to fill these in:** use your last 30 days of observability data.
> - Error rate: your P99 baseline × 3, sustained for ≥ 3 minutes
> - Duration cutoff: staging measured duration × 3
> - Replication lag: your application's read-staleness SLA
> - Dead tuple ceiling: 50% is a hard stop; 30% is a better early warning

- Auto-rollback: error rate > __% for __ consecutive minutes *(baseline × 3, ≥ 3 min)*
- Manual rollback: migration not complete within __ hours *(staging estimate × 3)*
- Manual rollback: replication lag > __ seconds and not recovering *(your staleness SLA)*
- Manual rollback: n_dead_tup > 50% of n_live_tup and autovacuum not catching up

## Sign-Off
Executed by: ___   At: ___   Reviewed and verified by: ___
```

---

## 24. What Goes Wrong — Six Patterns From Production

### 🔥 Failure #1: "It Ran Fine in Staging"

Staging had 50,000 rows. Production had 80 million. An `ALTER TABLE ADD COLUMN NOT NULL DEFAULT NOW()` completed in 0.3 seconds in staging. In production it held an exclusive lock for 11 minutes. Connection pool exhausted. Full service outage.

**The rule:** Always check row count before any migration on a live table. If > 1,000,000 on a live system: use Expand/Contract, not a direct `ALTER TABLE`.

---

### 🔥 Failure #2: "The Rollback Made Things Worse"

A team dropped a column (Phase 3) while Kubernetes was mid-rolling-deploy. Old pods (still reading the column) crashed. The rollback re-added the column. New pods crashed on the re-added column. Total outage: 12 minutes.

**The rule:** Never run Phase 3 (DROP COLUMN) until the rolling deploy is 100% complete and the old version is fully retired.

---

### 🔥 Failure #3: "The Backfill Finished But the Data Was Wrong"

A 3-day backfill populated `normalized_email`. During those 3 days, some users updated their email. The backfill used values captured at job-start. A fraction of rows ended up with stale normalised values. Team cut reads without a divergence check.

**The rule:** Always run the divergence check before every read cutover.

```sql
SELECT id, raw_email, normalized_email FROM users
WHERE normalized_email != LOWER(TRIM(raw_email)) LIMIT 100;
-- If ANY rows returned: do NOT cut over. Fix the backfill first.
```

---

### 🔥 Failure #4: "The Lock Queue Took Down the Application"

An `ALTER TABLE` (needing `AccessExclusiveLock`) queued behind a 4-minute analytics query. Every new query to the table queued behind the waiting `ALTER TABLE`. Connection pool full in 30 seconds. 4-minute application outage — not from the migration itself, but from the lock queue.

**The rule:** Set `lock_timeout = '5s'` before every DDL statement. Fail fast and retry.

---

### 🔥 Failure #5: "The 'Safe' Backfill Destroyed Query Performance"

A team ran a compliant batched backfill — small batches, sleep between each, no table locks. The backfill completed without errors. Two days later, query performance on the table had degraded significantly. Support tickets arrived. Investigation revealed that autovacuum had not been able to keep up during the backfill. The table had tens of millions of dead tuples. Every index on the table had significant bloat, and the table had grown substantially on disk.

**The root cause:** The team checked the migration checklist (batch size, replication lag, lock timeout) but had no monitoring on `n_dead_tup` and had not tuned `autovacuum_vacuum_scale_factor` for the migration.

**The fix:** Three steps:
1. `VACUUM ANALYZE users;` — reclaimed dead tuples, updated statistics
2. `REINDEX INDEX CONCURRENTLY` on all affected indexes — rebuilt bloated indexes non-blockingly
3. Retroactively added dead-tuple monitoring to the migration runbook

**The rule:** Every backfill that touches more than 1M rows must monitor `n_dead_tup` in real time, tune autovacuum for the target table before starting, and run `VACUUM ANALYZE` + check index bloat after completing.

---

### 🔥 Failure #6: "The ENUM Migration Brought Down the Deployment Pipeline"

A team needed to add a new value to a PostgreSQL ENUM. The SQL was correct. The migration runner was Flyway in its default configuration, wrapping every script in an implicit transaction. The migration failed with:

```
ERROR: ALTER TYPE ... ADD VALUE cannot run inside a transaction block
```

Flyway marked the version as failed and stopped the pipeline. Because the statement never executed, the schema was unchanged — but the runner now refused to proceed. Engineers spent 90 minutes diagnosing what they assumed was a locking problem before discovering the transaction-block restriction.

**The root cause:** No engineer on the team knew that ENUM additions cannot run inside transactions. The Flyway documentation covers it — but only in a subsection most engineers never read.

**The fix:**
1. Mark the migration file with `-- flyway:nonTransactional`.
2. Delete the failed version record from the Flyway history table.
3. Re-run. A 90-second fix after a 90-minute diagnosis.

**The rule:** Any migration containing `ALTER TYPE ... ADD VALUE` must be marked non-transactional. Add this to your code review checklist. See [Section 16](#16-enum-type-migrations) for the full pattern and runner-specific instructions.


---

## 25. Quick Reference — Pre-Migration Checklist

Answer every question before starting. Do not proceed on any "no" or "I don't know."

| # | Question | If No → |
|---|---|---|
| 1 | **How many rows / documents / vectors will be touched?** | If > 1M on a live system: use Expand/Contract or batched approach, not Big Bang |
| 2 | **Does this operation lock the table?** | Use `CONCURRENTLY` for indexes; `NOT VALID` for FK constraints; set `lock_timeout = '5s'` |
| 3 | **Is there sufficient free disk space?** | 2× table size for rewrites; 1× index size for new indexes; **1.5× for backfills due to dead-tuple bloat** |
| 4 | **Is autovacuum healthy on the target table?** | Check `n_dead_tup` baseline; tune `autovacuum_vacuum_scale_factor` if > default 0.2; verify autovacuum is not disabled |
| 5 | **Is `lock_timeout = '5s'` set at the top of the migration script?** | Add it. No DDL statement should silently queue and cascade. |
| 6 | **Will the application stay live?** | If yes → Incremental. If maintenance window acceptable → Big Bang may be fine |
| 7 | **Is the rollback script written, tested, and reviewed?** | Stop. Write it first. |
| 8 | **Is the backfill idempotent?** | A re-run after a crash must skip already-processed rows without errors |
| 9 | **Is the divergence check written?** | Must pass before flipping reads (Phase 2 → Phase 3) |
| 10 | **Is the migration compatible with both N and N-1 app versions?** | Do not drop old schema while the previous app version still reads it |
| 11 | **For MySQL: is `innodb_lock_wait_timeout` set on the migration session?** | Default is 50 seconds — too long for a migration that should fail fast. Set it: `SET SESSION innodb_lock_wait_timeout = 5;`. PostgreSQL timeouts are covered by items 5 (lock_timeout) and 19 (statement_timeout). |
| 12 | **Is replication lag being monitored with an alert?** | Set an alert threshold and pause the backfill if it fires |
| 13 | **Will you run `REINDEX CONCURRENTLY` after the backfill?** | Index bloat from dead tuples must be addressed — don't skip this step |
| 14 | **Is the TXID age below 1B before starting?** | Check `SELECT age(datfrozenxid) FROM pg_database` — if > 1B, run `VACUUM FREEZE` first |
| 15 | **Is the column slot count safe (if using Expand/Contract)?** | Check total `pg_attribute` count including dropped columns; alert at 1,200 |
| 16 | **Is the runbook complete and signed off by a second engineer?** | No single-engineer sign-off on Medium or High risk migrations |
| 17 | **Is the migration runner connecting over a stable, encrypted link with TCP keepalives configured?** | Check `pg_stat_ssl` to verify TLS. A silent TCP drop (from a NAT gateway, ALB idle timeout, or network blip) during a long index build aborts the operation entirely. Set keepalives: `keepalives=1 keepalives_idle=60 keepalives_interval=10 keepalives_count=5 sslmode=require` in the connection string. SQLAlchemy: `connect_args={"keepalives": 1, "keepalives_idle": 60, "keepalives_interval": 10, "keepalives_count": 5}`. |
| 18 | **Are sequences aligned if moving data to a new table?** | After copying data, sync: `SELECT setval('table_id_seq', (SELECT MAX(id) FROM table))`. Skipping this causes `Duplicate Key` errors on the first insert after read cutover |
| 19 | **Is `statement_timeout` set on the migration runner connection (not just `lock_timeout`)?** | `lock_timeout` prevents waiting for a lock. `statement_timeout` prevents hanging if a deadlock or runaway query occurs mid-migration. Set both: `SET statement_timeout = '2h'; SET lock_timeout = '5s';` |
| 20 | **On AWS RDS / Aurora: is your I/O credit budget sufficient for the full migration duration?** | Applies to **gp2 volumes only** (gp3, io1, io2 have consistent IOPS — this check does not apply). gp2 volumes earn credits at 3 IOPS/GB of volume size per second as baseline. A 500GB gp2 volume earns 1,500 baseline IOPS/s. If your migration requires 3,000 IOPS/s sustained, it depletes credits twice as fast as it earns them. **Rough check:** open CloudWatch → RDS → your instance → `BurstBalance`. If `BurstBalance(%) × 5.4 million credits ÷ (required_iops − baseline_iops) < migration_hours`: switch to gp3 or provision io1 before starting. The 50% heuristic used previously was not calibrated — use the calculation. |

---

## Further Reading

- [PostgreSQL — ALTER TABLE](https://www.postgresql.org/docs/current/ddl-alter.html)
- [PostgreSQL — CREATE INDEX CONCURRENTLY](https://www.postgresql.org/docs/current/sql-createindex.html#SQL-CREATEINDEX-CONCURRENTLY)
- [PostgreSQL — Lock Monitoring](https://www.postgresql.org/docs/current/monitoring-locks.html)
- [PostgreSQL — NOT VALID Constraints](https://www.postgresql.org/docs/current/sql-altertable.html)
- [PostgreSQL — Routine Vacuuming](https://www.postgresql.org/docs/current/routine-vacuuming.html)
- [PostgreSQL — Preventing Transaction ID Wraparound](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-WRAPAROUND)
- [MySQL — Online DDL Operations](https://dev.mysql.com/doc/refman/8.0/en/innodb-online-ddl-operations.html)
- [Flyway Documentation](https://flywaydb.org/documentation)
- [Liquibase Documentation](https://docs.liquibase.com)
- [Mongock Documentation](https://mongock.io/docs)
- [Confluent Schema Registry — Schema Evolution](https://docs.confluent.io/platform/current/schema-registry/fundamentals/schema-evolution.html)
- [Stripe Engineering — Online Migrations at Scale](https://stripe.com/blog/online-migrations) *(if this URL has moved, search "Stripe online migrations at scale" — it is frequently cited and easily findable)* *(if this URL has moved, search "Stripe online migrations at scale" — the article is frequently cited and easily findable)*
- [AWS Database Migration Service](https://aws.amazon.com/dms/)
- [gh-ost — GitHub's Online Schema Change for MySQL](https://github.com/github/gh-ost)
- [PostgreSQL — ALTER TYPE (ENUM)](https://www.postgresql.org/docs/current/sql-altertype.html)
- [PostgreSQL — autovacuum Configuration](https://www.postgresql.org/docs/current/runtime-config-autovacuum.html)
- [PostgreSQL — JSON Functions and Operators](https://www.postgresql.org/docs/current/functions-json.html)
