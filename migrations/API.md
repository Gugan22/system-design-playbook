# API Version Migration
> How to evolve your APIs without breaking clients — REST, GraphQL, gRPC, WebSockets, Message Queues, SOAP, and GraphQL Federation

---

## How to Use This Document

Real systems use REST, GraphQL, gRPC, WebSockets, and event-driven APIs — sometimes all at once. Each has a different versioning philosophy, and applying the wrong strategy to the wrong API type is a common source of production incidents.

This document covers all major API types with concrete examples. It is written for engineers at all levels:

- **New to API design?** Start at Section 1 and read linearly.
- **Experienced engineer?** Jump to the API type you are working with using Quick Navigation.
- **Want to know what your framework gives you out of the box?** Go to [Section 4b](#4b-framework-versioning-support) — framework-by-framework overview and support matrix covering Spring Framework 7 / Boot 4, ASP.NET Core, DRF, NestJS, Laravel, Rails, and Go.
- **On Spring Framework 7 / Spring Boot 4?** Go to [Section 4c](#4c-spring-framework-7--spring-boot-4) for the full native `configureApiVersioning()` deep-dive, including Kotlin and WebFlux examples.
- **Preparing for a Tech Lead interview?** Read every "What goes wrong in production" box — those are worth more than the theory.
- **Versioning Message Queues, SOAP, or GraphQL Federation?** Go to [Section 7b — Message Queues](#7b-message-queue--event-driven-api-versioning), [Section 7c — SOAP](#7c-soap--legacy-xml-api-versioning), or [Section 7d — GraphQL Federation](#7d-graphql-federation-versioning).
- **Just need a checklist?** Go straight to [Section 14](#14-api-migration-execution-checklist).

---

## Quick Navigation

**Foundations**

| I want to... | Go to |
|---|---|
| Understand what counts as a breaking change | [Section 2 — Breaking Changes](#2-what-counts-as-a-breaking-change) |
| Learn how to retire an old API version | [Section 3 — Sunset Strategy](#3-sunset-strategy) |
| Get the TL;DR key principles | [Section 16 — Summary](#16-summary) |
| Get the execution checklist | [Section 14 — Checklist](#14-api-migration-execution-checklist) |
| See real failure stories | [Section 13 — What Goes Wrong](#13-what-ive-seen-go-wrong-in-production) |

**Versioning by API Type**

| I want to... | Go to |
|---|---|
| Handle REST API versioning | [Section 4 — REST Versioning](#4-rest-api-versioning) |
| Handle GraphQL versioning | [Section 5 — GraphQL](#5-graphql-api-versioning) |
| Handle gRPC / Protobuf versioning | [Section 6 — gRPC](#6-grpc--protobuf-versioning) |
| Handle WebSocket / Streaming versioning | [Section 7 — WebSockets](#7-websocket--streaming-api-versioning) |
| Handle Message Queue / Event-Driven versioning | [Section 7b — Message Queues](#7b-message-queue--event-driven-api-versioning) |
| Handle SOAP / Legacy XML versioning | [Section 7c — SOAP](#7c-soap--legacy-xml-api-versioning) |
| Handle GraphQL Federation versioning | [Section 7d — GraphQL Federation](#7d-graphql-federation-versioning) |

**Implementation & Operations**

| I want to... | Go to |
|---|---|
| See framework versioning support (all stacks) | [Section 4b — Framework Matrix](#4b-framework-versioning-support) |
| Spring Framework 7 / Boot 4 native versioning | [Section 4c — Spring Framework 7](#4c-spring-framework-7--spring-boot-4) |
| Manage SDKs alongside API versions | [Section 8 — SDK Versioning](#8-sdk-versioning) |
| Handle mobile clients | [Section 9 — Mobile Clients](#9-mobile-client-migration) |
| Manage multi-tenant versioning | [Section 10 — Multi-Tenant](#10-multi-tenant-api-versioning) |
| Set up automated contract testing | [Section 11 — Contract Testing](#11-consumer-driven-contract-testing) |
| Handle graceful degradation | [Section 12 — Graceful Degradation](#12-graceful-degradation) |

---

## Table of Contents

1. [Introduction — Why API Migration Is Hard](#1-introduction)
2. [What Counts as a Breaking Change](#2-what-counts-as-a-breaking-change)
3. [Sunset Strategy — How to Retire Old Versions Without Chaos](#3-sunset-strategy)
4. [REST API Versioning](#4-rest-api-versioning)
   - [4b. Framework Versioning Support Matrix](#4b-framework-versioning-support)
   - [4c. Spring Framework 7 / Spring Boot 4 — Native Versioning In Depth](#4c-spring-framework-7--spring-boot-4)
5. [GraphQL API Versioning](#5-graphql-api-versioning)
6. [gRPC / Protobuf Versioning](#6-grpc--protobuf-versioning)
7. [WebSocket / Streaming API Versioning](#7-websocket--streaming-api-versioning)
   - [7b. Message Queue & Event-Driven API Versioning](#7b-message-queue--event-driven-api-versioning)
   - [7c. SOAP / Legacy XML API Versioning](#7c-soap--legacy-xml-api-versioning)
   - [7d. GraphQL Federation Versioning](#7d-graphql-federation-versioning)
8. [SDK Versioning](#8-sdk-versioning)
9. [Mobile Client Migration](#9-mobile-client-migration)
10. [Multi-Tenant API Versioning](#10-multi-tenant-api-versioning)
11. [Consumer-Driven Contract Testing](#11-consumer-driven-contract-testing)
12. [Graceful Degradation](#12-graceful-degradation)
    - [12b. Rollback Strategy — Going Back Fast](#12b-rollback-strategy--going-back-fast)
    - [12c. Monitoring During a Migration](#12c-monitoring-during-a-migration)
13. [What I've Seen Go Wrong in Production](#13-what-ive-seen-go-wrong-in-production)
14. [API Migration Execution Checklist](#14-api-migration-execution-checklist)
    - [14b. If You're the Consumer](#14b-if-youre-the-consumer--what-to-do-when-your-api-is-changing)
15. [API Type Comparison — Which Strategy Applies Where](#15-api-type-comparison)
16. [Summary — The Key Principles](#16-summary)
17. [Related Documents & Further Reading](#17-related-documents--further-reading)
18. [Changelog](#18-changelog)

---

## 1. Introduction

Every API you ship is a contract. Once a client starts using it — a mobile app, a partner's backend, a third-party integration — changing that contract can break things instantly. This document is your guide to changing that contract safely.

"API versioning" means different things depending on the API type:

- In **REST**, it usually means `/v1/` vs `/v2/` in the URL.
- In **GraphQL**, the community largely avoids explicit versions — instead you evolve the schema carefully.
- In **gRPC**, versioning is baked into Protobuf field numbering and package names.
- In **WebSockets**, versioning happens at the connection handshake level.

Understanding *which* strategy to apply *where* is the mark of a senior engineer. The following sections cover each type in depth.

### The Core Problem in Plain English

Imagine you have a mobile app with one million users. Half of them have **not** updated to the latest version. Your backend is one service. When you change the `/users` endpoint to return `fullName` instead of `name`, every older app breaks immediately.

The goal of API version migration is to give both old and new clients time to coexist on the same backend, with a clear end-date for when you stop supporting the old shape.

---

## 2. What Counts as a Breaking Change

This section applies to all API types. Before any change, run this test.

### Universal Breaking Change Test

| Change Type | Example | Breaking? |
|---|---|---|
| Remove a field / method / type | Remove `username` from response | **YES** |
| Rename a field | `name` → `fullName` | **YES** |
| Change a field's data type | `userId: int` → `userId: string` | **YES** |
| Change a field from optional to required | Make `email` required in request | **YES** |
| Remove an endpoint / operation | Delete `DELETE /users/:id` | **YES** |
| Change HTTP status code semantics | `404` becomes `200` with empty body | **YES** |
| Add a new required field to a request | New mandatory `region` param | **YES** |
| Change authentication / auth requirement | Add or remove API key | **YES** — see note |
| Add a new optional field to a response | Add optional `middleName` | **NO** |
| Add a new optional request parameter | Add optional `?include=address` | **NO** |
| Add a new endpoint | New `GET /users/:id/preferences` | **NO** |
| Change internal implementation | Faster algorithm, same output | **NO** |
| Widen an enum (add a new value) | Add `PENDING_REVIEW` to order status | **RISKY** — see note |
| Change error response shape | `{"error":"not_found"}` → `{"code":404,"message":"..."}` | **YES** — clients parsing errors will break |
| Reduce rate limits | Lower requests/second on an endpoint | **YES** — consumers tuned to current limits will start failing |

### Backward vs Forward Compatibility — Know the Difference

These two terms are often confused. Both matter in migrations and they protect different failure modes.

| Term | Definition | Example |
|---|---|---|
| **Backward compatibility** | Old clients can correctly read data produced by new server code | v1 mobile app reads a response from the upgraded v2 server without errors |
| **Forward compatibility** | New clients can gracefully handle data produced by old server code | v2 mobile app handles a response from an older not-yet-upgraded server during a rolling deploy |

Most migration strategies focus on backward compatibility. Forward compatibility is equally important in rolling deployments where new client versions may briefly talk to old server versions during the deploy window.

> **⚠️ Auth changes need special treatment.** Removing an auth requirement may violate security policy and compliance agreements. Adding a new auth requirement is always breaking — existing callers cannot know to include the new credentials. If your migration touches auth, loop in your security team early and treat it as a separate versioned change.

#### Auth Migration Playbook

| Auth Change | Strategy |
|---|---|
| **Adding API key requirement** | Run a dual-auth window: accept both authenticated and unauthenticated requests during the deprecation period. Log unauthenticated callers, contact them directly, then enforce. |
| **Migrating API keys → OAuth** | Support both simultaneously. Old clients keep API keys. New clients use OAuth tokens. Sunset API keys on their own timeline — do not couple to your API version migration. |
| **Changing OAuth scopes** | Issue new scopes alongside old ones. Old tokens with old scopes continue to work. After sunset, return a clear error: `{"error": "scope_deprecated", "required_scope": "users:read:v2"}` |
| **Rotating JWT signing keys** | Support the old key for a rolling window (24–48 hours minimum). Never invalidate all existing tokens instantly — users mid-session will be logged out. |
| **Removing auth entirely** | Requires security sign-off. Usually only valid for public read-only endpoints. Document the decision and rationale in your change log. |
| **Migrating to mTLS (service-to-service)** | Common in zero-trust environments. Issue client certificates alongside existing auth during transition. Services must validate the client cert in addition to (then instead of) the old credential. Coordinate cert rotation windows — mTLS cert expiry is a breaking operational event. Never let certs expire silently; add expiry alerting before any mTLS migration goes live. |

> **⚠️ Enum additions are sneaky.** Adding a new enum value is technically non-breaking on the server side, but breaks clients that use exhaustive switch statements or strict enum parsers. Always communicate new enum values in advance, and design clients to handle unknown values gracefully.

### Breaking Changes by API Type

The same underlying change has different implications depending on your API type:

| Change | REST | GraphQL | gRPC | Message Queue |
|---|---|---|---|---|
| Remove a field | Breaking | Breaking | Breaking (field number) | Breaking — old consumers cannot deserialise |
| Rename a field | Breaking | Breaking | Breaking | Breaking — treat as remove + add |
| Add optional field | Safe | Safe | Safe (new number) | Safe if schema registry in BACKWARD mode |
| Change field type | Breaking | Breaking | Breaking | Breaking |
| Remove an operation/topic | Breaking | Breaking | Breaking | Breaking — must drain queue first |
| Add a new operation/topic | Safe | Safe | Safe | Safe |
| Reorder fields | Usually safe | Safe | **BREAKING** (positional) | **BREAKING** in Avro (positional) — safe in Protobuf |

---

## 3. Sunset Strategy

A sunset is the planned, communicated shutdown of an old API version. Without a sunset strategy, your team ends up maintaining v1, v2, v3, and v4 forever. That is expensive, risky, and demoralising.

### The Sunset Lifecycle — Four Phases

| Phase | What Happens |
|---|---|
| **1. Active** | Version is fully supported. No warnings. New features may still be added. |
| **2. Deprecated** | Version still works. Machine-readable warnings on every response. Docs updated. Migration guide published. |
| **3. Sunset Warning** | Direct notifications to all known consumers. Hard deadline communicated. Countdown begins. Traffic monitoring active. |
| **4. Retired** | Version removed or returns `410 Gone`. Traffic-gated — only retire when traffic data confirms it is safe. |

### How to Signal Deprecation (REST / HTTP APIs)

Add these headers to **every** response from a deprecated version — not just errors:

```http
Deprecation: true
Sunset: Sat, 31 Dec 2026 23:59:59 GMT
Link: <https://docs.yourapi.com/migrate-v1-to-v2>; rel="deprecation"
```

### How Long Should You Support an Old Version?

| Consumer Type | Suggested Support Window |
|---|---|
| Internal services (same org) | 3–6 months after new version General Availability (GA) |
| External partner integrations | 12 months minimum after GA |
| Public API with unknown clients | 18–24 months minimum |
| Mobile apps (can't force-update) | Until < 1% traffic on old version — monitor, don't guess |
| Enterprise customers with SLAs | Check contract — often 90–180 days written notice required |

### Enterprise and Partner APIs — Formal Deprecation Notices

HTTP headers are not enough for enterprise APIs. Partners may have legal or contractual SLAs requiring written notice.

| Obligation | What to Do |
|---|---|
| **Contractual SLA notice** | Check partner agreements for required notice periods. Send a formal email on deprecation day — not just a header change. |
| **Change log / release notes** | Publish a named entry: "v1 deprecated on [date], sunset on [date]." Link to the migration guide. |
| **Developer portal** | Add a visible "Deprecated" badge. Some teams add a red banner. |
| **Support ticket tracking** | For external partners, open a tracked ticket. Don't assume they read emails or headers. |
| **Legal review** | For APIs under data processing agreements, loop in legal before sunset. "We just turned it off" is not a legal defense. Key areas to check: GDPR Article 28 (processor changes may require controller notification), SOC 2 change management (deprecations are often auditable events), and SaaS contract "material change" clauses which may grant customers exit rights if an API is retired without adequate notice. |

> **⚠️ What goes wrong in production:** Teams announce a sunset date without checking actual traffic. On shutdown day, 8% of requests are still from old client versions — old CI pipelines, forgotten batch jobs, a partner who "forgot" to migrate. Always gate the shutdown on traffic data, not the calendar alone.

---

## 4. REST API Versioning

REST is the most common API style and has the most established versioning patterns.

### The Three Versioning Strategies

#### Strategy 1: URL Path Versioning

```
GET /v1/users/123
GET /v2/users/123
```

**Pros:** Immediately visible, easy to route, easy to test in a browser, cache-friendly (URL is unique per version).

**Cons:** Creates URL duplication. Can sprawl if not managed.

**Best for:** Public APIs, external-facing APIs, teams getting started with versioning.

#### Strategy 2: Header Versioning

```http
GET /users/123
X-API-Version: 2
```

or using content negotiation:

```http
GET /users/123
Accept: application/vnd.myapi.v2+json
```

**Pros:** Clean URLs. The URL represents the resource, not the version of its representation — which is technically more correct per REST principles. Used by GitHub's API.

**Cons:** Not visible in the browser URL bar. Harder to test without a client tool like Postman.

**Best for:** Internal microservices, teams that care about URL cleanliness, GitHub-style public APIs.

#### Strategy 3: Query Parameter Versioning

```
GET /users/123?version=2
```

**Pros:** Easy to add, easy to override per-request.

**Cons:** Hard to cache correctly. Easy to forget.

**Best for:** Internal APIs only, quick experiments. Never for public APIs.

### Gateway Routing for Multi-Version REST APIs

Your API gateway should normalise the version signal before passing requests to your services. Never let version detection logic leak into your application code.

```nginx
# Priority order at the gateway:

1. URL path   (/v2/*)             → route to v2 service cluster
2. Header     (X-API-Version: 2)  → route to v2 service cluster
3. Default                        → route to v1 + attach Deprecation headers
```

#### Kong example (declarative config):

```yaml
services:
  - name: user-service-v2
    url: http://user-service-v2:8080
    routes:
      - name: v2-url-route
        # Explicit /v2/* path — no header required
        paths: ["/v2/users"]
      - name: v2-header-route
        # /users path + header constraint — Kong prioritises header-constrained
        # routes over unconstrained routes at the same path
        paths: ["/users"]
        headers:
          X-API-Version: ["2"]

  - name: user-service-v1
    url: http://user-service-v1:8080
    routes:
      - name: v1-url-route
        paths: ["/v1/users"]
      - name: v1-default
        # /users path with NO header constraint — catches all remaining /users traffic
        # Kong route priority: header-constrained routes evaluated first
        paths: ["/users"]
    plugins:
      - name: response-transformer
        config:
          add:
            headers:
              - "Deprecation: true"
              - "Sunset: Sat, 31 Dec 2026 23:59:59 GMT"
```

> **Always verify routing with `curl` after deployment.** Test: (1) `curl /v2/users` → v2, (2) `curl -H "X-API-Version: 2" /users` → v2, (3) `curl /users` → v1 with Deprecation header, (4) `curl /v1/users` → v1 with Deprecation header.

#### AWS API Gateway example (routing by stage):

```json
{
  "routes": {
    "GET /users/{id}": {
      "integration": {
        "type": "HTTP_PROXY",
        "uri": "http://user-service-${stageVariables.apiVersion}/users/{id}"
      }
    }
  },
  "stages": {
    "v1": { "stageVariables": { "apiVersion": "v1" } },
    "v2": { "stageVariables": { "apiVersion": "v2" } }
  }
}
```

> **⚠️ AWS API Gateway gotcha:** Stage variables work in **REST APIs (v1)** but are **not supported** in **HTTP APIs (v2)**. If you are using the newer HTTP API type (lower latency, lower cost), use Lambda routing logic or ALB path-based rules for version routing instead of stage variables.

### OpenAPI / Swagger Spec Management Across Versions

As you add versions, your API spec files need to be versioned too.

#### Option A: Separate spec files per version (recommended)

```
docs/
  api/
    openapi-v1.yaml   ← frozen when v1 deprecated
    openapi-v2.yaml   ← active
    openapi-v3.yaml   ← in development
```

Freeze the spec for deprecated versions. Only the active version gets new features.

#### Option B: Code-generated specs (preferred for microservices)

Generate the OpenAPI spec from your code annotations. Each deployed version produces its own spec at `/openapi.json`. Your developer portal aggregates them.

```python
# FastAPI example — spec is auto-generated per version
app_v1 = FastAPI(title="User API", version="1.0.0", docs_url="/v1/docs")
app_v2 = FastAPI(title="User API", version="2.0.0", docs_url="/v2/docs")
```


---

## 4b. Framework Versioning Support — What Your Stack Provides Out of the Box

Before writing custom routing logic, check what your framework already gives you. This is a concise overview — it tells you *what exists*, not a full implementation guide. For Spring Framework 7 / Spring Boot 4 implementation detail, see [Section 4c](#4c-spring-framework-7--spring-boot-4).

**Jump to your framework:** [Spring Framework 7 / Boot 4](#spring-framework-7--spring-boot-4) | [Spring Boot 3.x](#spring-boot-3x) | [ASP.NET Core](#aspnet-core-net-8) | [Django REST Framework](#django-rest-framework) | [FastAPI](#fastapi) | [NestJS](#nestjs) | [Laravel](#laravel) | [Rails](#ruby-on-rails) | [Express / Go](#expressjs--go)

### Versioning Support Matrix

| Framework | URL Path | Header | Query Param | Media Type | Auto Deprecation Headers | Notes |
|---|---|---|---|---|---|---|
| **Spring Boot 4 / Framework 7** | Native | Native | Native | Native | `StandardApiVersionDeprecationHandler` | First full native support — see Section 4c |
| **Spring Boot 3.x** | Convention | Custom `RequestCondition` | Manual | Manual | Custom filter required | No built-in versioning API |
| **ASP.NET Core (.NET 8+)** | `Asp.Versioning` pkg | `HeaderApiVersionReader` | `QueryStringApiVersionReader` | `MediaTypeApiVersionReader` | `api-deprecated-versions` header | Official Microsoft NuGet — not in core runtime |
| **Django REST Framework** | `URLPathVersioning` | `AcceptHeaderVersioning` | `QueryParameterVersioning` | `AcceptHeaderVersioning` | Manual via `finalize_response()` | 5 built-in classes; `request.version` in every view |
| **FastAPI** | `APIRouter` prefix | Middleware | Manual | Manual | Manual per-router | No native versioning; community packages available |
| **NestJS** | `VersioningType.URI` | `VersioningType.HEADER` | Via `CUSTOM` extractor | `VersioningType.MEDIA_TYPE` | Custom `NestInterceptor` | Native `enableVersioning()` since v8 |
| **Laravel** | `Route::prefix()` | Middleware | Manual | Manual | Manual | `apiPrefix` parameter added in Laravel 11 |
| **Ruby on Rails** | `namespace` routing | Routing constraints | Manual | Manual | Manual | Rails normalises headers to title-case |
| **Express.js / Go** | Router prefix | Middleware | Manual | Manual | Manual | Pure convention; no framework primitives |

---

### Framework-by-Framework Summary

#### Spring Framework 7 / Spring Boot 4

The only Java framework with full native versioning support (released November 2025). `configureApiVersioning()` on `WebMvcConfigurer` (or `WebFluxConfigurer` for reactive) sets the version resolution strategy globally — header, path segment, query param, or media type. The `version` attribute on `@GetMapping`, `@PostMapping`, etc. pins each handler to a fixed or baseline version (`"1.1+"`). `StandardApiVersionDeprecationHandler` auto-emits RFC-compliant `Deprecation`, `Sunset`, and `Link` headers. Works identically in Java and Kotlin. Full implementation detail in Section 4c.

#### Spring Boot 3.x

No built-in versioning API. Standard approaches: URL prefix conventions (`/v1/`, `/v2/`) with gateway-layer routing (Kong, AWS API GW), or a custom `RequestCondition` for header-based matching. **Important:** when upgrading to Boot 4, remove any custom `RequestCondition` before enabling native versioning — they conflict and cause silent wrong-handler routing (Failure Pattern 7).

#### ASP.NET Core (.NET 8+)

The `Asp.Versioning.Mvc` NuGet package (official Microsoft, not in core runtime) provides URL segment, header, query string, and media type version readers — combinable. `[ApiVersion("1.0", Deprecated = true)]` on a controller auto-populates the `api-deprecated-versions` response header. For RFC 9745-compliant `Deprecation` + `Sunset` headers, add a response middleware. Swagger integration via `Asp.Versioning.Mvc.ApiExplorer`.

#### Django REST Framework

Five built-in versioning classes, configured globally via `DEFAULT_VERSIONING_CLASS` in `settings.py`. Once set, `request.version` is available in every view — no per-view wiring. `ALLOWED_VERSIONS` whitelists valid versions; unrecognised versions raise a `NotFound` exception (HTTP `404`) in recent DRF versions — earlier versions returned `403 Forbidden`. Check your DRF version. Deprecation headers are not emitted natively — override `finalize_response()` in a base `APIView` to inject them for deprecated version strings.

#### FastAPI

No native versioning. Community standard: `APIRouter` with path prefixes (`/v1`, `/v2`) mounted on the main `FastAPI` app. Community libraries (`fastapi-versionizer`, `fastapi-versioning`) add a `@api_version` decorator and per-version Swagger docs, but are not FastAPI core.

#### NestJS

Native versioning since v8 via `app.enableVersioning()` in `main.ts`. Four strategies: `URI` (path prefix), `HEADER`, `MEDIA_TYPE`, and `CUSTOM` (arbitrary extractor — useful for query param versioning). `@Version()` on a controller method pins it to a version; `VERSION_NEUTRAL` matches all. Deprecation headers require a global `NestInterceptor`.

#### Laravel

Convention-based. `Route::prefix('v1')` and `Route::prefix('v2')` group route files. Laravel 11 added `apiPrefix` in `bootstrap/app.php` for setting a global API prefix. Controllers live in versioned namespaces (`Api\V1\UserController`). No version validation or deprecation headers natively.

#### Ruby on Rails

Convention-based via routing namespaces: `namespace :api { namespace :v1 { resources :users } }`. For header-based versioning, use a routing constraint class. Note: Rails normalises HTTP header names to title-case — use `X-Api-Version`, not `X-API-Version`.

#### Express.js / Go

Pure convention. Express uses `Router()` with a path prefix; Go (Chi, Gin) uses router groups or sub-routers. Deprecation middleware can be scoped to old version groups. No framework versioning primitives in either ecosystem.

---

### Which Framework Pattern Should You Apply?

| Your Stack | Recommended Approach | Key Caveat |
|---|---|---|
| **Spring Boot 4 / Framework 7** | Native `configureApiVersioning()` + `version` attribute | Requires Boot 4 — not available in 3.x |
| **Spring Boot 3.x** | URL prefix + gateway routing | Remove custom `RequestCondition` before upgrading to Boot 4 |
| **ASP.NET Core (.NET 8+)** | `Asp.Versioning.Mvc` — combine readers as needed | Install separately; add middleware for RFC deprecation headers |
| **Django REST Framework** | `DEFAULT_VERSIONING_CLASS` globally | Set `ALLOWED_VERSIONS` before GA; `finalize_response()` for deprecation |
| **FastAPI** | `APIRouter` prefix; `fastapi-versionizer` for per-version Swagger | Community packages only |
| **NestJS** | `enableVersioning()` + `@Version()` | Add a global interceptor for deprecation headers |
| **Laravel** | `Route::prefix()` groups + versioned namespaces | Convention only — add middleware for deprecation headers |
| **Ruby on Rails** | `namespace` routing + versioned controller modules | Headers are title-cased by Rails |
| **Express.js / Go** | Router prefix groups | Pure convention — all deprecation logic is manual |


---

## 4c. Spring Framework 7 / Spring Boot 4 — Native API Versioning In Depth

Spring Framework 7, released November 2025 and paired with Spring Boot 4 (collectively referred to here as "Spring Boot 4 / Framework 7"), introduced the first full native API versioning support in the Spring ecosystem. Prior to this, every Spring versioning approach required custom `RequestCondition` implementations, manual URL rewriting, or external gateway logic.

### The Four Core Classes

| Class / Interface | Role |
|---|---|
| `ApiVersionStrategy` | Central strategy: resolves, parses, validates request versions; fires deprecation handlers |
| `ApiVersionConfigurer` | Configuration DSL in `WebMvcConfigurer` / `WebFluxConfigurer` |
| `SemanticApiVersionParser` | Default parser: `major.minor.patch` — minor and patch default to 0 if absent |
| `StandardApiVersionDeprecationHandler` | Sets RFC-compliant `Deprecation`, `Sunset`, `Link` headers automatically |

### Configuration — Two Methods

**Method 1: Java configuration**

```java
// Required imports
import org.springframework.context.annotation.Configuration;
import org.springframework.web.servlet.config.annotation.WebMvcConfigurer;
import org.springframework.web.servlet.config.annotation.ApiVersionConfigurer;
import org.springframework.web.servlet.config.annotation.PathMatchConfigurer;
import org.springframework.web.servlet.mvc.method.annotation.StandardApiVersionDeprecationHandler;
import org.springframework.web.util.pattern.HandlerTypePredicate;
import org.springframework.web.bind.annotation.RestController;
import java.time.ZonedDateTime;
import java.time.ZoneOffset;

@Configuration
public class WebConfig implements WebMvcConfigurer {

    @Override
    public void configureApiVersioning(ApiVersionConfigurer configurer) {
        StandardApiVersionDeprecationHandler handler =
            new StandardApiVersionDeprecationHandler();

        // Configure v1 as deprecated — handler adds headers automatically
        handler.configureVersion("1.0")
            .setDeprecationDate(ZonedDateTime.of(2025, 6, 1, 0, 0, 0, 0, ZoneOffset.UTC))
            .setSunsetDate(ZonedDateTime.of(2026, 1, 1, 0, 0, 0, 0, ZoneOffset.UTC));

        configurer
            .useRequestHeader("API-Version")         // resolve version from header
            // .usePathSegment(0)                     // OR: resolve from /v1/...
            // .useQueryParam("v")                    // OR: resolve from ?v=1.0
            // .useMediaTypeParam("v")                // OR: resolve from Accept header param
            .addSupportedVersions("1.0", "1.1", "2.0")
            .setDefaultVersion("2.0")                // used when client sends no version
            .setDeprecationHandler(handler);
    }

    // REQUIRED when using usePathSegment: configure the path prefix globally
    // so you don't repeat /{version} in every @RequestMapping
    @Override
    public void configurePathMatch(PathMatchConfigurer configurer) {
        configurer.addPathPrefix("/{v}", HandlerTypePredicate.forAnnotation(RestController.class));
        // Now all @RestController endpoints are automatically prefixed with /{v}
        // matching /v1/users, /v2/users, etc. — no changes needed in each controller
    }
}
```

**Method 2: `application.properties` (Spring Boot 4)**

```properties
# Server: choose one resolution strategy
spring.mvc.apiversion.use.header=API-Version
# spring.mvc.apiversion.use.path-segment=0
# spring.mvc.apiversion.use.query-param=v

# Client-side: configure RestClient / WebClient inserter
spring.http.client.restclient.apiversion.insert.header=API-Version
```

### The `version` Attribute on Mappings

```java
@RestController
public class UserController {

    // Fixed version: handles ONLY requests for version 1.0
    @GetMapping(path = "/users/{id}", version = "1.0")
    public UserV1 getUserV1(@PathVariable String id) {
        return userService.getUserV1(id);
    }

    // Baseline version ("1.1+"): handles 1.1 AND all future versions
    // until a higher-version handler is declared for the same path
    @GetMapping(path = "/users/{id}", version = "1.1+")
    public UserV2 getUserV2(@PathVariable String id) {
        return userService.getUserV2(id);
    }
}
```

**Why `"1.1+"` matters:** Without baseline versions, every unchanged endpoint needs a copy in every new API version. With `"1.1+"`, unchanged endpoints automatically serve all future versions. This is the biggest ergonomic improvement over the old pattern.

### Client Support — `RestClient` and `@HttpExchange`

```java
// RestClient — configure the inserter once
RestClient client = RestClient.builder()
    .baseUrl("http://api.example.com")
    .apiVersionInserter(ApiVersionInserter.useHeader("API-Version"))
    .build();

// Specify version per request — the "how" (header vs path vs query) is abstracted
User user = client.get()
    .uri("/users/{id}", "123")
    .apiVersion("1.1")
    .retrieve()
    .body(User.class);

// HTTP Interface client
@HttpExchange("/users")
public interface UserService {
    @GetExchange(url = "/{id}", version = "1.1")
    User getUser(@PathVariable String id);
}
```

### Validation Behaviour

| Request scenario | Framework response |
|---|---|
| Version absent, `setDefaultVersion` configured | Uses the default — no error |
| Version absent, no default configured | `MissingApiVersionException` → HTTP `400` |
| Version present, not in supported list | `InvalidApiVersionException` → HTTP `400` |
| Version matches a deprecated version | Calls deprecation handler → adds `Deprecation` + `Sunset` + `Link` headers |

> **⚠️ Security: version enumeration attacks.** A `400 Bad Request` for an unsupported version is correct behaviour, but consider two production hardening steps: (1) Do not echo the invalid version string back in the error response body — this helps attackers enumerate your supported versions via trial and error. Return a generic `"Version not supported"` message, not `"Version '0.9' is not in supported list [1.0, 1.1, 2.0]"`. (2) Monitor for bursts of 400s on the version header from a single IP — version probing is a low-cost reconnaissance technique. Add a rate-limit rule at your gateway for repeated version-invalid requests.

### Testing

```java
@SpringBootTest(webEnvironment = WebEnvironment.RANDOM_PORT)
class UserApiVersionTest {

    @Autowired RestTestClient testClient;  // new in Spring Framework 7.0

    @Test
    void v1DeprecationHeadersArePresent() {
        testClient.get().uri("/users/123")
            .apiVersion("1.0")
            .exchange()
            .expectStatus().isOk()
            .expectHeader().exists("Deprecation")
            .expectHeader().exists("Sunset");
    }

    @Test
    void unsupportedVersionReturns400() {
        testClient.get().uri("/users/123")
            .apiVersion("0.9")
            .exchange()
            .expectStatus().isBadRequest();
    }
}
```

> **Spring Boot 3.x users:** The native `configureApiVersioning()` API requires Spring Framework 7 and Spring Boot 4. On Boot 3.x, use URL prefix conventions (`/v1/`, `/v2/`) with gateway routing as described in Section 4. Plan your Spring Boot 4 migration — it was released November 2025.

### Kotlin and WebFlux Support

**Kotlin:** Spring Framework 7 / Spring Boot 4 is fully supported in Kotlin. `WebMvcConfigurer`, `configureApiVersioning()`, and the `version` attribute on `@GetMapping` / `@PostMapping` work identically in Kotlin — the DSL translates directly with no API differences.

**Spring WebFlux (reactive):** For reactive applications, replace `WebMvcConfigurer` with `WebFluxConfigurer`. The `configureApiVersioning()` method, its options, and the `version` mapping attribute are identical. `WebClient` uses `ApiVersionInserter` in the same way as `RestClient`, and `WebTestClient.apiVersion()` provides the same test DSL.


---

## 5. GraphQL API Versioning

GraphQL takes a fundamentally different approach to versioning: **the community philosophy is "don't version — evolve."**

> **Apollo Server version note:** The patterns in this section apply to **Apollo Server 4+** (released October 2022). Apollo Server 3 and earlier had different plugin APIs and schema-building patterns. If you are on Apollo Server 3, the `@deprecated` directive and schema evolution rules are identical, but code examples using the `ApolloServer` constructor or plugin API differ. Check your version before applying implementation patterns.

This is not laziness. It is a deliberate design choice. GraphQL's schema is strongly typed and introspectable, which makes schema evolution safer than REST endpoint changes. But "evolve instead of version" still requires discipline.

### The GraphQL Philosophy: No Versions, Additive Evolution

Instead of `/v2/graphql`, the GraphQL community recommends:

1. **Add new fields and types.** Never remove or rename existing ones without a deprecation cycle.
2. **Mark old fields as deprecated** using the `@deprecated` directive.
3. **Monitor field usage.** Only remove a field after confirmed zero usage.
4. **Use field arguments** to extend behaviour without breaking existing queries.

### The `@deprecated` Directive — Your Primary Tool

```graphql
type User {
  id: ID!

  # Old field — kept for backward compat
  name: String @deprecated(reason: "Use `fullName` instead. Removed after 2027-01-01.")

  # New field — additive, not replacing
  fullName: String!

  # Deprecated nested type
  address: Address @deprecated(reason: "Use `contactInfo.address` instead.")
  contactInfo: ContactInfo
}
```

When a field is marked `@deprecated`:
- The schema still serves it — no breaking change
- GraphQL introspection tools (GraphiQL, Apollo Studio) show it with the deprecation reason
- Your monitoring can track how many queries still use it
- You have a clear migration path to communicate to consumers

### What IS Breaking in GraphQL

Despite the "evolve not version" philosophy, some changes are still breaking:

| Change | Breaking? |
|---|---|
| Remove a field | **YES** — queries referencing it will fail |
| Rename a field | **YES** — equivalent to remove + add |
| Change field type (e.g., `String` to `Int`) | **YES** |
| Make a nullable field non-nullable | **YES** — clients assuming null is valid will break |
| Remove a type | **YES** |
| Remove an enum value | **YES** |
| Add a new non-null field to an **input type** | **YES** — existing mutations won't send it |
| Add a new nullable field to a type | **NO** |
| Add a new optional argument to a field | **NO** |
| Add a new enum value | **RISKY** — exhaustive switch clients may break |
| Add a new type | **NO** |
| Make a non-nullable field nullable | **NO** (widening — safer) |

> **⚠️ Input type changes are the most dangerous in GraphQL.** Adding a required field to an input type breaks every mutation that uses it. Always add new input fields as nullable/optional first, even if you intend them to be required later.

### Schema Evolution Patterns

#### Pattern 1: Additive fields with parallel support

```graphql
# Both fields exist during transition period
type Order {
  id: ID!
  userId: Int! @deprecated(reason: "Use `userUuid`. Int IDs removed 2027-01-01.")
  userUuid: String!   # new UUID-based ID
}
```

#### Pattern 2: Type aliasing during migration

```graphql
type User {
  id: ID!

  # Old flat structure — deprecated
  street: String @deprecated(reason: "Use `address.street`")
  city:   String @deprecated(reason: "Use `address.city`")

  # New nested structure
  address: Address
}

type Address {
  street: String!
  city:   String!
  country: String!
}
```

#### Pattern 3: Field arguments for behavioural evolution

```graphql
type Query {
  # The `format` argument lets new clients opt into the new shape
  # without breaking clients who don't send the argument
  users(format: UserFormat = LEGACY): [User!]!
}

enum UserFormat {
  LEGACY   # old shape, default for backward compat
  V2       # new shape with fullName, contactInfo, etc.
}
```

> **⚠️ Short-term bridge only.** This pattern buys migration time but should not become permanent. Accumulating behaviour-switching enums leads to schema complexity that is hard to reason about. Set a sunset date for the `LEGACY` enum value when you introduce it, and remove it on schedule.

### Monitoring Field Usage — Critical Before Removal

You cannot safely remove a deprecated field without knowing if anyone still uses it:

```javascript
// Apollo Server v3 field usage plugin.
// Apollo Server v4 changed the plugin API — use @apollo/server plugin hooks instead.
const fieldUsagePlugin = {
  requestDidStart() {
    return {
      executionDidStart() {
        return {
          willResolveField({ info }) {
            metrics.increment(
              `graphql.field.${info.parentType}.${info.fieldName}`
            );
          }
        };
      }
    };
  }
};
```

Only remove a deprecated field when its usage has been zero for 30+ consecutive days.

### When GraphQL Versioning IS Justified

Despite the "evolve not version" preference, a versioned endpoint makes sense for:

- **Complete schema redesign** — when the domain model changes so fundamentally that additive evolution would leave the schema incoherent
- **Security overhaul** — when the auth model changes significantly enough that a fresh schema is cleaner
- **Pagination paradigm shift** — changing from offset to cursor-based pagination touches almost every query

In these cases, run two separate GraphQL endpoints:

```
/graphql      ← v1, deprecated
/graphql/v2   ← new schema
```

And apply the same sunset strategy from Section 3.

---

## 6. gRPC / Protobuf Versioning

gRPC uses Protocol Buffers (Protobuf) for serialisation. Protobuf has its own versioning rules that are very different from JSON-based APIs — and getting them wrong causes **silent data corruption**, not obvious errors.

### The Golden Rule of Protobuf: Field Numbers Are Forever

Every field in a Protobuf message has a number. That number is what gets serialised on the wire — **not the field name**. This has a critical implication:

```protobuf
message User {
  string name  = 1;   // Field number 1
  string email = 2;   // Field number 2
  int32  age   = 3;   // Field number 3
}
```

If you remove field 3 (`age`) and later add a **new** field with number 3, old clients who receive the new message will try to interpret the new field as `age` (an int32). **This causes silent data corruption — not an error.**

### Protobuf Backward and Forward Compatibility Rules

| Change | Safe? | Why |
|---|---|---|
| Add a new field with a new field number | **YES** | Old clients ignore unknown fields; new clients use it |
| Remove a field (mark as `reserved`) | **YES — with caution** | You must reserve the number so it can never be reused |
| Rename a field (same number) | **YES** | Field name is irrelevant on the wire |
| Change a field's type (same number) | **DANGEROUS** | Wire types must be compatible or data is corrupted |
| Reuse a removed field number | **NEVER** | Causes silent data corruption in old clients |
| Change from `optional` to `repeated` | **RISKY** | Compatible for scalars, dangerous for messages |

### The `reserved` Keyword — Use It Every Time You Remove a Field

```protobuf
message User {
  // Field 3 (age) was removed in v2.
  // Reserved to prevent accidental reuse — enforced by the compiler.
  reserved 3;
  reserved "age";

  string name  = 1;
  string email = 2;
  // Field 3 is locked. Next new field uses number 4.
  string phone = 4;
}
```

If any engineer tries to add a field with a reserved number, the Protobuf compiler will error. This is a compile-time safety net — use it every single time.

> **Tip:** Enforce this with [buf.build](https://buf.build) — a linter and breaking change detector for Protobuf. Add it to your CI pipeline. It will catch field number reuse, type changes, and other dangerous mutations automatically.

### Package Versioning in gRPC

For significant API changes, introduce a new package:

```protobuf
// Original
package mycompany.users.v1;
service UserService {
  rpc GetUser (GetUserRequest) returns (User);
}

// New major version — different package, different service URL
package mycompany.users.v2;
service UserService {
  rpc GetUser (GetUserRequest) returns (User);
}
```

This maps to different service paths in gRPC:

```
/mycompany.users.v1.UserService/GetUser   ← v1
/mycompany.users.v2.UserService/GetUser   ← v2
```

Your gRPC server can register both implementations simultaneously, giving consumers time to migrate.

### Protobuf Schema Evolution in Practice

```protobuf
// v1 — original message
message Order {
  string id      = 1;
  int32  user_id = 2;
  repeated string items = 3;
  // ⚠️  Never use float/double for money in production — float precision
  // causes rounding errors. Use int64 (store smallest unit, e.g. cents)
  // or a dedicated Decimal/Money message type instead.
  float  total   = 4;
}

// v2 — evolved, fully backward compatible with v1 clients
message Order {
  string id      = 1;

  // Old field kept with same number — old clients still decode correctly
  int32  user_id = 2;

  repeated string items = 3;
  float  total   = 4;

  // New fields — old clients silently ignore these
  string      user_uuid      = 5;
  OrderStatus status         = 6;
  int64       created_at_unix = 7;
}

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;  // always include a zero-value for safety
  PENDING   = 1;
  CONFIRMED = 2;
  SHIPPED   = 3;
}
```

---

## 7. WebSocket / Streaming API Versioning

WebSocket and SSE APIs have unique migration challenges because connections are **long-lived and stateful**. You cannot redirect a mid-flight WebSocket connection the way you redirect an HTTP request.

### WebSocket Versioning Strategies

#### Strategy 1: Version in the Connection URL

```
wss://api.example.com/v1/ws
wss://api.example.com/v2/ws
```

Simple and explicit. Run both endpoints simultaneously during migration.

**Sunset:** Return an HTTP `410 Gone` during the WebSocket handshake (which is a regular HTTP upgrade request — you can inspect and reject it before the connection is established):

```http
HTTP/1.1 410 Gone
Content-Type: application/json

{
  "error": "v1 WebSocket endpoint retired",
  "migrate": "wss://api.example.com/v2/ws",
  "docs": "https://docs.example.com/ws-migration"
}
```

#### Strategy 2: Version in the Subprotocol Header

```http
GET /ws HTTP/1.1
Upgrade: websocket
Sec-WebSocket-Protocol: myapi-v2, myapi-v1
```

The server selects the highest version it supports from the client's offered list:

```http
HTTP/1.1 101 Switching Protocols
Sec-WebSocket-Protocol: myapi-v2
```

This keeps the connection URL stable while the protocol version negotiates. Similar to HTTP content negotiation. Cleaner for clients but more complex to implement server-side.

#### Strategy 3: Version in the Message Envelope

For cases where you cannot control the connection URL, include the version in every message:

```json
{
  "v": "2",
  "type": "user.updated",
  "payload": { "id": "123", "fullName": "Alice" }
}
```

The server reads the `v` field and routes to the appropriate handler. Less clean, but useful for legacy systems where you can't change the handshake.

### Migrating WebSocket Message Schemas

When you need to change the shape of messages over an existing connection:

**Step 1: Additive fields first**

```json
// During transition — server sends both
{
  "type": "order.created",
  "userId": 123,
  "userUuid": "abc-def-ghi",
  "total": 99.99,
  "currency": "USD"
}
// Old clients ignore userUuid and currency. New clients use them.
```

**Step 2: Announce capabilities at connection time**

```json
{
  "type": "connection.established",
  "serverVersion": "2.1.0",
  "deprecations": [
    {
      "field": "userId",
      "reason": "Use userUuid",
      "removedAt": "2027-01-01"
    }
  ]
}
```

**Step 3: Remove old fields only after confirmed zero usage.**

Unlike REST where access logs show field usage, WebSocket message fields require explicit server-side instrumentation — instrument your message handler to record which fields are present in incoming messages. Only remove a deprecated field when its usage metric has been zero for 30+ consecutive days.

### Server-Sent Events (SSE) Versioning

SSE is simpler — the connection is one-way (server → client) and reconnects automatically. Version in the URL:

```
GET /v1/events
GET /v2/events
```

Clients reconnect on disconnect, so migration is straightforward: return an HTTP `301/308` redirect from the v1 SSE endpoint to `/v2/events`. Most SSE clients follow redirects on reconnect.

---

## 7b. Message Queue & Event-Driven API Versioning

Message queues and event-driven systems have a fundamentally different versioning challenge from request-response APIs: **messages are decoupled in time**. A producer and consumer may not be running the same version simultaneously, and a message published today may be consumed hours, days, or weeks later by a different consumer version.

The patterns in this section are primarily written for **Kafka and RabbitMQ**, which have log-based or queue-based semantics with explicit consumer tracking. AWS SNS/SQS and Azure Service Bus behave differently in key ways:

| Platform | Consumer Model | Log Replay | Consumer Lag Metric | Topic Rename Risk |
|---|---|---|---|---|
| **Kafka** | Consumer groups with independent offsets | Yes (retention period) | `consumer_lag` per group/partition | High — never rename |
| **RabbitMQ** | Queue-bound consumers, messages ACK'd/NACK'd | No (messages deleted on consume) | Queue depth | N/A — queues are ephemeral |
| **AWS SNS/SQS** | Fan-out via SNS topics to SQS queues; no consumer groups | No native replay | Queue depth (CloudWatch) | Low — queues are named independently |
| **Azure Service Bus** | Topics + subscriptions; sessions for ordering | No native replay | Active message count | Low |

The schema registry and compatibility mode patterns apply to all platforms, but consumer group management, `consumer_lag` monitoring, and topic versioning strategies are **Kafka-specific**. Adapt accordingly for your platform.

### Why Message Queue Versioning Is Uniquely Hard

| Challenge | What It Means |
|---|---|
| **Temporal decoupling** | A v1 message sitting in the queue today may be consumed by a v2 consumer next week. Both must be able to handle it. |
| **No request-response handshake** | You cannot negotiate a version at connection time. The message must carry its own schema identity. |
| **Fan-out consumers** | A single topic may have dozens of consumers at different versions. You cannot sunset a schema until every consumer has migrated. |
| **Replay and reprocessing** | Old messages in retention or dead-letter queues may be replayed against new consumer code. |

### Core Strategy: Schema-First with a Registry

The most reliable approach for event-driven systems is to version the **schema**, not the topic or queue. Use a schema registry (Confluent Schema Registry for Kafka, AWS Glue Schema Registry, or Apicurio) to enforce compatibility rules at publish time.

**Compatibility modes in schema registries:**

| Mode | Rule | Use When |
|---|---|---|
| `BACKWARD` | New schema can read data written by old schema | Most common — consumers upgrade first |
| `FORWARD` | Old schema can read data written by new schema | Producers upgrade first |
| `FULL` | Both backward and forward | Maximum safety; most restrictive |
| `NONE` | No compatibility enforcement | Only for development environments |

Set compatibility mode per-topic/per-subject. Never disable compatibility enforcement in production.

### Avro, Protobuf, and JSON Schema in Message Queues

| Format | Versioning Mechanism | Notes |
|---|---|---|
| **Apache Avro** | Schema evolution rules: add fields with defaults, never remove or rename | Default field values are mandatory for backward compat — fields without defaults block schema registration in BACKWARD mode |
| **Protobuf** | Field numbers (see Section 6) | Same rules apply in message queues as in gRPC — never reuse a removed field number |
| **JSON Schema** | `additionalProperties: true` and optional new fields | Weakest enforcement; relies on consumer resilience rather than schema compiler guarantees |
| **Plain JSON** | No enforcement | Include a `schema_version` field in the envelope; consumers branch on it |

### The Versioned Envelope Pattern

When you cannot use a schema registry, embed version information in every message:

```json
{
  "schema_version": "2",
  "event_type": "order.created",
  "timestamp": "2026-03-01T12:00:00Z",
  "payload": {
    "orderId": "abc-123",
    "customerUuid": "def-456",
    "totalCents": 4999
  }
}
```

Consumers check `schema_version` and route to the appropriate handler. Add a dead-letter queue for unknown schema versions — do not silently drop messages with unrecognised versions.

### Topic / Queue Versioning Strategies

| Strategy | How | When to Use |
|---|---|---|
| **Same topic, schema evolution** | Evolve the schema additively; use a schema registry for enforcement | Preferred — keeps consumers and producers on the same channel |
| **Parallel topics** | Publish to both `orders.v1` and `orders.v2` during migration window | When schema change is truly breaking and consumers cannot handle both shapes |
| **Consumer group versioning** | Keep old consumer group on old topic; add new consumer group on new topic | Useful in Kafka when you need independent replay positions per version |

> **Kafka Streams and ksqlDB note:** If you use Kafka Streams or ksqlDB, schema evolution also affects state stores and changelog topics. Streams applications materialise intermediate state using internal topics — a schema change that breaks the state store format requires a full state reset (clearing the changelog topic) or a migration of the state store before the new application version can start. Plan for this separately from your event topic migration.

> **Never rename a Kafka topic** as a versioning strategy. Topic rename requires creating a new topic, migrating all consumers, and draining the old topic — equivalent to a full migration anyway, with more operational risk.

### Sunset Strategy for Message Queue Schemas

Sunsetting a message schema is harder than sunsetting an HTTP API endpoint — you cannot inspect traffic by consumer identity easily. Steps:

1. Publish the new schema version. Keep publishing both old and new shapes during migration.
2. Monitor consumer lag on old topic/schema version. The key metric is `consumer_lag` (records-behind) per consumer group per partition — exposed via Kafka JMX and surfaced by tools like Burrow, Kafka UI, or Confluent Control Center. A lag of zero across all partitions for the old schema consumer group is your signal that migration is complete.
3. Coordinate with all consumer teams — get explicit sign-off that each has deployed v2.
4. Stop publishing old schema. Messages in-flight on the old schema will still be consumed by backward-compatible v2 consumers.
5. After retention period expires (messages cycle out of the log), old schema is fully retired.

> **⚠️ Dead Letter Queue (DLQ) risk:** Messages that failed processing and were routed to a DLQ may still carry the old schema. When you retire the old consumer, these DLQ messages become permanently unprocessable. Before sunsetting a schema, audit your DLQs. Either reprocess and republish DLQ messages on the new schema, or accept that they will be dropped.

---

## 7c. SOAP / Legacy XML API Versioning

Many enterprise environments still run SOAP services, and many teams must version them during a migration to REST or gRPC. SOAP has its own versioning conventions.

### SOAP Versioning Strategies

| Approach | Mechanism | Notes |
|---|---|---|
| **WSDL versioning** | Separate WSDL files per version (`UserServiceV1.wsdl`, `UserServiceV2.wsdl`) | Cleanest — clients generate stubs from a specific WSDL version |
| **Namespace versioning** | Change the XML namespace: `xmlns:v1="..."` vs `xmlns:v2="..."` | Forces client regeneration but is self-describing |
| **Endpoint versioning** | Different URL per version: `/soap/v1/UserService`, `/soap/v2/UserService` | Simplest operationally; easy to route at the gateway |
| **SOAPAction header versioning** | Different `SOAPAction` header value per version | Fragile — not all stacks expose SOAPAction easily |

The recommended approach for SOAP is **WSDL versioning + separate endpoints**. For teams on enterprise ESB platforms (IBM MQ, Oracle SOA Suite, TIBCO), also check whether your platform supports **WS-Versioning** — a WS-* extension standard that provides a formal versioning envelope for SOAP messages. It is not widely adopted but is native in some ESB toolchains. Maintain separate WSDL files per version and expose them at versioned URLs. This is the approach most ESBs and API gateways support natively.

### Migrating from SOAP to REST

SOAP-to-REST migrations are a common scenario. The key versioning principle: **run SOAP and REST in parallel** for the full deprecation window. Do not attempt a flag-day cutover. Use an API gateway or adapter layer to forward SOAP requests to the new REST backend during the migration window, translating XML envelopes to JSON. Sunset the SOAP endpoint on the same schedule as any other deprecated API version.

---

## 7d. GraphQL Federation Versioning

In federated GraphQL architectures (Apollo Federation, Hive), each subgraph owns a portion of the schema. Schema versioning in federation has additional constraints.

### How Federation Changes the Versioning Problem

In a monolithic GraphQL server, you control the entire schema. In federation, each subgraph publishes its own schema fragment, and the gateway composes them. A breaking change in one subgraph propagates to the composed supergraph and affects all clients of the gateway — including clients who don't use that subgraph's types directly.

### Federation Versioning Rules

| Rule | Why |
|---|---|
| Schema changes in any subgraph must be backward compatible by default | The gateway exposes a unified schema; a break in one subgraph breaks the gateway |
| Use `@deprecated` in subgraph schemas, not gateway-level versioning | Deprecation propagates to the composed schema and is visible in introspection |
| Coordinate field removals across the gateway team | A field removal in a subgraph must be approved by the gateway team — the supergraph owns the deprecation timeline |
| Run schema checks before every subgraph deployment | Apollo Schema Checks and Hive's schema registry both catch composition-breaking changes pre-deploy |
| Separate subgraph deployments require composition validation | Deploy subgraph schema changes to a schema registry first; validate composition; then deploy the service |

### Subgraph Breaking Change Check Process

1. Propose schema change in the subgraph.
2. Run composition check: does the new subgraph schema still compose with all other subgraphs? **If composition fails, the deploy is rejected and the current supergraph stays active — no client impact.**
3. Run schema check: does the new supergraph break any registered client operations?
4. If checks pass — deploy subgraph, publish new schema to registry.
5. Gateway picks up the new composition automatically (in Apollo Managed Federation / Hive).

> For teams using Apollo Federation, `rover subgraph check` and `rover subgraph publish` are the CLI commands for steps 2-4. For Hive, the equivalent is `hive schema:check` and `hive schema:publish`. Build these into your CI pipeline — never deploy a subgraph schema change without running them.

> **Self-hosted Apollo Router:** If you are running a self-hosted Apollo Router with a static `supergraph.yaml`, the gateway does **not** pick up schema changes automatically. You must regenerate the supergraph schema and redeploy the Router. Only Apollo Managed Federation (RouterOS) polls for schema updates automatically. Know which mode your team is in before assuming zero-downtime schema propagation.


---

## 8. SDK Versioning

When your API has a client SDK, the SDK versioning strategy must be coordinated with the API versioning strategy. Most teams treat this as an afterthought. It should not be.

### SDK Version ↔ API Version Alignment

**Model A: SDK version mirrors API version (tight coupling)**

```
SDK v1.x  →  calls API v1
SDK v2.x  →  calls API v2
```

Clear mapping. SDK major version bump = API major version bump. Simple to reason about.

**Problem:** Clients must upgrade the SDK library to migrate API versions. For languages with strict dependency management (Java, C#), this can require significant effort across many teams.

**Model B: SDK version independent, targets multiple API versions**

```
SDK v3.x  →  can call API v1 or API v2 (configurable)
```

One SDK version supports multiple API versions via a constructor parameter:

```python
# Python SDK example
client = MyApiClient(api_version="v2")   # or "v1" for legacy
user = client.users.get("123")
```

More complex to maintain, but lets consumers stay on a single SDK version while migrating their API version calls incrementally.

### SDK Deprecation Warnings at Runtime

When you deprecate an API version, emit warnings from the SDK so developers see them in their logs:

```javascript
// JavaScript SDK — deprecation warning
class UsersV1 {
  async getUser(id) {
    console.warn(
      '[DEPRECATED] UsersV1.getUser() calls API v1 which is deprecated. ' +
      'Migrate to UsersV2.getUser() by 2027-01-01. ' +
      'See: https://docs.example.com/migrate-v1-v2'
    );
    return this._http.get(`/v1/users/${id}`);
  }
}
```

### Generating SDK Versions from OpenAPI Specs

```bash
# Generate v2 SDK from the v2 OpenAPI spec
openapi-generator generate \
  -i docs/api/openapi-v2.yaml \
  -g typescript-fetch \
  -o sdk/typescript/v2 \
  --additional-properties=npmName=@mycompany/api-sdk,npmVersion=2.0.0
```

Automate this in CI so every spec change triggers an SDK rebuild and patch version bump.

### Deprecating SDK Versions on Package Registries

Old SDK versions persist on registries indefinitely unless you act. When an API version is deprecated, deprecate the SDK too:

```bash
# npm — mark old SDK as deprecated (still works, but shows a warning on install)
npm deprecate @mycompany/api-sdk@"<2.0.0" \
  "API v1 is deprecated. Upgrade to sdk@2.x. See: https://docs.example.com/migrate"

# PyPI — yank old releases (uninstallable for new installs, existing installs unaffected)
# Use the PyPI web UI: Manage → Release → Yank, with a yank reason.

# Maven — add a <deprecated> notice in the POM description for the old version
```

> **Never delete old SDK versions from package registries.** Teams may have pinned to a specific version. Deletion causes immediate build breakage for any project that pins exactly.

### SDK Breaking Change Rules

SDK breaking changes are not always tied to API breaking changes:

| Change | Breaking for SDK Users? |
|---|---|
| Rename a method | **YES** — user code calls the method by name |
| Change method signature (add required param) | **YES** |
| Change return type structure | **YES** |
| Remove a method | **YES** |
| Change error type thrown | **YES** — user catch blocks depend on it |
| Change pagination model (offset → cursor) | **YES** — user iteration code changes |
| Add a new optional method parameter | **NO** |
| Add a new method | **NO** |

---

## 9. Mobile Client Migration

Mobile clients are the hardest consumers to migrate. You cannot force-update them. App store review cycles mean even urgent updates take 1–2 weeks to reach users. Some users never update at all.

### The Fundamental Problem

On web, a deployment instantly updates all users. On mobile:

- Users can ignore updates indefinitely
- Enterprise MDM environments may lock app versions for months
- Older devices may not support newer app versions
- Some users are permanently stuck (device storage full, old OS, etc.)

Plan for **18–24 months** minimum support for major mobile API consumers.

### Minimum Version Enforcement

At some point, you need a way to tell clients they are too old to continue. Do this gracefully:

```http
HTTP/1.1 410 Gone
X-Minimum-App-Version: 3.2.0
X-Current-App-Version: 1.8.0
Content-Type: application/json

{
  "error": "app_version_unsupported",
  "message": "This version of the app is no longer supported. Please update.",
  "minimumVersion": "3.2.0",
  "updateUrl": "https://apps.apple.com/app/myapp"
}
```

Always include `updateUrl` — so the app can show a one-tap "Update Now" button.

### Sending App Version on Every Request

> **Cross-platform apps (React Native / Flutter):** The same version header pattern applies. In React Native, add the header at the `fetch` or Axios request interceptor level. In Flutter, configure it in the Dio HTTP client's interceptors (`dio.interceptors.add(...)`). Both approaches give you a single place to set the header across all requests — equivalent to the native iOS/Android interceptor pattern described below.

Include app version as a global interceptor in your mobile HTTP client — not something individual screens have to remember:

```swift
// iOS — Alamofire session configuration
let headers: HTTPHeaders = [
  "X-App-Version": Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "unknown",
  "X-Platform": "ios",
  "X-OS-Version": UIDevice.current.systemVersion
]
```

```kotlin
// Android — OkHttp interceptor
class AppVersionInterceptor : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val request = chain.request().newBuilder()
            .addHeader("X-App-Version", BuildConfig.VERSION_NAME)
            .addHeader("X-Platform", "android")
            .addHeader("X-OS-Version", Build.VERSION.RELEASE)
            .build()
        return chain.proceed(request)
    }
}
```

### Server-Side Version Routing by App Version

Use the `X-App-Version` header at your gateway to route old app versions to compatibility shims:

```nginx
map $http_x_app_version $api_upstream {
  "~^1\."   user-service-compat;   # app v1.x → compatibility layer
  "~^2\."   user-service-v1;       # app v2.x → API v1
  default   user-service-v2;       # app v3.x and above → API v2
}
```

### Forced Upgrade Strategy

Design this mechanism into your app **before you need it**. Adding it retroactively requires another app release, which defeats the purpose.

| Phase | What Happens | User Experience |
|---|---|---|
| **Soft force** (3 months before EOL) | API returns a warning field in response body | App shows dismissible "Update Available" banner |
| **Hard force** (1 month before EOL) | API returns non-dismissible warning | App shows modal on launch: "Update required by [date]" |
| **Block** (EOL date) | API returns `410 Gone` | App shows: "This version is no longer supported." + Update button |

---

## 10. Multi-Tenant API Versioning

Enterprise SaaS products often have a specific challenge: different customers are contractually locked to different API versions. One customer is on v2. Another is mid-migration to v3. A third hasn't started.

### Per-Tenant Version Pinning

Store each tenant's API version in your tenant configuration:

```json
{
  "tenantId": "acme-corp",
  "apiVersionPin": "v2",
  "pinExpiresAt": "2027-06-01",
  "migrationGuideUrl": "https://docs.example.com/acme-migration"
}
```

Resolve the version at your gateway using the tenant's pin:

```python
# Pseudocode — tenant-aware version resolution
def resolve_api_version(request):
    tenant = get_tenant_from_request(request)   # via API key, subdomain, or JWT
    requested = extract_version_from_path(request.path)  # e.g. "v3" or None

    if requested:
        # Guard: reject if tenant's contract doesn't cover the requested version.
        # Without this, a tenant on v2 pin could silently access v3 data shapes
        # their integration was never tested against.
        if tenant.api_version_pin and version_gt(requested, tenant.api_version_pin):
            raise ApiVersionNotPermitted(
                f"Account pinned to {tenant.api_version_pin}. "
                f"Contact support to enable {requested}."
            )
        return requested

    # Use tenant's pinned version
    if tenant.api_version_pin:
        return tenant.api_version_pin

    # Default to latest stable
    return "v3"
```

> **Edge case:** A tenant on v2 pin accidentally calling a `/v3/` endpoint should get a clear error — not silent routing to v3. Their contract and integration testing may not cover v3 data shapes.

### Tracking Migration Commitments

Build a dashboard (or at minimum a spreadsheet) so these commitments are visible to your platform team:

| Tenant | Current Version | Target Version | Migration Deadline | DRI |
|---|---|---|---|---|
| Acme Corp | v2 | v3 | 2027-03-01 | @alice |
| GlobalCo | v1 | v3 | 2026-12-01 | @bob |
| StartupX | v3 | v3 | — | Current |

### Set a Hard Maximum on Concurrent Versions

The longer you maintain per-tenant version pins, the more divergence accumulates in your codebase. Set a hard rule: **no more than 3 API versions in active circulation at any time.** When v4 is released, v1 must be fully retired, regardless of individual tenant timelines. Build this into customer contracts upfront.

---

## 11. Consumer-Driven Contract Testing

Consumer-driven contract testing (CDCT) turns "hope everyone migrated" into an automated, CI-enforced guarantee.

### How It Works — The Simple Version

Each consumer publishes a contract saying exactly what they need from your API. Your CI pipeline runs those contracts against your latest code before every deployment. If your changes break a consumer's expectations, the build fails — before anything reaches production.

| Concept | In Plain English |
|---|---|
| **Consumer** | Any client that calls your API — mobile app, partner service, internal microservice |
| **Provider** | Your API |
| **Pact / Contract** | A file listing what the consumer expects: endpoints, fields, status codes |
| **Pact Broker** | A shared server where consumers publish contracts and providers fetch them |
| **Verification** | The provider runs consumer contracts against a live test instance. Pass = safe to deploy. |

### What a Pact Contract Looks Like (REST)

```json
{
  "consumer": { "name": "orders-service" },
  "provider": { "name": "user-service" },
  "interactions": [
    {
      "description": "get user by ID",
      "request":  { "method": "GET", "path": "/v2/users/123" },
      "response": {
        "status": 200,
        "body": {
          "id": "123",
          "fullName": "Alice Chen",
          "email": "alice@example.com"
        }
      }
    }
  ]
}
```

If `user-service` renames `fullName` to `name`, this pact fails in CI. The breaking change is caught before it ships.

### The CDCT Workflow

1. **Consumer writes a pact** describing what it needs.
2. **Consumer publishes the pact** to the Pact Broker (automated in CI on commit).
3. **Provider pulls all pacts** from the Broker and verifies against its current codebase.
4. **CI fails fast** if any pact is broken.
5. **"Can I deploy?"** — Pact Broker's `can-i-deploy` command confirms every consumer is compatible before release.

### Contract Testing for Non-REST APIs

Pact supports more than REST:

- **GraphQL:** Use [pact-graphql](https://docs.pact.io/implementation_guides/graphql) — contracts describe query/mutation shapes and expected response structures.
- **gRPC:** Use [pact-protobuf-plugin](https://docs.pact.io/plugins/protobuf) — contracts describe Protobuf message shapes.
- **Message queues (async):** Pact supports async contracts for event-driven systems.

### Tool Recommendation

[Pact (pact.io)](https://pact.io) — industry standard, supports Node.js, Java, Python, Go, Ruby, .NET, and more.

- **Self-hosted Pact Broker** — free, works for most teams. Quick start: `docker run -p 9292:9292 pactfoundation/pact-broker`. Consumers publish pacts via `pact-broker publish ./pacts --broker-base-url http://localhost:9292`. Providers verify with `pact-broker can-i-deploy` in their CI pipeline.
- **PactFlow** (paid) — adds UI, webhooks, dependency graph of all consumer/provider relationships, enterprise SSO

> **⚠️ CDCT does not protect you from unregistered consumers** — clients who never wrote a contract. That is why the sunset phase must also monitor actual traffic.

---

## 12. Graceful Degradation

Even with sunset strategies and contract tests, old clients will sometimes hit new API behaviour. Graceful degradation is your safety net.

### Three Tiers

| Tier | Strategy | Example |
|---|---|---|
| **Tier 1 — Ignore and default** | Unknown fields silently ignored. Missing optional fields use defaults. | Old client omits `region` → server defaults to `us-east-1` |
| **Tier 2 — Redirect and explain** | Old version returns `301/308` with a `Link` header to migration docs. | `GET /v1/orders` returns `308 Permanent Redirect` |
| **Tier 3 — Translate at the gateway** | An adapter layer converts old request shapes to new ones transparently. | Gateway rewrites `name` → `fullName` for v2 backend |

### The Adapter / Translation Layer Pattern

```
Old Client  →  POST /v1/users  { "name": "Alice" }
                     ↓
  Adapter   →  POST /v2/users  { "fullName": "Alice" }   ← field translated
                     ↓
 v2 Service  →  processes normally
                     ↓
  Adapter   ←  response back-translated: { "name": "Alice" }
                     ↓
Old Client  ←  receives expected v1 shape
```

Most useful when:
- Mobile clients cannot be updated quickly
- Enterprise partners need 6+ months transition time
- Large number of internal legacy consumers

> **⚠️ Set a sunset date for the adapter itself on day one.** Adapter layers that outlive their purpose become permanent, and in two years nobody knows why they exist.

### Feature Flags as a Safety Valve

- **Consumer-targeted rollout:** Enable new behaviour only for Consumer A while Consumer B stays on the old shape.
- **Canary testing:** Route 5% of traffic to the new shape, watch error rates, increment to 20% → 100%.
- **Instant rollback:** Flip the flag off — no deployment needed, 30-second recovery.

> **ℹ️ Feature flags are a deployment mechanism. Versioning is a contract mechanism. Use both.**

---

## 12b. Rollback Strategy — Going Back Fast

Every migration plan must include a rollback plan. "We'll figure it out if something breaks" is not a plan.

### The Three Rollback Tiers

| Tier | Speed | Method |
|---|---|---|
| **Tier 1 — Feature flag flip** | < 30 seconds | Flip the feature flag off. All traffic instantly returns to v1. No deployment needed. Design for this tier. |
| **Tier 2 — Gateway re-route** | 1–5 minutes | Update API gateway routing config. Requires a gateway config deploy or admin API call. |
| **Tier 3 — Service rollback** | 5–30 minutes | Roll back the v2 service deployment. Standard `kubectl rollout undo` or equivalent. |

### Rollback Pre-conditions Checklist

Verify these **before** v2 goes live:

- [ ] v1 service is still running and reachable — do not decommission on launch day
- [ ] Gateway routing config is version-controlled and revertable in one command
- [ ] Feature flag exists to toggle v2 off (if applicable)
- [ ] On-call engineer knows the rollback command — it is in the runbook, not just in someone's head
- [ ] Database migrations are backward compatible — v1 code can still read data written by v2 code

### The Rollback Boundary: Database Schema

The hardest part of API rollback is the database. If v2 includes a schema change, rolling back the API does **not** automatically roll back the data. Use the **expand/contract pattern**:

1. **Expand:** Add new columns/tables alongside old ones. Both v1 and v2 code can run simultaneously.
2. **Migrate:** Move data to the new shape during the migration window. Both old and new columns populated.
3. **Contract:** Once v2 is confirmed stable and v1 is sunset, remove old columns.

Rolling back from step 2 to step 1 is always safe — v1 code never encounters missing columns.

> See also: `database-migrations.md` in this repo for the full expand/contract pattern.


---

## 12c. Monitoring During a Migration

You have deployed v2 and migration is active. What exactly should you be watching?

**For event-driven / message queue APIs**, the equivalent version-traffic metric is consumer lag. Track `consumer_lag` (records-behind) per consumer group per partition in Kafka, or queue depth in SQS/Service Bus. When migrating from schema v1 to v2, lag on the v1 consumer group trending toward zero is your signal that migration is complete — equivalent to watching version-tagged request traffic fall on an HTTP API.

### Core Metrics — Track Per API Version (HTTP / gRPC)

| Metric | Why It Matters |
|---|---|
| **Request volume by version** | Is old version traffic declining? This is your migration progress gauge. |
| **Error rate (4xx/5xx) by version** | A spike in v2 errors right after launch is your rollback trigger. |
| **Latency p50/p95/p99 by version** | Regressions show up here before error rates climb. |
| **Consumer breakdown by version** | Which specific consumers are still on v1 at week 4? |

### Example Prometheus Queries

```promql
# Request rate by API version
sum(rate(http_requests_total{job="api-gateway"}[5m])) by (api_version)

# Error rate by API version
sum(rate(http_requests_total{job="api-gateway",status=~"5.."}[5m])) by (api_version)
/ sum(rate(http_requests_total{job="api-gateway"}[5m])) by (api_version)

# p99 latency by version
histogram_quantile(0.99,
  sum(rate(http_request_duration_seconds_bucket{job="api-gateway"}[5m]))
  by (le, api_version)
)
```

### Pre-Define Your Rollback Triggers

Agree on these thresholds **before** you deploy — not during an incident:

| Trigger | Example Threshold | Action |
|---|---|---|
| v2 error rate spike | > 1% sustained for 5 min | Auto-rollback via feature flag |
| v2 p99 latency regression | > 2× baseline for 10 min | Page on-call, manual rollback decision |
| Data inconsistency reported | Any consumer reports wrong data | Immediate halt, investigation |
| Old version traffic stalling | < 10% reduction after 30 days | Escalate — direct outreach to lagging consumers |

> **Write rollback triggers in the migration plan. Not in the post-mortem.**

---

## 13. What I've Seen Go Wrong in Production

### Failure Pattern 1: The Forgotten Internal Consumer

**The situation:** The team deprecates v1 with a 6-month window and notifies all external partners. On sunset day, they remove the v1 routing. Within 4 minutes, a cascade of failures hits the payments service. An internal data pipeline — written 2 years ago by an engineer who had since left — was still calling v1 endpoints. Nobody had updated the service registry. No consumer contract existed for it.

**The fix:** 45 minutes of emergency rollback. New rule: before any version sunset, run a 30-day traffic analysis by consumer identity. Direct-notify any consumer still calling the deprecated version — not just a Slack announcement.

---

### Failure Pattern 2: The Silent Field Type Change

**The situation:** A backend team changed a `userId` field from integer to string (UUID) without bumping the API version — they considered it a non-breaking "internal change." The mobile app, which was comparing `userId`s as numbers, started getting silent comparison failures. User A could no longer see their own orders. The bug took 3 weeks to surface because it only appeared on accounts created after the migration date.

**The lesson:** Any field type change is a breaking change. ID fields especially — integers vs strings vs UUIDs always need a new API version and explicit migration communication.

---

### Failure Pattern 3: The Sunset Date Nobody Owned

**The situation:** The team published a migration guide with a sunset date 12 months out. The engineer who wrote the doc left 4 months later. The Jira ticket to "remove v1 routing" sat in the backlog unowned. 14 months after the announced sunset date, v1 was still running — two parallel codepaths, two sets of monitoring, two deployment pipelines. When the team finally tried to shut it down, nobody had done a traffic analysis in over a year.

**The fix:** Every sunset date must have a named DRI and an automated enforcement mechanism — a pipeline alert that fires when the deprecated version hasn't been removed by the target date.

---

### Failure Pattern 4: The Protobuf Field Number Reuse

**The situation:** A gRPC service removed a field and — not knowing the `reserved` rule — reused its field number for a new field six months later. Old client versions that hadn't fully migrated started silently receiving corrupted data. No errors were thrown. The wrong data was being cast to the wrong type. An audit uncovered the cause weeks later.

**The lesson:** In Protobuf, field numbers are permanent. Always use `reserved` when removing a field. Enforce this with a CI linter — [buf.build](https://buf.build) has this check built in.

---

### Failure Pattern 5: The GraphQL Input Type Trap

**The situation:** A team added a new non-nullable field to a GraphQL input type during what they thought was a minor schema update — "it has a server-side default, so it's fine." But the generated TypeScript SDK marked the field as required. Every client on an older SDK version that hadn't regenerated their types started getting TypeScript compile errors after an automated SDK update. The fix required reverting the schema change and issuing an emergency SDK patch.

**The lesson:** Adding a non-null field to a GraphQL input type is always breaking — even with a server-side default. Add new input fields as nullable first. Add server-side validation for the requirement separately.

---

### Failure Pattern 6: The Perfect Migration Nobody Knew About

**The situation:** The team did everything right technically. Deprecation headers were set. Emails were sent to the technical contacts on file. The migration guide was published. The sunset gate was enforced on traffic data. On sunset day, everything went cleanly. Then at 11:30pm, the VP of a key enterprise partner called the CTO. Their integration had broken. The technical contact on file had left the company 3 months earlier. The new engineer had never seen the migration emails. The developer portal was behind a login the new engineer didn't have yet.

**The lesson:** Technical correctness does not equal successful communication. For enterprise or business-critical consumers: (1) maintain a contact list that includes both technical and business stakeholders — not just the original integration developer, (2) require explicit *acknowledgement* of deprecation notices, not just delivery, (3) add a "migration confirmed" gate to your sunset checklist — don't just check traffic, check that the consumer has told you they are migrated.

---

### Failure Pattern 7: The Spring Boot 3 → 4 Upgrade That Broke Version Routing

**The situation:** A team upgrading from Spring Boot 3.x to Spring Boot 4 had a working custom `RequestCondition` implementation that matched the `X-API-Version` header and routed to v1 or v2 controller methods. After the upgrade, they enabled `configureApiVersioning()` with `.useRequestHeader("API-Version")`. Suddenly, some requests were hitting the wrong handler — both the new native versioning and the old `RequestCondition` were evaluating on the same request, creating ambiguous matches. In some cases, requests for `v2` were being served by the v1 handler. No error was thrown — just silent wrong behaviour.

**The root cause:** Spring Framework 7's native versioning integrates at the request-mapping resolution level. A custom `RequestCondition` operating on the same header creates a duplicate routing decision. The framework does not automatically disable or replace custom conditions when native versioning is configured.

**The fix:** When upgrading to Boot 4 and enabling native versioning, remove all custom `RequestCondition` implementations that were handling version routing. Migrate their logic to the `version` attribute on `@GetMapping`/`@PostMapping`. Do this in the same PR as the `configureApiVersioning()` setup — not as a separate cleanup task, or you will have a window of double-routing in production.

---

> **These are not edge cases.** Every team hits at least one of these in their first major API migration. The engineers who have seen these before are the ones who build the safeguards proactively.

---

## 14. API Migration Execution Checklist

Use this before the work starts — not during the incident.

**How to use:** Copy this checklist into your team's project tracker (Jira, Linear, Notion). Assign a DRI to each item. The phase gates are sequential — do not start Phase 2 until Phase 1 is complete. This is a team artifact, not a personal to-do list.

### Phase 1: Before You Write a Line of Code

- [ ] Identify all consumers (traffic analysis + service registry + SDK download stats)
- [ ] Classify consumers: internal / external partner / public / mobile
- [ ] Confirm the change is genuinely breaking (use Section 2 tables)
- [ ] Choose versioning strategy appropriate to your API type (REST / GraphQL / gRPC / WebSocket / Message Queue / SOAP)
- [ ] Define support window per consumer type
- [ ] Assign a DRI with an explicit sunset calendar event
- [ ] Check for contractual SLA obligations on enterprise/partner consumers
- [ ] Review for auth changes — loop in security team if affected

### Phase 2: Building the New Version

- [ ] Build behind a feature flag or separate route — do not break existing version during development
- [ ] Write consumer contracts (Pact) for all registered consumers
- [ ] Add deprecation signals from day one of new version GA
- [ ] Write the migration guide before announcing the sunset date
- [ ] Version your OpenAPI / Protobuf spec files alongside the API change
- [ ] **Framework-specific pre-flight:**
  - Spring Boot 4: confirm `addSupportedVersions()` includes the new version before deploying
  - Spring Boot 4: if using `usePathSegment`, verify `configurePathMatch` prefix is set
  - DRF: confirm the new version string is in `ALLOWED_VERSIONS` in `settings.py`
  - ASP.NET Core: add `[ApiVersion("x.y")]` to the controller and verify `ReportApiVersions = true`
  - NestJS: confirm `enableVersioning()` is called in `main.ts` before `app.listen()`
  - Laravel: confirm the new prefix route group file exists and is loaded in `api.php`
- [ ] Update or regenerate SDK versions; add runtime deprecation warnings
- [ ] For gRPC: mark removed fields as `reserved`; run buf.build linter
- [ ] For GraphQL: add `@deprecated` directives; set up field usage tracking
- [ ] Set up separate dashboards for old and new version traffic by consumer

### Phase 3: Migration Active

- [ ] Announce sunset date via email, docs, and machine-readable deprecation signals
- [ ] Direct-notify any consumer still on old version after 50% of support window has passed
- [ ] Track old version traffic weekly — do not shut down on calendar alone
- [ ] Run consumer contract verification in CI on every deployment
- [ ] Monitor SDK version distribution — are consumers downloading the new SDK?

### Phase 4: Sunset Execution

- [ ] Confirm old version traffic is below agreed threshold (e.g., < 0.5%)
- [ ] Send final 2-week warning to all remaining consumers
- [ ] Switch old version to return `410 Gone` (or equivalent rejection for non-HTTP APIs)
- [ ] Monitor for unexpected error spikes for 48 hours
- [ ] Remove old version code after 30-day observation period
- [ ] Deprecate and archive the old OpenAPI / Protobuf spec
- [ ] Write a brief post-migration summary: what worked, what didn't, what to improve next time

---

## 14b. If You're the Consumer — What to Do When Your API Is Changing

Most of this document is written from the provider's perspective. But if *you* are the consumer of an API that is changing, here is what to do.

### When You Receive a Deprecation Header

```http
HTTP/1.1 200 OK
Deprecation: true
Sunset: Sat, 31 Dec 2026 23:59:59 GMT
Link: <https://docs.example.com/migrate>; rel="deprecation"
```

Do not ignore this. Set up tooling in your HTTP client to detect and log these headers automatically:

```javascript
// Axios interceptor — detect and log deprecation headers globally
axios.interceptors.response.use((response) => {
  if (response.headers['deprecation']) {
    const sunset = response.headers['sunset'];
    const link   = response.headers['link'];
    console.warn(
      `[API DEPRECATION] ${response.config.url} is deprecated. ` +
      `Sunset: ${sunset}. Migration guide: ${link}`
    );
    metrics.increment('api.deprecated_endpoint_called', { url: response.config.url });
  }
  return response;
});
```

### Your Consumer Migration Checklist

- [ ] Wire up deprecation header detection in your HTTP client — once, globally
- [ ] Set a calendar reminder 30 days before the sunset date
- [ ] Read the migration guide on the day you first see the deprecation warning — not the week before sunset
- [ ] Write Pact contracts for what you need from the new API version
- [ ] Test against the new version in staging before touching production
- [ ] Notify the API provider when your migration is complete — they are tracking you

### If You Cannot Migrate in Time

Be transparent early. Contact the API provider as soon as you know you will miss the sunset date. Most teams would rather extend the window than deal with a production incident. What they cannot help with is silence.

If you are an enterprise customer with a contractual SLA, check your agreement — you may have rights to an extended support window.

---

## 15. API Type Comparison

Which migration strategies apply to which API type?

**Table A — Request-Response & Streaming APIs**

| Strategy | REST | GraphQL | gRPC | WebSocket | SSE |
|---|---|---|---|---|---|
| URL versioning (`/v1/`, `/v2/`) | ✅ Primary | ⚠️ Rare | ❌ N/A | ✅ Works | ✅ Works |
| Header versioning | ✅ Supported | ⚠️ Rare | ⚠️ Via gRPC metadata | ✅ Subprotocol negotiation | ❌ N/A |
| Schema evolution (additive) | ✅ Recommended | ✅ **Primary** | ✅ Recommended | ✅ Additive messages | ✅ Works |
| `@deprecated` / `reserved` fields | ⚠️ OpenAPI `deprecated: true` | ✅ Native `@deprecated` | ✅ Native `reserved` | ❌ No standard | ❌ No standard |
| Consumer contract testing (Pact) | ✅ Well supported | ✅ Supported | ✅ Via plugin | ⚠️ Limited | ⚠️ Limited |
| Feature flags | ✅ | ✅ | ✅ | ✅ | ✅ |
| Sunset / 410 Gone | ✅ HTTP standard | ✅ HTTP level | ✅ Connection rejection | ✅ Handshake rejection | ✅ HTTP redirect |
| Adapter / translation layer | ✅ Common | ✅ Common | ✅ Common | ⚠️ Complex | ✅ Works |
| SDK versioning | ✅ | ✅ | ✅ | ✅ if SDK exists | ✅ if SDK exists |

**Table B — Async, Queue-Based & Legacy APIs**

| Strategy | Message Queue / Event | SOAP / Legacy XML |
|---|---|---|
| URL / endpoint versioning | ❌ N/A — topic-based | ✅ Versioned endpoint path |
| Schema versioning | ✅ **Primary approach** — schema registry | ✅ Versioned WSDL file |
| Field deprecation mechanism | ⚠️ Schema registry compatibility rules | ⚠️ WSDL `deprecated` annotation |
| Consumer contract testing | ⚠️ Pact async message contracts | ❌ Limited tooling |
| Feature flags | ✅ | ✅ |
| Sunset mechanism | ⚠️ Stop publishing; drain topic; retire after retention | ✅ HTTP 410 on SOAP endpoint |
| Adapter / translation layer | ⚠️ Consumer-side adapter pattern | ✅ Common — SOAP→REST gateway |
| Generated client stubs | ✅ Schema-registry generated | ✅ WSDL-generated stubs |

### "Which versioning approach should I use?" — Decision Tree

```
What type of API are you versioning?
│
├── REST API
│   ├── Public / external? ──────────────── URL versioning (/v1/, /v2/)
│   ├── Internal services? ──────────────── Header versioning (X-API-Version)
│   └── Experimental / internal only? ───── Query param (?version=2)
│
├── GraphQL API
│   ├── Additive change? ────────────────── Schema evolution + @deprecated. No new version needed.
│   ├── Input type change? ──────────────── Add as nullable only. Never add non-null to input types.
│   └── Major redesign? ─────────────────── New endpoint (/graphql/v2) + full sunset strategy
│
├── gRPC API
│   ├── Additive change? ────────────────── New field with new field number. Backward compatible.
│   ├── Removing a field? ───────────────── Mark as `reserved`. NEVER reuse the field number.
│   └── Major redesign? ─────────────────── New package version (mycompany.service.v2)
│
├── WebSocket API
│   ├── New version? ────────────────────── New URL (wss://api/v2/ws)
│   └── Protocol negotiation? ───────────── Sec-WebSocket-Protocol subprotocol header
│
├── SSE / Streaming
│   └── New version? ────────────────────── New URL (/v2/events)
│
├── Message Queue / Event-Driven
│   ├── Schema change? ──────────────────── Schema evolution with registry (BACKWARD compat mode)
│   └── Breaking schema change? ─────────── Parallel topics (orders.v1 / orders.v2) + dual publish
│
└── SOAP / Legacy XML
    ├── Internal / controlled clients? ───── Separate WSDL + versioned endpoint (/soap/v2/...)
    └── Migrating to REST? ──────────────── Adapter layer (SOAP→REST gateway) + full deprecation window
```

---

## 16. Summary

| Principle | Why It Matters |
|---|---|
| **This isn't just REST.** | GraphQL, gRPC, WebSocket, and streaming APIs all have distinct versioning models. Apply the right strategy for the right type. |
| **Check your framework first.** | Spring Framework 7 / Spring Boot 4, ASP.NET Core, NestJS, and DRF all ship native versioning support. Reach for a framework primitive before writing custom routing logic. |
| **Message queues version schemas, not endpoints.** | In event-driven systems, the schema is the contract. Use a schema registry with compatibility enforcement (BACKWARD mode by default). Audit DLQs before sunsetting. Never rename a topic as a versioning strategy. |
| **Every API is a contract.** | Breaking it without warning erodes trust, causes incidents, and costs real engineering time across multiple teams. |
| **Not every change is breaking.** | Use the breaking-change tables in Section 2. Over-versioning is almost as costly as under-versioning. |
| **GraphQL: evolve, don't version.** | Use `@deprecated`, additive fields, and field usage monitoring. Introduce a new endpoint only for complete redesigns. |
| **Protobuf field numbers are permanent.** | Never reuse a removed field number. Always use `reserved`. Enforce with buf.build in CI. |
| **Mobile clients need more time.** | Plan 18–24 months minimum. Design forced-upgrade mechanisms into the app before you need them. |
| **Sunset dates need ownership.** | A date without an automated gate and a named DRI is a suggestion, not a commitment. |
| **Test contracts, don't assume them.** | Pact gives you CI-enforced confirmation that your migration doesn't break registered consumers. |
| **Monitor traffic, not calendars.** | Never sunset a version because the date arrived. Sunset it because the traffic data says it is safe. |

> **The single most important principle:** Forgotten consumers, unnamed owners, reused Protobuf field numbers, and un-enforced sunset dates cause more migrations to fail than bad code. Technical correctness is necessary but not sufficient.

---

---

## 17. Related Documents & Further Reading

### In This Repo

| Document | Why It's Relevant |
|---|---|
| [`database-migrations.md`](./database-migrations.md) | Expand/contract pattern, zero-downtime schema changes, and backfill strategies — essential reading alongside the Rollback section above |
| [`monolith-to-microservices.md`](./monolith-to-microservices.md) | API versioning becomes more complex when APIs are mid-extraction from a monolith — the Strangler Fig pattern has direct implications for your versioning strategy |
| [`event-driven-migration.md`](./event-driven-migration.md) | Event schema evolution and Kafka consumer group migration in depth. **Note:** Section 7b of this document covers event-driven *API versioning* (schema registries, compatibility modes, topic strategies). The separate `event-driven-migration.md` covers the broader migration *execution* — Kafka consumer group rebalancing, state store migrations, and cross-datacenter event replication during migrations. They complement each other; read 7b first. |
| [`api-management.md`](../standards/api-management.md) | The standards governing API design in this org — versioning decisions should align with these |

### External References

| Resource | What You'll Learn |
|---|---|
| [Pact documentation](https://docs.pact.io) | Full consumer-driven contract testing guide |
| [buf.build](https://buf.build) | Protobuf linting, breaking change detection, schema registry |
| [Google API Design Guide — Versioning](https://cloud.google.com/apis/design/versioning) | How Google approaches API versioning at scale |
| [Stripe API versioning blog](https://stripe.com/blog/api-versioning) | Stripe's per-customer date-based versioning model — a real-world alternative to v1/v2 naming |
| [OpenAPI Specification](https://spec.openapis.org/oas/latest.html) | The spec behind OpenAPI/Swagger |

---
