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
> | 🟡 **Mid-level engineer?** | Sections 2–13 are your core reference. |
> | 🔴 **Senior / Tech Lead?** | Sections 14–21 are where the advanced nuance lives. |

---

## ⚡ Key Points — Read This First

Before diving into any section, internalise these 12 principles. Every pattern in this document flows from them. If you read nothing else, read this table.

> **🔑 The 12 Laws of Safe Migration**
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

**Systems & Infrastructure**
14. [Versioned Migration System](#14-versioned-migration-system)
15. [Central Migration Registry](#15-central-migration-registry)
16. [Migration Runner](#16-migration-runner)
17. [Infrastructure-Level Migrations](#17-infrastructure-level-migrations)

**Tooling**
18. [Migration Tooling — The Full Landscape](#18-migration-tooling--the-full-landscape)

**Reference**
19. [Migration Runbook Template](#19-migration-runbook-template)
20. [What Goes Wrong — Four Patterns From Production](#20-what-goes-wrong--four-patterns-from-production)
21. [Quick Reference — Pre-Migration Checklist](#21-quick-reference--pre-migration-checklist)

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

-- Why NULL and not NOT NULL?
-- NOT NULL with DEFAULT NOW() would force a full row rewrite on older PostgreSQL versions.
-- NULL means "this column can be empty" — existing rows simply have NULL here.
```

At the same time, update the application to **write to both the old and new columns** on every relevant operation (the Dual-Write pattern — covered fully in [Section 4](#4-dual-write-strategy-for-critical-data)):

```python
# After deploying Phase 1, new writes populate both columns
def verify_user(user_id):
    db.execute("""
        UPDATE users
        SET is_verified = TRUE,    -- old column — keep writing for now
            verified_at = NOW()    -- new column — start populating
        WHERE id = %s
    """, (user_id,))
```

> **💡 Tip:** Deploy Phase 1 and let it run for at least 24 hours before moving to Phase 2. You want new writes to have been populating the new column long enough to validate the behaviour before starting the backfill.

---

#### 🟡 Phase 2: MIGRATE — Backfill Existing Rows

Existing rows still have `NULL` in the new column. Run a **background job** to populate them without downtime. See [Section 5](#5-backfill-patterns-without-locking-tables) for the full safe batched backfill pattern.

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

**The hidden cost of incremental:** Teams that rush leave migrations in **"Phase 2 limbo"** — the backfill ran but the contract phase never shipped, leaving two columns with diverging data coexisting in the schema indefinitely. Put a migration-completion ticket in every sprint until the full cycle is done.

---

## 4. Dual-Write Strategy for Critical Data

### What Is Dual-Write?

During a migration, there is a period when your old schema and your new schema must both stay in sync. **Dual-write** keeps them aligned: every write goes to both the old location and the new location simultaneously.

Think of it like forwarding your mail when you move house. During the transition, you tell the post office to deliver to both addresses so nothing gets lost while you are still moving boxes.

> **📘 Real Example: An e-commerce order service migrating how it stores delivery addresses**
>
> **Old design:** A single `delivery_address` text column — a free-form string like `"42 Elm Street, London, UK"`.
>
> **New design:** Three structured columns — `delivery_street`, `delivery_city`, `delivery_country` — enabling fast queries like "show all orders in Germany" without parsing free text.
>
> With 8 million existing orders in the old format, the dual-write solution:
>
> **Step 1 (Expand):** Add the three new nullable columns. Deploy code that writes to all four:
> ```python
> def place_order(order):
>     db.execute("""
>         INSERT INTO orders (
>             delivery_address,    -- old column — keep writing
>             delivery_street, delivery_city, delivery_country
>         ) VALUES (%s, %s, %s, %s)
>     """, (order.full_address, order.street, order.city, order.country))
> ```
>
> **Step 2 (Backfill):** Background job parses `delivery_address` for all 8 million existing rows.
>
> **Step 3 (Read cutover):** After divergence check passes, switch app to read from new columns. Old column still written as safety net.
>
> **Step 4 (Contract):** After a week of clean reads, drop `delivery_address`.

### Controlling Dual-Write With Feature Flags

Feature flags let you control dual-write without a code deployment — ramp from 5% → 50% → 100% of writes, or kill it instantly if something looks wrong.

```sql
-- A simple feature flags table — no external tool needed to start
CREATE TABLE feature_flags (
    flag_name   VARCHAR(100) PRIMARY KEY,
    is_enabled  BOOLEAN      DEFAULT FALSE,
    description TEXT,
    updated_at  TIMESTAMP    DEFAULT NOW()
);

INSERT INTO feature_flags (flag_name, is_enabled, description) VALUES
    ('dual_write_order_address_v2', FALSE, 'Write to new delivery address columns'),
    ('read_from_order_address_v2',  FALSE, 'Read from new delivery address columns');

-- Toggle without a deployment:
UPDATE feature_flags SET is_enabled = TRUE
WHERE flag_name = 'dual_write_order_address_v2';
```

```python
def is_feature_enabled(flag_name: str) -> bool:
    result = db.execute(
        "SELECT is_enabled FROM feature_flags WHERE flag_name = %s", (flag_name,)
    ).fetchone()
    return bool(result["is_enabled"]) if result else False

class OrderRepository:
    def save_order(self, order):
        self._write_legacy_address(order)                        # always write old
        if is_feature_enabled("dual_write_order_address_v2"):
            self._write_new_address_columns(order)               # write new when flag is on

    def get_order(self, order_id):
        if is_feature_enabled("read_from_order_address_v2"):
            return self._read_new_address_columns(order_id)      # read new when verified
        return self._read_legacy_address(order_id)               # default: old
```

> **💡 Tip:** A database flag table is completely sufficient for migration feature flags. The goal is to flip a switch without a deployment — a `UPDATE feature_flags SET is_enabled = TRUE` achieves exactly that.

For teams that grow beyond this, dedicated tools offer targeting rules, audit logs, and percentage rollouts:

| Tool | Type | When to Graduate To It |
|---|---|---|
| **LaunchDarkly** | Managed SaaS | Large teams needing real-time targeting and rollout controls |
| **Unleash** | Open source, self-hosted | Full control with no vendor dependency |
| **Flagsmith** | Open source or managed | Easy self-hosting; good middle ground |
| **AWS AppConfig** | Managed | Teams already on AWS wanting zero extra infrastructure |

### The Four States of a Dual-Write Migration

Move through these states strictly in order. **Never skip ahead.**

| State | Writes To | Reads From | When You Are Here |
|---|---|---|---|
| **1** | OLD only | OLD only | Before migration starts — normal operation |
| **2** | OLD + NEW | OLD only | After Expand phase — new columns being populated |
| **3** | OLD + NEW | NEW only | After backfill verified — new columns are source of truth |
| **4** | NEW only | NEW only | After Contract phase — migration complete |

### Consistency During Dual-Write — What Can Actually Go Wrong

Between the write to the old location and the write to the new location, there is a brief window where both stores have different data. In most cases this is milliseconds and harmless. But depending on your data type, it can cost real money and real users.

> **📘 Scenario 1: Bank balance — the inconsistency that costs real money**
>
> Old schema: `balance` column. New schema: `account_ledger` table with a running total. During dual-write:
>
> - `balance` updated to £1,500 (£500 withdrawal from £2,000)
> - Milliseconds later: a second concurrent request reads from `account_ledger` — which still shows £2,000
> - Second £500 withdrawal approved against the stale £2,000
>
> Result: Two £500 withdrawals went through. Account is now £1,000 but should be £1,500.
>
> **Fix:** Wrap both writes in one transaction so they are atomic:
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

> **📘 Scenario 2: Session migration — the inconsistency that logs users out**
>
> Migrating sessions from a `sessions` table to Redis. A user logs in — session written to both. Their next request hits a different app server with a 200ms Redis replication lag. Redis returns "session not found". User is logged out immediately after logging in. No errors in the logs. The code is correct. The problem is a consistency window created by replication lag.
>
> **Fix:** Fall back to the old location if the new one misses:
> ```python
> def get_session(session_id: str):
>     session = redis.get(session_id)             # try new location first
>     if session is None:
>         session = db.query_session(session_id)  # fall back to old
>     return session
> ```

> **📘 Scenario 3: Cache serving stale schema — the silent wrong answer**
>
> Migrating `product_price` to a new `pricing` table. Redis cache was populated before migration. New code reads from `pricing` table — but cache intercepts first, returning the old price. Cache TTL is 1 hour. For up to 1 hour after cutover, users see wrong prices. No error thrown. No alert fires.
>
> **Fix:** Invalidate the cache on every write, regardless of which path is primary:
> ```python
> def update_price(product_id: int, new_price: float):
>     db.execute("UPDATE products SET product_price = %s WHERE id = %s", (new_price, product_id))
>     db.execute(
>         "INSERT INTO pricing (product_id, price) VALUES (%s, %s) "
>         "ON CONFLICT (product_id) DO UPDATE SET price = EXCLUDED.price",
>         (product_id, new_price)
>     )
>     cache.delete(f"product:{product_id}")    # invalidate on EVERY write
> ```

### What To Monitor During Dual-Write

| Metric | Why It Matters | Alert Threshold |
|---|---|---|
| Write success rate (new location) | A drop means new schema is rejecting writes | < 99.9% |
| Divergence rate (old vs new disagree) | Any divergence is data corruption | > 0% |
| Read fallback rate | How often new location misses and old is used | > 1% |
| Cache hit rate | Sudden drop may indicate broken cache invalidation | Sudden change |
| Replication lag | Lag in new target creates inconsistency windows | > 500ms |

---

## 5. Backfill Patterns Without Locking Tables

A backfill populates data in the new column or table for rows that **existed before the migration started**. New writes are handled by dual-write. Old rows need separate treatment.

> **🚨 Never Do This on a Large Live Table:**
> ```sql
> -- This will lock the table for minutes on large datasets
> UPDATE users SET verified_at = created_at WHERE verified_at IS NULL;
> ```
> On 50M rows this locks the table, spikes CPU, saturates replication lag, and causes an outage.

### The Safe Pattern: Batch + Throttle + Cursor

Process rows in small batches with a brief pause between each. This keeps individual transactions short and gives the database — and its replicas — time to breathe.

```python
import time

def run_backfill(db):
    BATCH_SIZE = 1_000    # rows per batch — start small, increase if DB stays healthy
    SLEEP_SECS = 0.05     # 50ms pause between batches
    last_id    = 0
    total_done = 0

    while True:
        # Keyset pagination — always resumes exactly where we left off
        rows = db.execute("""
            SELECT id FROM users
            WHERE verified_at IS NULL
              AND id > %(last_id)s
            ORDER BY id ASC
            LIMIT %(batch_size)s
        """, {"last_id": last_id, "batch_size": BATCH_SIZE}).fetchall()

        if not rows:
            print(f"Backfill complete — {total_done:,} rows updated.")
            break

        ids = [row["id"] for row in rows]

        result = db.execute("""
            UPDATE users
            SET verified_at = created_at
            WHERE id = ANY(%(ids)s)
              AND verified_at IS NULL    -- idempotency guard: skip already-done rows
        """, {"ids": ids})

        total_done += result.rowcount
        last_id     = ids[-1]
        time.sleep(SLEEP_SECS)
        print(f"  {total_done:,} rows done, last id = {last_id}")
```

> **📘 Why keyset pagination (`id > last_id`) instead of `OFFSET`?**
>
> `LIMIT 1000 OFFSET 5000` has a subtle bug: as the backfill updates rows, the `OFFSET` calculation shifts — you skip rows or re-process them. `id > last_id` always resumes from exactly the last row processed regardless of what happened behind it. It is also significantly faster because PostgreSQL satisfies it using the primary key index rather than scanning and discarding rows.

### Tuning the Backfill

Start at the conservative settings. Increase only if monitoring shows the database remains healthy (CPU < 70%, replication lag < 1s).

| Setting | Conservative (Start Here) | Moderate | Aggressive |
|---|---|---|---|
| Batch size | 500 | 1,000–5,000 | 10,000+ |
| Sleep between batches | 100ms | 25–50ms | 0–10ms |
| Run during | Off-peak hours | Any time | Any time |
| Replication lag tolerance | < 1s | < 5s | < 30s |

### Tracking Backfill Progress

```sql
CREATE TABLE backfill_jobs (
    job_name          VARCHAR(100) PRIMARY KEY,
    started_at        TIMESTAMP,
    last_processed_id BIGINT  DEFAULT 0,
    total_processed   BIGINT  DEFAULT 0,
    status            VARCHAR(20) DEFAULT 'running',  -- running | paused | complete
    updated_at        TIMESTAMP   DEFAULT NOW()
);

-- Check percentage complete
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

An idempotent migration produces **the same result whether it runs once or ten times**. If it runs, crashes, and runs again — nothing breaks. No duplicate data. No constraint violations. No errors on re-run.

This is non-negotiable. Migrations fail in production. Network timeouts happen. Deployment systems restart mid-run. A non-idempotent migration that crashes halfway can leave the database in a state where the re-run fails with a *different* error — turning a recoverable failure into a crisis requiring manual intervention.

> **📘 Real Example: Non-idempotent vs idempotent**
>
> **Non-idempotent — dangerous:**
> ```sql
> -- If this crashes after inserting 50K rows and you re-run it,
> -- every already-inserted row fails with a duplicate key violation.
> INSERT INTO user_preferences (user_id, default_theme)
> SELECT id, 'light' FROM users;
> ```
>
> **Idempotent — safe to re-run:**
> ```sql
> -- ON CONFLICT DO NOTHING skips rows that already exist
> INSERT INTO user_preferences (user_id, default_theme)
> SELECT id, 'light' FROM users
> ON CONFLICT (user_id) DO NOTHING;
> ```

### Making Every Migration Type Idempotent

**Table creation:**
```sql
-- ❌ Fails if table already exists
CREATE TABLE subscriptions (...);

-- ✅ Safe to run multiple times
CREATE TABLE IF NOT EXISTS subscriptions (...);
```

**Column addition (PostgreSQL):**
```sql
-- ❌ Fails if column already exists
ALTER TABLE users ADD COLUMN verified_at TIMESTAMP NULL;

-- ✅ Guard with information_schema check
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

**Column addition (MySQL):**
```sql
-- ✅ MySQL 8.0+ supports IF NOT EXISTS directly
ALTER TABLE users ADD COLUMN IF NOT EXISTS verified_at DATETIME NULL;
```

**Index creation:**
```sql
-- ✅ PostgreSQL — idempotent and non-locking
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email ON users(email);

-- ✅ MySQL — idempotent with non-locking algorithm
ALTER TABLE users
ADD INDEX IF NOT EXISTS idx_users_email (email)
ALGORITHM=INPLACE, LOCK=NONE;
```

**Data migration / backfill:**
```sql
-- ❌ Re-runs update rows already processed (wasted work, potential side effects)
UPDATE users SET verified_at = created_at;

-- ✅ Skips already-processed rows
UPDATE users SET verified_at = created_at WHERE verified_at IS NULL;
```

**Constraint addition (PostgreSQL):**
```sql
-- ✅ Idempotent + safe (NOT VALID avoids the full-table scan lock — see Section 10)
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
-- Follow with separately: ALTER TABLE orders VALIDATE CONSTRAINT fk_user;
```

### Why You Cannot Rely Solely on the Migration Runner

Migration runners track applied migrations in a history table. But this protection breaks down when:

- A migration **partially ran** and the runner crashed — it is not in the history table, so it re-runs from the beginning
- The **history table was manually edited or lost**
- Two nodes **race** to apply the same migration simultaneously (before advisory locking fires)

Write every migration as if the runner might execute it twice. Defense in depth.

---

## 7. Rollback Planning

> **🚨 The Most Important Rule in This Document**
>
> If you cannot describe the rollback procedure **before** starting a migration, **you are not ready to run the migration.**
>
> Rollback planning is done before the migration runs — not after something breaks.

### The Three Levels of Rollback

#### ✅ Level 1: Application Rollback — Always Try This First

Roll the application code back to the previous version. The schema change stays in the database but is simply unused.

Safe during Phase 1 and Phase 2 because the new column is nullable — old code ignores it. No data corruption.

```bash
kubectl rollout undo deployment/api-server
kubectl rollout status deployment/api-server
```

**Target time: under 2 minutes.** This should be your default first response for any migration-related incident in the first 24–48 hours after any phase.

---

#### ⚠️ Level 2: Schema Rollback — Use With Caution

Reverse the schema change with a new migration script. Safe only for Phase 1 rollbacks where no data has been written in the new format.

```sql
-- Safe for Phase 1 only — column is empty, nothing reads it yet
ALTER TABLE users DROP COLUMN verified_at;
```

> **⚠️ Watch Out:** Before running a schema rollback, confirm exactly which application version is deployed and whether any pod reads the new column. Dropping a column that live code depends on is worse than the original problem.

---

#### 🚨 Level 3: Point-In-Time Restore — Last Resort Only

Restore from a snapshot taken before the migration. **You lose every row written since the snapshot.** For a busy system, that can be thousands of transactions.

Use only when Levels 1 and 2 are not viable. If Level 3 is your *primary* rollback plan, the migration was not designed safely.

---

### Rollback Plan Template

```
## Rollback Plan: [Migration Name]

Snapshot taken at:      ___________     Snapshot ID:  ___________

# Level 1 — Application Rollback
Previous image tag:         ___________
Rollback command:           kubectl rollout undo deployment/api-server
Estimated time:             < 2 minutes
Safe to use until:          ___________  (when does new-format data make this unsafe?)

# Level 2 — Schema Rollback
Rollback script:            migrations/rollback/V5__rollback.sql
Rows affected estimate:     ___________
Lock risk:                  ___________
Dependencies to roll back:  ___________

# Level 3 — Point-In-Time Restore
Snapshot ID:                ___________
Estimated restore time:     ___________
Data loss window:           ___________

# Trigger thresholds — fill in real numbers, never leave these blank
Auto-rollback trigger:    Error rate > 2% for 3 consecutive minutes
Manual rollback trigger:  Replication lag > 30s and not recovering
Manual rollback trigger:  Migration not complete within 8 hours
```

---

## 8. Index Migrations

Indexes are among the most dangerous schema operations on a live system. An improperly run index creation on a large table can hold a full table lock for 10–30 minutes — blocking every read and write the entire time.

### The Locking Problem

```sql
-- ❌ DANGEROUS on any large table with live traffic
-- Acquires AccessExclusiveLock — blocks ALL reads and writes until complete
CREATE INDEX idx_orders_user_id ON orders(user_id);
-- On 100M rows: may block all queries for 10–30 minutes
```

### PostgreSQL: Use `CONCURRENTLY`

```sql
-- ✅ SAFE: builds index in the background
-- Reads and writes continue uninterrupted throughout
CREATE INDEX CONCURRENTLY idx_orders_user_id ON orders(user_id);
```

`CONCURRENTLY` builds the index in multiple passes, tracking concurrent changes. It takes 2–3× longer than a normal build but never blocks live traffic.

**Three important constraints:**

- **Cannot run inside a transaction block.** Must be a standalone statement. Running inside `BEGIN ... COMMIT` causes an immediate error.
- **If it fails mid-build, it leaves an `INVALID` index.** Detect and clean up before retrying.
- **Only one `CONCURRENTLY` build per table at a time.** A second concurrent build on the same table waits.

```sql
-- Detect INVALID indexes left by a failed concurrent build
SELECT i.indexrelid::regclass AS index_name, i.indrelid::regclass AS table_name
FROM pg_index i
WHERE i.indisvalid = FALSE;

-- Clean up and retry
DROP INDEX CONCURRENTLY idx_orders_user_id;
CREATE INDEX CONCURRENTLY idx_orders_user_id ON orders(user_id);
```

### Dropping Indexes Safely

```sql
-- ❌ Holds a lock that blocks writes until the drop completes
DROP INDEX idx_orders_user_id;

-- ✅ Non-blocking
DROP INDEX CONCURRENTLY idx_orders_user_id;
```

### Estimating Build Time Before Running

```sql
-- Check table size and estimated row count
SELECT
    pg_size_pretty(pg_total_relation_size('orders')) AS total_size,
    pg_size_pretty(pg_relation_size('orders'))       AS table_size,
    reltuples::BIGINT                                AS estimated_rows
FROM pg_class
WHERE relname = 'orders';
```

**General guideline:** PostgreSQL indexes roughly 10–50 million rows per minute with `CONCURRENTLY`, depending on hardware and write load. Always verify on staging with production-scale data first.

### MySQL: Use `ALGORITHM=INPLACE, LOCK=NONE`

Since MySQL 5.6, InnoDB Online DDL supports adding indexes without locking reads or writes:

```sql
-- ✅ MySQL: non-locking index creation
ALTER TABLE orders
ADD INDEX idx_orders_user_id (user_id)
ALGORITHM=INPLACE, LOCK=NONE;
```

> **⚠️ Watch Out:** MySQL silently falls back to `ALGORITHM=COPY` (full table lock) if conditions are not met (e.g., the index type is not supported for online DDL). Add `ALGORITHM=INPLACE` explicitly so MySQL returns an error instead of silently locking — that forces you to choose deliberately.

### Composite Index Column Order

The leftmost column must appear in the `WHERE` clause for the index to be used:

```sql
-- Helps: WHERE user_id = X AND created_at > Y
-- Helps: WHERE user_id = X  (leftmost alone)
-- Does NOT help: WHERE created_at > Y  (skips leftmost column)
CREATE INDEX CONCURRENTLY idx_orders_user_created ON orders(user_id, created_at);
```

### Partial Indexes (PostgreSQL)

Index only the subset of rows queried most often — smaller, faster, less disk:

```sql
-- Only index ~5M active orders out of 100M total
CREATE INDEX CONCURRENTLY idx_orders_active_user
ON orders(user_id)
WHERE status = 'active';
```

---

## 9. Long Transactions

### What Is a Long Transaction and Why Is It Dangerous?

A long transaction is any database transaction that remains open — uncommitted or not yet rolled back — for an extended period. In PostgreSQL and MySQL alike, long transactions cause cascading problems that worsen over time.

**Harm 1 — Locks are held for the full duration.**
Any row-level or table-level lock acquired inside the transaction is held until it commits or rolls back. A migration that wraps DDL and a large data update in one transaction holds the DDL lock for the entire duration of the data operation.

**Harm 2 — VACUUM cannot reclaim dead rows (PostgreSQL).**
Every `UPDATE` creates a new row version; the old version becomes a dead row. VACUUM reclaims dead rows — but only if no open transaction could still see them. A transaction open for hours prevents VACUUM from cleaning up, causing table bloat that slows every subsequent query.

**Harm 3 — WAL cannot be truncated (PostgreSQL).**
PostgreSQL must retain all Write-Ahead Log entries from before the oldest open transaction. A long-running transaction forces WAL retention, which can fill disk on the primary.

**Harm 4 — Replication slot lag.**
If you use logical replication (e.g., Debezium for CDC), the replication slot cannot advance past a long-running transaction on the primary, causing replica lag and potential disk fill.

> **📘 Real Example: DDL + backfill in one transaction — 45-minute freeze**
>
> ```sql
> BEGIN;
> ALTER TABLE orders ADD COLUMN shipped_at TIMESTAMP NULL;
> -- ALTER TABLE acquired AccessExclusiveLock
>
> -- 10M-row backfill runs inside the same transaction
> UPDATE orders SET shipped_at = created_at WHERE status = 'shipped';
> -- Ran for 45 minutes — holding the DDL lock the entire time
> COMMIT;
> ```
>
> Every query to the `orders` table was blocked for 45 minutes.
>
> **Fix:** Separate DDL from long data operations:
> ```sql
> -- Transaction 1: fast DDL — lock held for milliseconds
> ALTER TABLE orders ADD COLUMN shipped_at TIMESTAMP NULL;
>
> -- Outside any transaction: batched backfill (see Section 5)
> -- No DDL lock held during data work
> UPDATE orders SET shipped_at = created_at
> WHERE status = 'shipped' AND id > %(last_id)s
> LIMIT 1000;
> ```

### Detecting Long Transactions

```sql
-- PostgreSQL: transactions open for more than 5 minutes
SELECT
    pid,
    now() - xact_start          AS transaction_age,
    now() - query_start         AS current_query_age,
    state,
    wait_event_type,
    wait_event,
    LEFT(query, 120)            AS query_preview
FROM pg_stat_activity
WHERE xact_start IS NOT NULL
  AND (now() - xact_start) > INTERVAL '5 minutes'
ORDER BY transaction_age DESC;
```

```sql
-- MySQL: transactions open for more than 5 minutes
SELECT
    trx_id,
    trx_started,
    TIMESTAMPDIFF(SECOND, trx_started, NOW()) AS seconds_open,
    trx_query
FROM information_schema.innodb_trx
WHERE TIMESTAMPDIFF(SECOND, trx_started, NOW()) > 300
ORDER BY trx_started ASC;
```

### Terminating a Long Transaction

```sql
-- PostgreSQL: graceful cancel (rolls back transaction)
SELECT pg_cancel_backend(pid);

-- PostgreSQL: force terminate the connection
SELECT pg_terminate_backend(pid);

-- MySQL: cancel the current query (keeps connection)
KILL QUERY <thread_id>;

-- MySQL: terminate the connection entirely
KILL <thread_id>;
```

### Setting Timeouts as a Safety Net

```sql
-- PostgreSQL: set timeouts on the migration connection
-- Prevents any single statement from running unbounded
SET statement_timeout = '30s';

-- Terminates a connection that has been idle inside a transaction for 10+ minutes
SET idle_in_transaction_session_timeout = '10min';
```

```python
# psycopg2 (Python): set timeouts at connection level
# statement_timeout: milliseconds | idle_in_transaction_session_timeout: milliseconds
conn = psycopg2.connect(
    dsn=DATABASE_URL,
    options=(
        "-c statement_timeout=30000 "            # 30 seconds
        "-c idle_in_transaction_session_timeout=600000"  # 10 minutes
    )
)
```

```sql
-- MySQL equivalent: set lock wait timeout for this session
SET SESSION innodb_lock_wait_timeout = 10;  -- seconds before lock acquisition fails
```

---

## 10. Foreign Key Validation

### The Hidden Cost

Adding a foreign key to an existing table is one of the most dangerous operations you can run without preparation. When you add a foreign key normally, the database must **validate every existing row** — confirming each foreign key value references a real row in the parent table.

On a table with 80 million rows, this validation scan can run for tens of minutes, holding a lock on **both** tables the entire time.

> **📘 Real Example: A 40-minute outage from one FK**
>
> ```sql
> -- This looks like a two-second operation. It is not.
> ALTER TABLE comments
> ADD CONSTRAINT fk_comment_user
> FOREIGN KEY (user_id) REFERENCES users(id);
> ```
>
> `comments` has 80M rows. Validation scanned every row for 40 minutes. Inserts and updates to `comments` were blocked for the entire duration. Comment posting was unavailable for 40 minutes.

### PostgreSQL: `NOT VALID` + `VALIDATE CONSTRAINT`

Split into two steps — separate the cheap constraint declaration from the expensive validation scan:

```sql
-- Step 1: Declare constraint WITHOUT scanning existing rows
-- Fast — brief lock to register the constraint in the catalog
-- All NEW rows are enforced immediately; existing rows deferred
ALTER TABLE comments
ADD CONSTRAINT fk_comment_user
FOREIGN KEY (user_id) REFERENCES users(id)
NOT VALID;

-- Step 2: Validate existing rows — at a low-traffic time
-- Uses ShareUpdateExclusiveLock — much weaker; allows concurrent reads and DML
ALTER TABLE comments VALIDATE CONSTRAINT fk_comment_user;
```

| Approach | Lock Acquired | Blocks |
|---|---|---|
| `ADD CONSTRAINT FK` (normal) | `AccessExclusiveLock` on both tables | ALL reads and writes for full scan duration |
| `ADD CONSTRAINT ... NOT VALID` | `ShareRowExclusiveLock` | DDL only — reads and DML continue |
| `VALIDATE CONSTRAINT` | `ShareUpdateExclusiveLock` | Concurrent DDL only |

**Same pattern works for `CHECK` constraints:**
```sql
ALTER TABLE orders ADD CONSTRAINT chk_positive_amount CHECK (amount > 0) NOT VALID;
ALTER TABLE orders VALIDATE CONSTRAINT chk_positive_amount;
```

### MySQL: Use `ALGORITHM=INPLACE, LOCK=NONE` Where Supported

MySQL does not have a direct `NOT VALID` equivalent. For InnoDB when the referenced column already has a compatible index, use Online DDL:

```sql
-- MySQL: add FK with minimal locking when supported
ALTER TABLE comments
ADD CONSTRAINT fk_comment_user FOREIGN KEY (user_id) REFERENCES users(id),
ALGORITHM=INPLACE, LOCK=NONE;
```

If Online DDL is not supported for the specific operation, MySQL will return an error and you should plan a maintenance window or use an online schema change tool (**gh-ost** or **pt-online-schema-change**) instead.

---

## 11. Replication Impact

### Why It Matters

Most production databases use replication — a primary handles writes, replicas serve reads and act as failover targets. Migrations that generate heavy writes create **replication lag**: replicas fall behind the primary and start serving stale data.

The danger is subtle: **replication lag is invisible at the application layer**. Queries succeed. No errors are thrown. Read-replicas silently return outdated rows — potentially minutes behind the primary.

> **📘 Real Example: Backfill created 8-minute replica lag**
>
> A team ran a batched backfill (1,000 rows per batch, no sleep between batches) on a 60M-row table. The primary handled writes fine. Replicas started falling behind within minutes. After 20 minutes, replica lag reached 8 minutes. The application load-balanced reads across primary and replicas — 70% of users were seeing data 8 minutes old. Missing orders, incorrect balances, absent notification counts. No errors anywhere in the logs.

### How Lag Builds Up

```
Primary: [batch 1][batch 2][batch 3][batch 4]...  → 5,000 rows/sec written
Replica: [batch 1][batch 2]                        → 3,000 rows/sec replayed
                            ↑
                            Lag growing — 2,000 rows/sec falling behind
```

### Measuring Replication Lag

```sql
-- PostgreSQL: on the primary — check lag for all connected replicas
SELECT
    client_addr,
    application_name,
    write_lag,
    flush_lag,
    replay_lag,
    pg_size_pretty(pg_wal_lsn_diff(sent_lsn, replay_lsn)) AS bytes_behind
FROM pg_stat_replication
ORDER BY replay_lag DESC NULLS LAST;

-- PostgreSQL: on a replica — check its own current lag
SELECT now() - pg_last_xact_replay_timestamp() AS replication_lag;
```

```sql
-- MySQL: on a replica (MySQL 8.0.22+)
SHOW REPLICA STATUS\G
-- Look at: Seconds_Behind_Source
-- On older MySQL (<8.0.22): column was named Seconds_Behind_Master
```

### Replication-Aware Backfill

```python
import time

def run_backfill_with_lag_check(db, max_lag_seconds: int = 5):
    BATCH_SIZE = 1_000
    last_id    = 0

    while True:
        # Check replica lag before each batch — pause if replicas are falling behind
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
            SELECT id FROM users
            WHERE verified_at IS NULL AND id > %(last_id)s
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

### Replication Slots and Disk Risk (PostgreSQL)

If you use **logical replication slots** (common with CDC tools like Debezium), long migrations create a disk risk: the slot cannot advance while the migration generates WAL faster than consumers can process it, forcing the primary to retain all that WAL.

```sql
-- Check how much WAL each logical slot is retaining
SELECT
    slot_name,
    active,
    pg_size_pretty(
        pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn)
    ) AS retained_wal
FROM pg_replication_slots
ORDER BY pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn) DESC;
```

If retained WAL grows rapidly during a migration, pause the migration until the slot catches up — or temporarily drop the slot if the consumer can safely replay from a checkpoint.

---

## 12. Disk Space During Migrations

Disk full is one of the few database failures that can be unrecoverable without manual intervention. PostgreSQL with a full disk stops accepting writes entirely. Understanding how migrations consume disk lets you plan ahead.

### Four Sources of Unexpected Disk Usage

**1. Table rewrites.** Some `ALTER TABLE` operations create a full copy of the table on disk before dropping the original. A 200GB table temporarily requires 400GB.

**2. Concurrent index builds.** `CREATE INDEX CONCURRENTLY` creates the new index before anything is dropped. During the build, both the full table data and the growing new index occupy disk simultaneously.

**3. MVCC dead-row bloat (PostgreSQL).** Every `UPDATE` creates a new row version; the old version becomes a dead row. During a heavy backfill, writes arrive faster than autovacuum can clean up. A 100M-row table being heavily updated may temporarily grow 30–50%.

**4. WAL retention.** Every write generates WAL entries. A 100M-row backfill can produce gigabytes of WAL, all retained until replicas have replayed it.

### Estimating Disk Requirements

```sql
-- PostgreSQL: current table and index sizes
SELECT
    pg_size_pretty(pg_total_relation_size('orders')) AS total_size,
    pg_size_pretty(pg_relation_size('orders'))       AS table_size,
    pg_size_pretty(pg_indexes_size('orders'))        AS index_size;

-- Total database size
SELECT pg_size_pretty(pg_database_size(current_database())) AS db_size;
```

**Rules of thumb:**
- Table rewrite operations: ensure **2× table size** free disk
- Adding a new index: ensure **1× estimated index size** free disk
- Heavy backfill: budget **1.5× table size** for temporary MVCC bloat

### Which PostgreSQL Operations Cause a Full Table Rewrite

| Operation | Full Rewrite? | Notes |
|---|---|---|
| `ADD COLUMN ... NULL` | **No** | The safe expand/contract approach |
| `ADD COLUMN ... NOT NULL DEFAULT <constant>` | **No** (PG 11+) | Constant default stored in catalog |
| `ADD COLUMN ... NOT NULL DEFAULT <function>` | **Yes** | Function-computed defaults still rewrite |
| `ALTER COLUMN TYPE` | **Yes** | Always rewrites unless the cast is trivial |
| `SET TABLESPACE` | **Yes** | Physically moves the file |
| `VACUUM FULL` | **Yes** | Creates a new compact copy |
| `CREATE INDEX CONCURRENTLY` | **No** | Builds new index; does not rewrite table data |

### Monitoring Disk and Bloat During a Migration

```sql
-- Monitor live bloat and vacuum status during a backfill
SELECT
    tablename,
    pg_size_pretty(pg_total_relation_size(tablename::regclass)) AS current_size,
    n_dead_tup    AS dead_rows,
    n_live_tup    AS live_rows,
    last_autovacuum,
    last_autoanalyze
FROM pg_stat_user_tables
WHERE tablename = 'orders';
```

### Triggering Manual VACUUM During a Migration

Autovacuum often cannot keep up with a heavy backfill. Run VACUUM manually in a separate session:

```sql
-- Non-locking — runs concurrently with reads and writes
VACUUM ANALYZE orders;

-- VACUUM FULL: only if immediate space reclamation is needed
-- AND you can afford an exclusive lock (maintenance window only)
-- VACUUM FULL orders;
```

---

## 13. Lock Diagnostics

### Why This Section Matters

During any migration, something unexpected may hold a conflicting lock — an analytics query, a long-running transaction, a forgotten `SELECT FOR UPDATE`. Without visibility into what is blocking what, you are flying blind.

The most dangerous scenario is **lock queue cascade**: once your migration is waiting for a lock, every subsequent query needing that table queues behind it. A migration waiting behind a 4-minute analytics query can cause a full application hang within 30 seconds.

> **📘 Real Example: A lock queue that took down the application**
>
> A team ran `ALTER TABLE users ADD COLUMN ...` which needed `AccessExclusiveLock`. An analytics query had been running for 3 minutes and held a `ShareLock`. The `ALTER TABLE` queued behind it. Every new query needing any lock on `users` then queued behind the `ALTER TABLE`. Within 30 seconds, the connection pool was full. The application stopped responding for 4 minutes — caused not by the migration itself but by the lock queue in front of it.

### The `lock_timeout` Safety Net — Set This Before Every Migration

```sql
-- PostgreSQL: if the migration cannot acquire its lock within 5 seconds, fail.
-- Better to fail fast and retry than to queue and cascade.
SET lock_timeout = '5s';
ALTER TABLE users ADD COLUMN verified_at TIMESTAMP NULL;
-- On timeout: ERROR: canceling statement due to lock timeout
-- → find the blocking query, wait for it to finish, then retry
```

### Finding the Blocking Chain (PostgreSQL)

```sql
-- Full lock blocking chain — who is waiting and who is blocking
SELECT
    blocked.pid                  AS blocked_pid,
    LEFT(blocked.query, 80)      AS blocked_query,
    blocked_locks.locktype,
    blocking.pid                 AS blocking_pid,
    LEFT(blocking.query, 80)     AS blocking_query,
    now() - blocked.query_start  AS blocked_for
FROM pg_catalog.pg_locks         blocked_locks
JOIN pg_catalog.pg_stat_activity blocked
    ON  blocked.pid = blocked_locks.pid
JOIN pg_catalog.pg_locks         blocking_locks
    ON  blocking_locks.locktype = blocked_locks.locktype
    AND blocking_locks.relation = blocked_locks.relation
    AND blocking_locks.pid     != blocked_locks.pid
JOIN pg_catalog.pg_stat_activity blocking
    ON  blocking.pid = blocking_locks.pid
WHERE NOT blocked_locks.granted
ORDER BY blocked_for DESC;
```

### All Locks on a Specific Table (PostgreSQL)

```sql
SELECT
    pid,
    mode,
    granted,
    LEFT(query, 80)      AS query,
    now() - query_start  AS held_for
FROM pg_locks l
JOIN pg_stat_activity a USING (pid)
WHERE relation = 'users'::regclass
ORDER BY granted DESC, held_for DESC NULLS LAST;
```

### MySQL: Finding Blocking Queries

```sql
-- MySQL: see all current InnoDB lock waits
SELECT
    r.trx_id                         AS waiting_trx_id,
    r.trx_mysql_thread_id            AS waiting_thread,
    LEFT(r.trx_query, 80)            AS waiting_query,
    b.trx_id                         AS blocking_trx_id,
    b.trx_mysql_thread_id            AS blocking_thread,
    LEFT(b.trx_query, 80)            AS blocking_query
FROM information_schema.innodb_lock_waits w
JOIN information_schema.innodb_trx b ON b.trx_id = w.blocking_trx_id
JOIN information_schema.innodb_trx r ON r.trx_id = w.requesting_trx_id;
```

### Lock Mode Compatibility (PostgreSQL)

Not all locks conflict. Knowing the hierarchy lets you reason about whether a migration blocks live traffic:

| Operation | Lock Acquired | What It Blocks |
|---|---|---|
| `SELECT` | `AccessShareLock` | Only `AccessExclusiveLock` |
| `INSERT` / `UPDATE` / `DELETE` | `RowExclusiveLock` | `Share`, `ShareRowExclusive`, `Exclusive`, `AccessExclusive` |
| `CREATE INDEX CONCURRENTLY` | `ShareUpdateExclusiveLock` | `ShareUpdateExclusive` and stronger |
| `VALIDATE CONSTRAINT` | `ShareUpdateExclusiveLock` | Concurrent DDL only |
| `ALTER TABLE` (most forms) | `AccessExclusiveLock` | **Everything** — all reads and writes |

### Pre-Migration Lock Check

```python
def is_safe_to_migrate(db, table_name: str, max_wait_sec: int = 10) -> bool:
    """Return True if no long-running queries hold significant locks on the table."""
    with db.cursor() as cur:
        cur.execute("""
            SELECT pid, now() - query_start AS held_for, LEFT(query, 80) AS query
            FROM pg_locks l
            JOIN pg_stat_activity a USING (pid)
            WHERE relation = %s::regclass
              AND mode NOT IN ('AccessShareLock')
              AND granted = TRUE
        """, (table_name,))
        holders = cur.fetchall()

    long_holders = [h for h in holders if h["held_for"].total_seconds() > max_wait_sec]
    for h in long_holders:
        print(f"  ⚠️  PID {h['pid']} held lock for {h['held_for']}: {h['query']}")
    return len(long_holders) == 0
```

---

## 14. Versioned Migration System

### What Is It and Why Does It Matter?

A versioned migration system treats every schema change as a **versioned, immutable artifact** — like source code commits. Every change gets a unique version number, lives in version control, and is tracked in the database so you always know exactly what state the schema is in.

Without this, teams accumulate "mystery migrations" — changes applied directly to production by hand, never recorded, impossible to reproduce, invisible in the audit trail.

### File Naming Convention

```
migrations/
├── V1__create_users_table.sql
├── V2__add_email_index.sql
├── V3__add_verified_at_column.sql
├── V4__create_orders_table.sql
├── V5__add_order_status_index.sql
└── rollback/
    ├── V3__rollback.sql
    ├── V4__rollback.sql
    └── V5__rollback.sql
```

- **`V{n}__`** — version number, monotonically increasing, no gaps
- **`{description}`** — snake_case, describes what the migration does
- **`.sql`** — or `.py`, `.java` for programmatic migrations

**The three immutable rules:**
1. Never edit a migration file that has been applied in any environment. Write a new one.
2. Never delete a migration file. The history must be complete and linear.
3. Never apply migrations out of order. `V5` must always follow `V4`.

### Schema History Table

```sql
CREATE TABLE schema_migrations (
    version        VARCHAR(50)  PRIMARY KEY,
    description    TEXT         NOT NULL,
    script         TEXT         NOT NULL,          -- filename
    checksum       VARCHAR(64),                    -- SHA-256 of file contents
    applied_by     VARCHAR(100),
    applied_at     TIMESTAMP    DEFAULT NOW(),
    execution_ms   INTEGER,
    status         VARCHAR(20)  DEFAULT 'success'  -- success | failed
);

-- What version is the schema at right now?
SELECT version, description, applied_at
FROM schema_migrations
ORDER BY applied_at DESC LIMIT 1;

-- Has a specific migration been applied?
SELECT EXISTS (
    SELECT 1 FROM schema_migrations
    WHERE version = 'V5' AND status = 'success'
) AS is_applied;
```

### Checksum Enforcement

A checksum is a cryptographic fingerprint of the migration file. Storing it and re-verifying on every run catches accidental edits to files that have already been applied:

```python
import hashlib

def compute_checksum(filepath: str) -> str:
    with open(filepath, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()

def verify_migration_integrity(db, migrations_dir: str) -> None:
    applied = db.execute(
        "SELECT script, checksum FROM schema_migrations WHERE status = 'success'"
    ).fetchall()

    for row in applied:
        filepath = f"{migrations_dir}/{row['script']}"
        current  = compute_checksum(filepath)
        if current != row["checksum"]:
            raise RuntimeError(
                f"INTEGRITY VIOLATION: {row['script']} was modified after being applied.\n"
                f"  Stored:  {row['checksum'][:16]}...\n"
                f"  Current: {current[:16]}..."
            )

    print(f"✅ {len(applied)} migration checksums verified — all intact.")
```

---

## 15. Central Migration Registry

### What Is It?

In a microservices architecture, each service owns its own database. Without coordination, migrations happen in isolation. Nobody knows which services are mid-migration, which have failed, or whether a cross-service change is creating inconsistencies.

A **Central Migration Registry** is a shared store that every service reports migration status to — giving the platform team a single view across the entire system.

### Why You Need One at Scale

| Without Registry | With Registry |
|---|---|
| No visibility into which services are mid-migration | Dashboard shows every service's current migration state |
| Cross-service dependencies are implicit and unenforced | Dependencies are declared, checked, and blocked if not satisfied |
| A failed migration in Service A silently breaks Service B | Registry detects and alerts on downstream impact |
| No audit trail of who ran what in production | Full history: actor, timestamp, duration, result |

### Registry Schema

```sql
CREATE TABLE migration_registry (
    id               SERIAL       PRIMARY KEY,
    service_name     VARCHAR(100) NOT NULL,
    version          VARCHAR(50)  NOT NULL,
    description      TEXT,
    status           VARCHAR(20)  NOT NULL,  -- pending|running|success|failed|rolled_back
    environment      VARCHAR(20)  NOT NULL,  -- dev|staging|production
    started_at       TIMESTAMP,
    completed_at     TIMESTAMP,
    duration_ms      INTEGER,
    applied_by       VARCHAR(100),
    rollback_version VARCHAR(50),
    notes            TEXT,
    UNIQUE (service_name, version, environment)
);

CREATE TABLE migration_dependencies (
    service_name       VARCHAR(100) NOT NULL,
    version            VARCHAR(50)  NOT NULL,
    depends_on_service VARCHAR(100) NOT NULL,
    depends_on_version VARCHAR(50)  NOT NULL,
    PRIMARY KEY (service_name, version, depends_on_service, depends_on_version)
);
```

### Reporting From a Service

```python
import requests
from datetime import datetime, timezone

class MigrationRegistry:
    def __init__(self, registry_url: str, service_name: str, environment: str):
        self.url     = registry_url
        self.service = service_name
        self.env     = environment

    def _now(self) -> str:
        return datetime.now(timezone.utc).isoformat()

    def report_started(self, version: str, description: str, applied_by: str) -> None:
        requests.post(f"{self.url}/migrations", json={
            "service_name": self.service,
            "version": version,
            "description": description,
            "status": "running",
            "environment": self.env,
            "applied_by": applied_by,
            "started_at": self._now(),
        })

    def report_completed(self, version: str, duration_ms: int) -> None:
        requests.patch(f"{self.url}/migrations/{self.service}/{version}", json={
            "status": "success",
            "duration_ms": duration_ms,
            "completed_at": self._now(),
        })

    def report_failed(self, version: str, error: str) -> None:
        requests.patch(f"{self.url}/migrations/{self.service}/{version}", json={
            "status": "failed",
            "notes": error,
            "completed_at": self._now(),
        })

# Usage
registry = MigrationRegistry("https://platform-registry.internal", "order-service", "production")
registry.report_started("V12", "Add shipped_at column", applied_by="gugan")
try:
    run_migration("V12__add_shipped_at.sql")
    registry.report_completed("V12", duration_ms=450)
except Exception as e:
    registry.report_failed("V12", str(e))
    raise
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
                f"requires {dep['depends_on_service']} {dep['depends_on_version']} "
                f"to complete in production first."
            )
```

---

## 16. Migration Runner

### What Is a Migration Runner?

A migration runner executes migrations — reads pending scripts, applies them in version order, records results, and halts on failure. It is the bridge between your migration files and your database.

Most migration tools bundle a runner (Flyway, Liquibase, Alembic). Understanding what a runner must guarantee — and building a minimal one — demystifies the tooling and helps you handle edge cases safely.

### What Every Runner Must Guarantee

1. **Mutual exclusion** — if two deployments start simultaneously, only one runs migrations; the other waits.
2. **History awareness** — reads the history table to determine which migrations are pending.
3. **Checksum verification** — re-validates checksums of all previously applied migrations.
4. **Ordered application** — applies pending migrations strictly in version order.
5. **Stop on failure** — rolls back the current transaction and halts immediately; never skips a failed migration.

### Minimal Runner (Python + PostgreSQL)

```python
import os
import hashlib
import time
import psycopg2
import psycopg2.extras

class MigrationRunner:
    ADVISORY_LOCK_KEY = 9876543210  # arbitrary unique bigint

    def __init__(self, db_url: str, migrations_dir: str):
        self.conn = psycopg2.connect(db_url, cursor_factory=psycopg2.extras.DictCursor)
        self.dir  = migrations_dir
        self._ensure_history_table()

    def _ensure_history_table(self) -> None:
        with self.conn.cursor() as cur:
            cur.execute("""
                CREATE TABLE IF NOT EXISTS schema_migrations (
                    version     VARCHAR(50) PRIMARY KEY,
                    description TEXT,
                    checksum    VARCHAR(64),
                    applied_at  TIMESTAMP DEFAULT NOW(),
                    duration_ms INTEGER,
                    status      VARCHAR(20) DEFAULT 'success'
                )
            """)
        self.conn.commit()

    def _acquire_lock(self) -> None:
        with self.conn.cursor() as cur:
            cur.execute("SELECT pg_try_advisory_lock(%s)", (self.ADVISORY_LOCK_KEY,))
            if not cur.fetchone()[0]:
                raise RuntimeError(
                    "Another migration runner is already active. Aborting."
                )

    def _release_lock(self) -> None:
        with self.conn.cursor() as cur:
            cur.execute("SELECT pg_advisory_unlock(%s)", (self.ADVISORY_LOCK_KEY,))

    def _applied_versions(self) -> set:
        with self.conn.cursor() as cur:
            cur.execute("SELECT version FROM schema_migrations WHERE status = 'success'")
            return {row["version"] for row in cur.fetchall()}

    def _checksum(self, path: str) -> str:
        with open(path, "rb") as f:
            return hashlib.sha256(f.read()).hexdigest()

    def _pending_files(self) -> list:
        applied   = self._applied_versions()
        all_files = sorted(
            f for f in os.listdir(self.dir)
            if f.startswith("V") and f.endswith(".sql")
        )
        return [f for f in all_files if f.split("__")[0] not in applied]

    def run(self) -> None:
        self._acquire_lock()
        try:
            pending = self._pending_files()
            if not pending:
                print("No pending migrations.")
                return

            for filename in pending:
                version  = filename.split("__")[0]
                desc     = filename.split("__")[1].replace(".sql", "").replace("_", " ")
                filepath = os.path.join(self.dir, filename)
                checksum = self._checksum(filepath)

                print(f"Applying {version}: {desc} ...", end=" ", flush=True)
                t0 = time.time()
                try:
                    with open(filepath) as f:
                        sql = f.read()
                    with self.conn.cursor() as cur:
                        cur.execute(sql)
                        cur.execute(
                            "INSERT INTO schema_migrations "
                            "(version, description, checksum, duration_ms) "
                            "VALUES (%s, %s, %s, %s)",
                            (version, desc, checksum, int((time.time() - t0) * 1000))
                        )
                    self.conn.commit()
                    print(f"✅  ({int((time.time() - t0) * 1000)}ms)")
                except Exception as e:
                    self.conn.rollback()
                    print(f"❌  FAILED: {e}")
                    raise   # halt immediately — never skip to the next migration
        finally:
            self._release_lock()
```

### CI/CD Integration

The runner runs before the new application version starts serving traffic:

```yaml
# .github/workflows/deploy.yml
jobs:
  deploy:
    steps:
      - name: Run database migrations
        run: python migrate.py --env production
        env:
          DATABASE_URL: ${{ secrets.PROD_DATABASE_URL }}

      - name: Verify no pending migrations remain
        run: python migrate.py --pending --expect-zero

      - name: Deploy application
        run: kubectl apply -f k8s/deployment.yaml
        # Only executes if both previous steps succeeded
```

### Advisory Locks for Concurrent Safety

PostgreSQL advisory locks prevent two runner instances from applying migrations simultaneously:

```sql
-- Attempt to acquire (non-blocking) — returns TRUE if acquired, FALSE if held by another
SELECT pg_try_advisory_lock(9876543210);

-- Release when done
SELECT pg_advisory_unlock(9876543210);

-- Blocking version — waits until lock is available
SELECT pg_advisory_lock(9876543210);
```

---

## 17. Infrastructure-Level Migrations

The previous sections cover schema and data migrations within a single database instance. Infrastructure-level migrations operate at a larger scope: moving between engines, restructuring the hosting topology, or changing how the database connects to applications.

### Database Engine Migration (e.g., MySQL → PostgreSQL)

Moving between engines is one of the most complex migrations possible. SQL dialects differ, data types differ, constraint semantics differ.

**The four phases:**

```
Phase 1 — Schema Translation
  Translate DDL from source dialect to target dialect.
  Handle type differences:
    MySQL TINYINT    → PostgreSQL SMALLINT or BOOLEAN
    MySQL ENUM       → PostgreSQL custom TYPE or VARCHAR + CHECK
    AUTO_INCREMENT   → SERIAL or GENERATED ALWAYS AS IDENTITY
    DATETIME         → TIMESTAMP
  Validate that constraint behaviour is identical in both engines.

Phase 2 — Data Migration (choose offline or live)
  Offline (Big Bang): pg_dump + pgLoader — requires maintenance window
  Live (CDC): AWS DMS or Debezium streams changes continuously;
              application dual-writes to both engines during cutover

Phase 3 — Application Cutover
  Update connection strings.
  Audit every query — SQL valid in MySQL may be invalid in PostgreSQL
  (MySQL permits non-aggregated columns in GROUP BY; PostgreSQL does not).
  Run full regression test suite against the new engine.

Phase 4 — Validation
  Row count match across all tables.
  Checksum comparison on a random row sample per table.
  Application error rate back to baseline.
  All indexes, constraints, and triggers confirmed recreated.
```

| Tool | Source → Target | Notes |
|---|---|---|
| **AWS DMS** | MySQL, Oracle, MSSQL → PostgreSQL / Aurora | Managed; supports full-load and CDC live replication |
| **pgLoader** | MySQL, SQLite, CSV → PostgreSQL | Open source; handles type mapping; fast bulk load |
| **Debezium** | MySQL, PostgreSQL, Oracle → Kafka → any | CDC-based; good for live migration with zero downtime |
| **ora2pg** | Oracle → PostgreSQL | Specialised; handles PL/SQL to PL/pgSQL translation |
| **gh-ost** | MySQL → MySQL | Online schema changes on MySQL without table locks |

### Vertical Scaling Migration

Moving from a smaller database host to a larger one. Goal: minimise the cutover window.

```
1.  Snapshot the current primary
2.  Restore snapshot to the new (larger) instance
3.  Configure the new instance to replicate FROM the old primary
4.  Wait until replication lag reaches near-zero
5.  Enable maintenance mode — stop writes to old instance
6.  Wait until replication lag = 0 exactly
7.  Update DNS / connection string to the new instance
8.  Disable maintenance mode
9.  Monitor for 30 minutes: error rate, latency, connection counts
10. Decommission old instance once stable
```

### Multi-Region Migration

**Single-region to active-passive:**
1. Provision a replica in the new region and let it fully sync
2. Route read traffic to the regional replica for lower latency
3. Test failover procedure manually before relying on it
4. Update write DNS if the primary region is also changing

**Active-passive to active-active:** This is an architecture change, not a schema migration. Active-active requires a distributed database engine (CockroachDB, Spanner, Aurora Global Database) or application-level conflict resolution. Write a dedicated architecture migration playbook — do not attempt this through schema migration alone.

### Adding a Connection Pooler (e.g., PgBouncer)

Adding a connection pooler affects every service connected to the database. Migrate incrementally:

```
1.  Deploy PgBouncer alongside the database — do NOT reroute traffic yet
2.  Configure PgBouncer with the same credentials as the existing database
3.  Test a read query through PgBouncer from a dev environment
4.  Migrate one low-traffic, non-critical service to the PgBouncer DSN
5.  Monitor for 24 hours: connection counts, error rates, query latency
6.  Migrate remaining services one at a time
7.  Decommission direct-to-database connections once all services are migrated
```

> **⚠️ Watch Out:** PgBouncer in `transaction` mode (the default for maximum pooling efficiency) does **not** support prepared statements (`PREPARE` / `EXECUTE`) or session-level advisory locks (`pg_advisory_lock`). Applications using either must switch to `session` mode pooling, or refactor those queries before the migration.

---

## 18. Migration Tooling — The Full Landscape

Modern systems span relational databases, document stores, caches, message queues, and vector databases. Each requires a different migration approach.

---

### 18.1 SQL Database Migration Tools

| Tool | Ecosystem | Best For |
|---|---|---|
| **Flyway** | Any (SQL-first) | Teams starting with migrations; plain SQL; no abstraction |
| **Liquibase** | Any (YAML/XML/SQL) | Multi-DB; built-in rollback; enterprise audit requirements |
| **Alembic** | Python / SQLAlchemy | Python-native teams; ORM-integrated schema management |
| **Django Migrations** | Python / Django | Django projects — auto-generated from model changes |
| **ActiveRecord** | Ruby / Rails | Rails projects — tightly ORM-integrated |
| **Prisma Migrate** | TypeScript / Node.js | Declarative schema; auto-generates SQL |
| **Sqitch** | Any | Complex dependency graphs between migrations |
| **golang-migrate** | Go | Lightweight runner with no framework dependency |
| **gh-ost** | MySQL | Online schema changes on MySQL without table locks |
| **pt-online-schema-change** | MySQL | Percona's online schema change tool for MySQL |
| **AWS DMS** | Managed | Heterogeneous migrations with CDC (Oracle → PostgreSQL etc.) |

**Flyway vs Liquibase at a glance:**

| Criterion | Flyway | Liquibase |
|---|---|---|
| Migration format | SQL files | YAML / XML / SQL |
| Built-in rollback | No — write your own | Yes — declared per changeset |
| Conditional logic | Requires Java callback | Native preconditions |
| Multi-database support | Limited | Strong |
| Auditability | Basic history table | Detailed changelog with tags |
| Best for | Simple schemas; full SQL control | Multi-DB; audit requirements; rollback-first |

> **💡 Recommendation:** Start with Flyway. It forces you to write and understand real SQL. Graduate to Liquibase when you need conditional changesets, multi-database support, or first-class rollback blocks.

---

### 18.2 NoSQL Migration Tools

NoSQL databases are "schemaless" — but that does not mean migrations are unnecessary. The schema lives in application code; when that code changes, existing documents no longer match. That is a migration problem.

**Mongock** (MongoDB, DynamoDB, CosmosDB):

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
            Filters.exists("verified_at"),
            Updates.unset("verified_at")
        );
    }
}
```

**Three document-store migration patterns:**

- **Migrate-on-read (lazy):** Upgrade a document the first time it is read. *Risk:* Documents never read stay in the old format indefinitely.
- **Schema version field:** Embed `schema_version` in every document; application code branches on it.
- **Background bulk migration:** Same as the SQL batched backfill — process documents in batches with throttling and an idempotency guard.

---

### 18.3 Redis Migration

Redis has no DDL — but key naming conventions and data structure types still need migration.

```python
def get_user_session(user_id: str):
    session = redis.get(f"session:v2:{user_id}")              # try new key format
    if session is None:
        session = redis.get(f"session:{user_id}")             # fall back to old
        if session:
            redis.set(f"session:v2:{user_id}", session, ex=3600)  # promote to new
    return session

def set_user_session(user_id: str, data: bytes) -> None:
    redis.set(f"session:{user_id}",    data, ex=3600)         # old — backward compat
    redis.set(f"session:v2:{user_id}", data, ex=3600)         # new — forward progress
```

| Tool | What It Does |
|---|---|
| **RIOT (Redis I/O Tools)** | Official Redis tool; live key migration between Redis instances |
| **redis-migrate-tool** | Open source; migrates with key pattern filtering |
| **AWS DMS** | Supports Redis as a source for migration to other targets |

> **⚠️ Watch Out:** Redis TTLs are **not automatically preserved** during migration. Keys that arrive without a TTL may never expire (memory leak) or expire too soon (session loss). Verify TTL handling explicitly for every migration approach.

---

### 18.4 Kafka & Message Queue Migration

Kafka topics and their message schemas must be migrated as carefully as any database schema. A breaking schema change on a Kafka topic causes consumers to fail to deserialize messages — silently or with errors — cascading failures across every service that reads from that topic.

**Confluent Schema Registry compatibility modes:**

| Mode | What It Enforces | When to Use |
|---|---|---|
| `BACKWARD` (default) | New schema can deserialize old messages | Upgrade consumers before producers |
| `FORWARD` | Old schema can deserialize new messages | Upgrade producers before consumers |
| `FULL` | Both backward and forward compatible | Safest; most restrictive |
| `NONE` | No compatibility check | Development only — never production |

**Safe Avro evolution:**
```
✅ Safe (backward compatible):
   Add an optional field with a default value
   Remove a field that already had a default

❌ Breaking (requires a new topic):
   Rename a field
   Change a field type (int → string)
   Add a required field with no default
   Remove a required field
```

**For breaking schema changes — dual-topic migration:**
```python
def emit_event(event):
    producer.send("user-events",    serialize_v1(event))  # old consumers still work
    producer.send("user-events-v2", serialize_v2(event))  # new consumers use this
# Migrate consumers one-by-one to user-events-v2, then retire user-events
```

| Tool | What It Does |
|---|---|
| **Confluent Schema Registry** | Stores schemas, enforces compatibility per topic |
| **MirrorMaker 2** | Apache-native topic replication across Kafka clusters |
| **Kafka Connect** | Data migration between Kafka and external systems |

---

### 18.5 Vector Database Migration

Vector databases store high-dimensional embeddings for AI applications. Migrations are uniquely constrained: **embeddings are model-specific**. Vectors generated by one model are numerically incompatible with vectors generated by another. If you change your embedding model, every vector must be regenerated from source text.

| Trigger | Example |
|---|---|
| Scaling beyond current DB limits | pgvector slowing at 50M+ vectors |
| Changing the embedding model | `ada-002` → `text-embedding-3-large` |
| Cost or vendor change | Pinecone (managed) → Qdrant (self-hosted) |
| Adding hybrid search | Moving to a DB supporting vector + keyword together |

**Migration pattern when changing embedding model:**
```python
import time

def migrate_vectors(source_db, target_db, new_model, batch_size: int = 100) -> None:
    """Cannot copy raw vectors — models differ. Must re-embed from source text."""
    offset = 0
    while True:
        docs = source_db.query(
            "SELECT id, document_text, metadata FROM documents "
            "ORDER BY id LIMIT %s OFFSET %s",
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
        time.sleep(0.1)   # throttle embedding API rate limits
```

> **💡 Tip:** If keeping the same embedding model and only migrating between two databases that support compatible vector formats, you can copy raw vectors. If changing models simultaneously, you must re-embed — these are two separate operations even when they happen at the same time.

| Vector DB | Type | Best For |
|---|---|---|
| **pgvector** | PostgreSQL extension | Teams already on Postgres; under 10M vectors |
| **Qdrant** | Open source (Rust) | High performance; complex metadata filtering |
| **Pinecone** | Managed SaaS | Zero-ops; enterprise scale |
| **Weaviate** | Open source | Hybrid search (vector + keyword combined) |
| **Milvus / Zilliz** | Open source / managed | Billions of vectors; GPU-accelerated |
| **Chroma** | Open source | Local development and prototyping |

---

## 19. Migration Runbook Template

Every migration that touches a live table or collection with more than 100K rows deserves a written runbook. A second engineer must review it before anything runs.

```markdown
# Migration Runbook: [Short Description]

## Summary
One paragraph: what does this migration do, why is it needed now,
and what is the expected impact if it goes wrong?

## Risk Level
- [ ] Low    — Additive only (nullable column, new table, CONCURRENTLY index)
- [ ] Medium — Data backfill required; schema change on an active table
- [ ] High   — Destructive change; FK or check constraint on a large table; type change

## Pre-Migration Checklist
- [ ] Row count checked on production: ___________
- [ ] Estimated duration recorded (tested on staging at prod scale): ___________
- [ ] Disk space verified: ___________  (2× table size free for rewrites)
- [ ] Snapshot taken — ID: ___________
- [ ] Rollback script written, reviewed, and tested on staging
- [ ] `lock_timeout` confirmed in migration session config
- [ ] On-call engineer available and aware
- [ ] Monitoring dashboards open: DB CPU, replication lag, lock wait count, app error rate
- [ ] No ongoing incidents or high-risk deployments in flight

## Migration Steps

### Step 1: [Description]
```sql
SET lock_timeout = '5s';
-- migration SQL here
```
Expected duration: ___
Verification query and expected output:
```sql
SELECT COUNT(*) FROM users WHERE verified_at IS NULL;
-- Expected: 0
```

### Step 2: [Backfill / Validation]
(repeat structure above for each step)

## Post-Migration Verification
- [ ] Row counts match expected
- [ ] No INVALID indexes (`SELECT * FROM pg_index WHERE indisvalid = FALSE`)
- [ ] Application error rate returned to baseline
- [ ] Replication lag returned to baseline
- [ ] Backfill divergence check passed (if applicable)
- [ ] VACUUM ANALYZE run after heavy backfill

## Rollback Triggers (fill in real numbers — never leave blank)
- Auto-rollback: error rate > __% for __ consecutive minutes
- Manual rollback: migration not complete within __ hours
- Manual rollback: replication lag > __ seconds and not recovering

## Sign-Off
Executed by: ___   At: ___   Reviewed and verified by: ___
```

---

## 20. What Goes Wrong — Four Patterns From Production

These are real failure patterns — generalised from real incidents. Every engineer should read this section before running their first migration on a live system.

---

### 🔥 Failure #1: "It Ran Fine in Staging"

**What happened:** Staging had 50,000 rows. Production had 80 million. An `ALTER TABLE ADD COLUMN NOT NULL DEFAULT NOW()` completed in 0.3 seconds in staging. In production it rewrote every row while holding an exclusive lock for 11 minutes. Connection pool exhausted. Full service outage.

**Root cause:** The team confirmed the migration was *correct*. They did not confirm how long it would take at *production scale*.

**The rule:** Always check row count before any migration on a live table.
```sql
SELECT COUNT(*) FROM users;
-- If > 1,000,000 on a live system: use Expand/Contract, not a direct ALTER TABLE.
```

---

### 🔥 Failure #2: "The Rollback Made Things Worse"

**What happened:** A team dropped a column (Phase 3) while Kubernetes was mid-rolling-deploy. For 4 minutes, old pods (that still read the column) ran alongside new pods (that did not). Old pods crashed. The rollback re-added the column. New pods crashed on the re-added column. Rollback of the rollback: 8 more minutes. Total outage: 12 minutes.

**Root cause:** The team assumed deployment was atomic. Rolling deployments are not — both N and N-1 app versions are live simultaneously for several minutes.

**The rule:** Never run Phase 3 (DROP COLUMN) until the rolling deploy is 100% complete and the old version is fully retired.

---

### 🔥 Failure #3: "The Backfill Finished But the Data Was Wrong"

**What happened:** A 3-day backfill populated `normalized_email` from `raw_email`. During those 3 days, some users updated their email. The backfill used the value captured at job-start time. 0.3% of rows ended up with stale normalised email. Team cut reads without a divergence check. Incorrect search results. Required a second backfill plus a full reindex.

**Root cause:** "Completed without errors" was mistaken for "data is correct".

**The rule:** Always run the divergence check before every read cutover.
```sql
SELECT id, raw_email, normalized_email FROM users
WHERE normalized_email != LOWER(TRIM(raw_email))
LIMIT 100;
-- If ANY rows returned: do NOT cut over. Fix the backfill first.
```

---

### 🔥 Failure #4: "The Lock Queue Took Down the Application"

**What happened:** A team queued an `ALTER TABLE users ADD COLUMN ...` (needing `AccessExclusiveLock`) behind a long-running analytics report (holding a `ShareLock`). The analytics query had 4 minutes left to run. Every new application query needing any lock on `users` queued behind the waiting `ALTER TABLE`. Within 30 seconds the connection pool was full. The application stopped responding for 4 minutes — not because the migration was wrong, but because it silently queued behind another process.

**Root cause:** The team ran the migration without checking for long-running queries already holding locks on the table.

**The rule:** Set `lock_timeout` before every DDL statement, and run a pre-migration lock check.
```sql
-- Fail fast if lock cannot be acquired in 5 seconds
-- Retry when the blocking query has finished
SET lock_timeout = '5s';
ALTER TABLE users ADD COLUMN verified_at TIMESTAMP NULL;
```

---

## 21. Quick Reference — Pre-Migration Checklist

Answer every question before starting. Do not proceed on any "no" or "I don't know."

| # | Question | If No → |
|---|---|---|
| 1 | **How many rows / documents / vectors will be touched?** | If > 1M on a live system: use Expand/Contract or batched approach, not Big Bang |
| 2 | **Does this operation lock the table?** | Use `CONCURRENTLY` for indexes; `NOT VALID` for FK constraints; set `lock_timeout` |
| 3 | **Is there sufficient free disk space?** | 2× table size for rewrites; 1× index size for new indexes; 1.5× for heavy backfills |
| 4 | **Will the application stay live?** | If yes → Incremental. If maintenance window is acceptable → Big Bang may be fine |
| 5 | **Is the rollback script written, tested, and reviewed?** | Stop. Write it first. |
| 6 | **Is the backfill idempotent?** | A re-run after a crash must skip already-processed rows without errors |
| 7 | **Is the divergence check written?** | Must pass before flipping reads (Phase 2 → Phase 3) |
| 8 | **Is the migration compatible with both N and N-1 app versions?** | Do not drop old schema while the previous app version still reads it |
| 9 | **Are `statement_timeout` / `lock_timeout` / `innodb_lock_wait_timeout` set?** | Set them. No migration should run for unbounded time. |
| 10 | **Is replication lag being monitored with an alert?** | Set an alert threshold and pause the backfill if it fires |
| 11 | **Is the runbook complete and signed off by a second engineer?** | No single-engineer sign-off on Medium or High risk migrations |

---

## Further Reading

- [PostgreSQL — ALTER TABLE](https://www.postgresql.org/docs/current/ddl-alter.html)
- [PostgreSQL — CREATE INDEX CONCURRENTLY](https://www.postgresql.org/docs/current/sql-createindex.html#SQL-CREATEINDEX-CONCURRENTLY)
- [PostgreSQL — Lock Monitoring](https://www.postgresql.org/docs/current/monitoring-locks.html)
- [PostgreSQL — NOT VALID Constraints](https://www.postgresql.org/docs/current/sql-altertable.html)
- [MySQL — Online DDL Operations](https://dev.mysql.com/doc/refman/8.0/en/innodb-online-ddl-operations.html)
- [MySQL — InnoDB Lock Wait Timeout](https://dev.mysql.com/doc/refman/8.0/en/innodb-parameters.html#sysvar_innodb_lock_wait_timeout)
- [Flyway Documentation](https://flywaydb.org/documentation)
- [Liquibase Documentation](https://docs.liquibase.com)
- [Mongock Documentation](https://mongock.io/docs)
- [Confluent Schema Registry — Schema Evolution](https://docs.confluent.io/platform/current/schema-registry/fundamentals/schema-evolution.html)
- [Qdrant Migration Tool (GitHub)](https://github.com/qdrant/migration)
- [Stripe Engineering — Online Migrations at Scale](https://stripe.com/blog/online-migrations)
- [AWS Database Migration Service](https://aws.amazon.com/dms/)
- [gh-ost — GitHub's Online Schema Change for MySQL](https://github.com/github/gh-ost)

---
