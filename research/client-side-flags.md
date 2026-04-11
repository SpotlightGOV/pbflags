# Research: Client-Side Access to Feature Flags

**Date:** 2026-04-05
**Status:** Research / Not yet planned

## Problem Statement

Applications need browser/frontend access to feature flag values, but exposing
the flag evaluation service directly to the internet is unacceptable. Client-side
access must flow through first-party APIs with proper authentication.

pbflags today is server-side evaluation only. Clients (Go, Java) call
`FlagEvaluatorService` via Connect RPC and receive resolved values — never rules
or targeting logic. There is no browser/frontend SDK, no auth on the evaluator,
and deployment assumes a trusted network.

## Industry Survey

### Flipt: Client-Side Evaluation via WASM

Flipt ships the entire ruleset — flags, segments, rules, constraints — to the
browser. A Rust-compiled WASM engine evaluates locally. Namespace scoping is the
only boundary between flag sets.

- **What the browser receives:** Full flag rules and segments for the namespace
- **Auth model:** Single token type, no client/server distinction
- **Rule visibility:** All targeting logic visible in browser network traffic
- **No payload encryption or rule filtering**

**Assessment:** Architecturally unsuitable for the "no direct internet exposure"
constraint. You'd need to build your own server-side proxy in front of Flipt to
get the desired security properties.

### LaunchDarkly: Server-Side Evaluation with Scoped Credentials

The most mature client/server split in the industry.

Three credential types:

| Credential     | Who uses it          | What it gets                              |
|----------------|----------------------|-------------------------------------------|
| SDK Key        | Server-side SDKs     | Complete flag ruleset (rules, segments)   |
| Mobile Key     | Mobile client SDKs   | Only flags marked "available to mobile"   |
| Client-Side ID | Browser/edge SDKs    | Only flags marked "available to client"   |

- Client-side SDKs send user context to LD servers (or self-hosted Relay Proxy)
- **Server evaluates flags, returns only the variation value** — never rules
- Per-flag toggle controls client-side visibility
- Optional HMAC "secure mode" prevents user context impersonation
- Edge SDKs (Cloudflare, Akamai, Fastly, Vercel) move evaluation to CDN workers

**Relay Proxy:** Self-hosted Go process. Proxy mode: SDKs connect to it as if it
were LD cloud. Daemon mode: writes to Redis/DynamoDB, server SDKs read directly.

### Unleash: Server-Side Evaluation via Edge Proxy

Clear two-tier token model:

| Token type     | Secret? | What it accesses                          |
|----------------|---------|-------------------------------------------|
| Backend token  | Yes     | Full flag configurations for evaluation   |
| Frontend token | No      | Only enabled flags for the given context  |

- Frontend SDKs hit the Unleash Edge proxy (written in Rust)
- **Edge evaluates server-side, returns only enabled flags + variant values**
- User context / PII never leaves your infrastructure
- Frontend tokens are safe to expose publicly, combined with CORS

### GrowthBook: Client-Side with Mitigations

Ships the ruleset to the browser but adds:
- Payload encryption (client-side decryption — obfuscation, not true security)
- SHA-256 hashing of sensitive targeting attributes
- Hidden experiment/variant names

Raises the bar for casual inspection but not robust against determined attackers.

## Pattern Comparison

| Dimension                          | Flipt         | LaunchDarkly    | Unleash          |
|------------------------------------|---------------|-----------------|------------------|
| Where browser evaluation happens   | Client (WASM) | Server          | Server (Edge)    |
| What browser receives              | Full rules    | Variation only  | Enabled flags    |
| Flag rules visible to browser?     | Yes           | No              | No               |
| Distinct client/server credentials | No            | Yes (3 types)   | Yes (2 types)    |
| Per-flag client visibility control | No (ns only)  | Yes             | Yes (project)    |
| Self-hosted proxy/edge             | No            | Yes             | Yes              |
| Secure mode / request signing      | No            | Yes (HMAC)      | No               |
| Bootstrap / SSR support            | No            | Yes             | No               |

## Viable Patterns for pbflags

Given the constraint — first-party APIs only, no direct internet exposure — two
patterns are viable. Both keep flag evaluation server-side.

### Pattern 1: Evaluation Proxy (Unleash Edge model)

```
Browser → First-Party API (authed) → pbflags evaluator → resolved values
```

The browser never talks to pbflags directly. The application's API authenticates
the user, constructs the entity ID, calls `Evaluate()` or `BulkEvaluate()`, and
returns the resolved values.

**Pros:**
- Zero new pbflags infrastructure — this works today
- Flag rules never leave the trusted network
- The application's API owns authentication and authorization
- Simple to reason about

**Cons:**
- Every flag check is a network round-trip through the application API
- No offline or bootstrap story
- Kill switch latency depends on how often the frontend polls

### Pattern 2: Bootstrap + Hydration (SSR model)

```
Browser requests page → Server calls BulkEvaluate(entity_id)
  → Embeds {flag: value} map in initial HTML/payload
  → Client JS reads from embedded data
  → Periodic refresh via first-party API
```

The server evaluates all relevant flags at page render time and embeds results
in the page. For SPAs, this becomes an initial payload that the client caches
and refreshes periodically through the first-party API.

**Pros:**
- No layout shift — first render is correct
- Flag rules stay server-side
- Can be cached aggressively
- Works naturally with SSR frameworks (Next.js, etc.)

**Cons:**
- Values can go stale between refreshes
- Kill switch latency = refresh interval
- Requires server rendering or an initial API call

### Shared Requirement: Client Flag Set

Both patterns need a concept of **which flags are safe to expose to frontends**.
This is the LaunchDarkly per-flag toggle idea applied to pbflags.

## Proposed Design Direction

Three additions to pbflags, when this work is picked up:

### 1. Proto-level annotation for client visibility

```protobuf
message Notifications {
  option (pbflags.feature) = { id: "notifications" };

  bool email_enabled = 1 [(pbflags.flag) = {
    default: true,
    layer: LAYER_USER,
    client_visible: true   // <-- new
  }];

  string internal_routing_key = 2 [(pbflags.flag) = {
    default: "default",
    layer: LAYER_GLOBAL
    // client_visible defaults to false — not exposed to frontends
  }];
}
```

Source-of-truth (proto) declares which flags are browser-safe. Code review
catches accidental exposure. Codegen can enforce the boundary.

### 2. Bulk evaluation endpoint for client flags

A new RPC that evaluates all client-visible flags for an entity in one call:

```protobuf
rpc EvaluateClientFlags(EvaluateClientFlagsRequest)
    returns (EvaluateClientFlagsResponse);

message EvaluateClientFlagsRequest {
  string entity_id = 1;
}

message EvaluateClientFlagsResponse {
  map<string, Value> flags = 1;
  google.protobuf.Timestamp evaluated_at = 2;
}
```

Designed to be called by the application's first-party API during page render
or as a polling endpoint. Returns only client-visible flags.

### 3. No new public-facing service

The application's existing authenticated API wraps the `EvaluateClientFlags`
call. pbflags stays on the internal network. Auth, rate limiting, and access
control remain the application's responsibility.

```
┌──────────┐     ┌───────────────────┐     ┌──────────────────┐
│ Browser  │────▶│ App API (authed)  │────▶│ pbflags evaluator│
│          │◀────│ /api/flags        │◀────│ (internal)       │
└──────────┘     └───────────────────┘     └──────────────────┘
                  You own this layer        Never internet-facing
```

## Open Questions

- Should `client_visible` be a flag-level or feature-level annotation?
- Should the bulk endpoint support etag/conditional responses for efficient polling?
- Is there value in a lightweight TypeScript package that handles polling +
  caching on the client side (consuming the first-party API, not pbflags directly)?
- Should kill switches get a faster propagation path to frontends (SSE/WebSocket
  from the first-party API)?

## References

- [Flipt Client-Side Evaluation](https://docs.flipt.io/v1/integration/client)
- [LaunchDarkly Client vs Server SDKs](https://launchdarkly.com/docs/sdk/concepts/client-side-server-side)
- [LaunchDarkly Relay Proxy](https://launchdarkly.com/docs/sdk/relay-proxy/use-cases)
- [Unleash Frontend API](https://docs.getunleash.io/concepts/front-end-api)
- [Unleash Edge](https://docs.getunleash.io/reference/unleash-edge)
- [GrowthBook Client-Side Flagging](https://blog.growthbook.io/client-side-feature-flagging/)
- [OpenFeature SDK Architectures](https://openfeature.dev/blog/feature-flags-sdks-architectures/)
