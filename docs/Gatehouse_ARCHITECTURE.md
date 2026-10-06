# Gatehouse — Architecture

**Primary language:** Go  
**Product UI:** TypeScript + React (Vite SPA by default)  
**Primary datastore:** PostgreSQL, control plane only  
**Scope:** Programmable HTTP / Connect / gRPC gateway with a small control plane, versioned dynamic configuration, policy enforcement, observability, and an operator-focused UI  
**Design goal:** Deep networking, contracts, failure handling, concurrency, auth, backpressure, and service-operations proof without cloning Envoy

---

## 1. Purpose

Gatehouse is an educational-but-serious Layer-7 gateway whose data plane accepts HTTP-family traffic, selects a route, applies bounded policy, forwards to an upstream pool, propagates cancellation/deadlines, and emits useful operational telemetry. A separate control plane owns configuration, validation, revision history, and rollout state.

The project must visibly prove this control path:

```text
operator change
    -> control-plane validation
    -> immutable config revision
    -> versioned config stream
    -> data-plane compile + ACK/NACK
    -> atomic activation
    -> request matched against that exact revision
    -> policy + upstream selection
    -> proxied request / streaming response
    -> telemetry + operational evidence
```

The core claim is:

> A Go gateway can safely apply dynamic configuration while serving concurrent HTTP/Connect/gRPC traffic, preserve cancellation and streaming behavior, limit retry amplification and resource growth, enforce policy, and remain operable under realistic partial failure.

This repository is successful only if that claim is demonstrated through executable tests, fault injection, race/leak checks, protocol-level integration, and reproducible load evidence.

---

## 2. Product boundary

Gatehouse contains two runtime roles and one UI:

```text
                                ┌──────────────────────────────┐
                                │         Web Console          │
                                │ routes / health / traffic    │
                                │ revisions / traces / policy  │
                                └──────────────┬───────────────┘
                                               │ HTTPS / Connect
                                               ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                              CONTROL PLANE                                  │
│                                                                             │
│  Admin API -> validation -> revision store -> publisher -> instance status │
│                                   │                                         │
│                              PostgreSQL                                     │
└───────────────────────────────────┬─────────────────────────────────────────┘
                                    │ versioned server-streaming config
                                    │ + ACK/NACK / heartbeat
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                               DATA PLANE                                    │
│                                                                             │
│ listener -> classify -> route -> auth -> rate/concurrency -> attempt policy │
│       -> upstream pool -> transport -> response/stream -> telemetry         │
│                                                                             │
│                    immutable active configuration                           │
└───────────────┬──────────────────┬───────────────────┬──────────────────────┘
                │                  │                   │
                ▼                  ▼                   ▼
           Service A          Service B           Service C
          HTTP/Connect          gRPC              HTTP/2

Optional operational dependencies:
- Redis/Valkey only for distributed rate-limit mode.
- OpenTelemetry Collector + Prometheus + Jaeger/Tempo in the local observability stack.
```

Hard boundary:

> The data plane does not read PostgreSQL and does not synchronously call the control plane during normal request processing.

---

## 3. Scope and non-goals

### 3.1 In scope

Gatehouse should eventually support:

- HTTP/1.1 and HTTP/2 ingress;
- HTTP upstream proxying;
- Connect RPC proxying as HTTP semantics;
- gRPC proxying with protocol-aware status/trailer/deadline handling;
- host + path + method + selected-header routing;
- deterministic route precedence;
- weighted upstream pools;
- active health checks;
- request timeout budgets and per-attempt timeout;
- bounded retries with retry budgets;
- client cancellation propagation;
- long-lived server streaming and SSE;
- WebSocket proxying only after ordinary streaming is correct;
- local token-bucket rate limiting;
- optional Redis-backed distributed limiting behind the same contract;
- concurrency limits / load shedding;
- circuit breaking with bounded local state;
- OIDC/JWT verification;
- optional listener-level mTLS client authentication;
- audit records for control-plane mutations;
- traces, metrics, structured logs, and live instance/config state;
- a TypeScript operator UI;
- deterministic CI and deliberate failure campaigns.

### 3.2 Explicit non-goals for the first serious release

Do not add these unless a current acceptance criterion requires them:

- Envoy xDS compatibility;
- arbitrary user-supplied plugins, WASM filters, Lua, or dynamic code loading;
- service mesh sidecars;
- L4/TCP proxying;
- UDP/QUIC/HTTP/3;
- full Kubernetes ingress-controller behavior;
- automatic service discovery across every cloud/provider;
- custom DNS server;
- global strongly-consistent rate limiting;
- distributed circuit-breaker state;
- request/response transformation DSL;
- API monetization/billing;
- WAF/signature engine;
- full OAuth authorization server;
- secrets manager;
- bespoke trace/metrics database;
- guaranteed exactly-once config delivery;
- transparent arbitrary protobuf transformation.

A feature that cannot be tested with a clear failure model does not enter the core scope.

---

## 4. Architecture principles

1. **Request path is local.** A request may depend on the active snapshot and upstream services, not on PostgreSQL or the control-plane RPC being healthy.
2. **Configuration is immutable after publication.** Every activated config has a monotonically increasing revision and content hash.
3. **Apply is atomic.** A request sees one compiled revision for its routing/policy decision; it never sees a half-applied configuration.
4. **Invalid config cannot evict valid config.** Compilation failure produces NACK and preserves last-known-good.
5. **No unbounded buffering.** Streaming paths stream; retryable request buffering has explicit byte caps.
6. **No unbounded concurrency.** Every owned goroutine, queue, watcher, retry loop, and background worker has a stop path and a capacity/budget.
7. **Cancellation is a correctness property.** Downstream cancellation/deadline must cancel upstream work.
8. **Retries are opt-in behavior, not generic resilience magic.** They are bounded by method safety, replayability, deadline, and budget.
9. **Observability is designed, not added later.** Route IDs, config revision, upstream attempt count, and failure class are available from the first end-to-end milestone.
10. **Metric cardinality is bounded.** Never label metrics by raw URL path, request ID, user ID, token, arbitrary host, or arbitrary header value.
11. **Protocols retain their semantics.** gRPC trailers/status and HTTP streaming cannot be flattened into a generic buffered request/response abstraction.
12. **Evidence beats architecture prose.** The design is accepted only where tests, traces, benchmarks, and fault runs support it.

---

## 5. Repository shape

Recommended shape; collapse packages that prove unnecessary.

```text
gatehouse/
├── cmd/
│   ├── gatehouse-control/
│   │   └── main.go
│   └── gatehouse-data/
│       └── main.go
├── api/
│   └── gatehouse/
│       └── v1/
│           ├── control.proto
│           ├── config.proto
│           └── telemetry.proto        # only if Gatehouse-specific events are needed
├── gen/
│   ├── go/
│   └── ts/
├── internal/
│   ├── control/
│   │   ├── admin/
│   │   ├── publish/
│   │   ├── revision/
│   │   └── store/
│   ├── config/
│   │   ├── model/
│   │   ├── validate/
│   │   ├── compile/
│   │   └── hash/
│   ├── dataplane/
│   │   ├── listener/
│   │   ├── router/
│   │   ├── proxy/
│   │   ├── upstream/
│   │   ├── policy/
│   │   ├── auth/
│   │   ├── ratelimit/
│   │   ├── breaker/
│   │   └── lifecycle/
│   ├── protocol/
│   │   ├── httpx/
│   │   ├── grpcx/
│   │   └── connectx/
│   ├── telemetry/
│   └── testkit/
│       ├── upstream/
│       ├── jwks/
│       ├── certs/
│       └── faults/
├── tests/
│   ├── integration/
│   ├── e2e/
│   ├── fault/
│   └── compatibility/
├── web/
│   ├── src/
│   └── tests/
├── deploy/
│   ├── compose/
│   └── k8s/                    # later; not required for initial gateway correctness
├── docs/
│   ├── ARCHITECTURE.md
│   ├── DECISIONS.md
│   ├── PLAN.md
│   ├── TESTING_CI.md
│   ├── failure-modes.md
│   ├── operations.md
│   ├── security.md
│   ├── benchmarks.md
│   └── adr/
├── .github/workflows/
├── buf.yaml
├── buf.gen.yaml
├── go.mod
├── Makefile
└── README.md
```

Package rule:

> Organize around real ownership or state boundaries. Do not create interface/package layers merely to reproduce “clean architecture.”

---

## 6. Deployment topology

### 6.1 Development topology

```text
browser
  |
  +--> web dev server
  |
  +--> control plane :8081 ---> PostgreSQL
  |
  +--> data plane    :8443 ---> test upstreams
              |
              +--> OTLP collector -> Prometheus / Jaeger
```

### 6.2 Production-shaped topology

```text
                     +----------------+
operators ---------->| control plane  |----> PostgreSQL
                     +--------+-------+
                              |
                   config stream / ACK
                              |
               +--------------+--------------+
               |                             |
        +------v------+               +------v------+
client->| data plane A|               | data plane B|<-client
        +------+------+               +------+------+
               |                             |
               +-----------+-----------------+
                           |
                    upstream services
```

Control-plane replicas may be added later, but V1 correctness does not depend on HA control-plane consensus. PostgreSQL is the serialization point for config revision creation.

---

## 7. Configuration model

A published revision is an immutable logical document.

Conceptually:

```text
GatewayConfig
├── revision
├── content_hash
├── listeners[]
├── routes[]
├── clusters[]
├── auth_providers[]
├── rate_limit_policies[]
└── defaults
```

### 7.1 Route

```text
Route
├── id                 stable operator-visible ID
├── listener_id
├── match
│   ├── hosts[]
│   ├── path_kind      exact | prefix
│   ├── path
│   ├── methods[]
│   └── headers[]      bounded exact/presence rules only in V1
├── cluster_id
├── timeout_policy
├── retry_policy
├── auth_policy
├── rate_policy
├── concurrency_policy
└── observability_policy
```

### 7.2 Cluster

```text
Cluster
├── id
├── protocol           http | h2 | grpc
├── endpoints[]
├── lb_policy          round_robin | weighted_round_robin
├── connect_timeout
├── tls
├── health_check
└── breaker_policy
```

### 7.3 Revision identity

Each revision has:

```text
revision:      uint64 monotonic database sequence
content_hash:  SHA-256 over canonical serialized configuration
created_at:    UTC timestamp
created_by:    authenticated principal
parent:        previous active revision
comment:       optional operator reason
```

The revision number establishes ordering. The content hash establishes identity and corruption/deduplication evidence.

---

## 8. Control-plane persistence

PostgreSQL is a control-plane durability dependency, not a per-request dependency.

Minimum logical tables:

```text
config_revisions
- revision BIGINT PRIMARY KEY
- parent_revision BIGINT NULL
- content_hash BYTEA UNIQUE NOT NULL
- config_blob JSONB or BYTEA NOT NULL
- created_at TIMESTAMPTZ NOT NULL
- created_by TEXT NOT NULL
- comment TEXT NULL
- state TEXT NOT NULL          # published / superseded / rolled_back

active_config
- singleton_key BOOLEAN PRIMARY KEY
- revision BIGINT NOT NULL REFERENCES config_revisions(revision)
- updated_at TIMESTAMPTZ NOT NULL

instance_status
- instance_id TEXT PRIMARY KEY
- boot_id UUID NOT NULL
- applied_revision BIGINT NOT NULL
- last_ack_at TIMESTAMPTZ NOT NULL
- state TEXT NOT NULL
- last_error TEXT NULL

audit_log
- id BIGSERIAL PRIMARY KEY
- occurred_at TIMESTAMPTZ NOT NULL
- principal TEXT NOT NULL
- action TEXT NOT NULL
- target TEXT NOT NULL
- revision BIGINT NULL
- metadata JSONB NOT NULL
```

The first implementation may store the entire versioned configuration as one document. Avoid premature normalization across ten tables; this is a gateway project, not a relational modeling project.

---

## 9. Configuration publication flow

```text
operator mutation
    |
    v
parse + schema validation
    |
    v
semantic validation
    |  reject: duplicate route / bad reference / invalid timeout / etc.
    v
canonicalize
    |
    v
compile dry-run
    |
    v
BEGIN DB transaction
    |
    +--> optimistic parent-revision check
    +--> insert immutable revision
    +--> set active revision
    +--> audit record
    v
COMMIT
    |
    v
publisher notifies connected data planes
```

Semantic validation must reject at least:

- duplicate IDs;
- route references to absent cluster/policy;
- duplicate or ambiguous route precedence where the winner cannot be determined;
- empty clusters;
- invalid URL/scheme/authority;
- invalid timeout relationships;
- retry policy whose maximum attempt budget cannot fit inside total timeout;
- negative/zero limits;
- invalid JWT issuer/audience/key configuration;
- invalid TLS references;
- conflicting listener bindings;
- header match count/size above configured limits;
- percentage/weight sums outside accepted semantics.

---

## 10. Config stream protocol

Use a small Gatehouse-owned gRPC API. Do not implement xDS.

Conceptual protocol:

```text
DataPlane -> ControlPlane: Subscribe(instance_id, boot_id, current_revision, capabilities)
ControlPlane -> DataPlane: Snapshot(revision, content_hash, config)
DataPlane -> ControlPlane: ACK(revision, content_hash, applied_at)
DataPlane -> ControlPlane: NACK(revision, content_hash, error_code, message)
DataPlane -> ControlPlane: Heartbeat(applied_revision, health_summary)
```

V1 ships **full snapshots**, not deltas.

Why:

- simpler validation;
- deterministic replay;
- easier rollback;
- fewer partial-order bugs;
- easier compatibility testing;
- config volume is small enough for an educational gateway.

Deltas are post-V1 and require measured evidence that full snapshots are a bottleneck.

### 10.1 Ordering rules

For a data plane with active revision `R`:

- `revision < R`: ignore and report stale update;
- `revision == R` + same hash: idempotent ACK;
- `revision == R` + different hash: protocol integrity error;
- `revision > R`: validate/compile as candidate, then atomically activate or NACK;
- unknown required capability: NACK;
- stream disconnect: continue serving with `R` and expose staleness.

---

## 11. Data-plane configuration application

The active configuration is immutable from the request path.

Conceptually:

```go
type RuntimeSnapshot struct {
    Revision uint64
    Hash     [32]byte
    Router   *Router
    Clusters map[ClusterID]*ClusterRuntime
    Policies CompiledPolicies
}
```

Activation flow:

```text
Snapshot wire message
  -> structural validation
  -> semantic validation
  -> compile route index
  -> construct versioned cluster runtimes
  -> validate TLS/auth resources
  -> build candidate RuntimeSnapshot
  -> atomic pointer swap
  -> ACK
```

If any step fails, no active state mutates. A cluster runtime may be reused across revisions only when its immutable cluster specification hash is identical. A changed endpoint/TLS/transport/load-balancing specification gets a new runtime object, so an old request cannot observe in-place mutation from a newer revision. Safe resources such as an unchanged transport may be shared deliberately behind immutable configuration.

### 11.1 Request revision consistency

At request start:

```text
snapshot := active.Load()
```

The request retains that pointer for routing and policy decisions. A concurrent config swap affects only later requests.

This is deliberately RCU-like: readers are lock-light and a config update cannot split one request across route revisions.

### 11.2 Old snapshot cleanup

Old snapshots may hold transports/cluster resources. Cleanup rules:

- call `CloseIdleConnections` on transports removed from the new revision;
- never forcibly close an active request solely because config changed;
- background cleanup has a bounded queue;
- repeated revision churn must not grow goroutines, timers, or transports without bound;
- tests must publish hundreds/thousands of revisions and assert stable resource counts within tolerance.

---

## 12. Data-plane request flow

```text
accepted connection
    |
    v
HTTP server limits / TLS
    |
    v
request context + trace span
    |
    v
load active snapshot
    |
    v
classify protocol
    |
    v
route match
    | no route -> protocol-correct 404 / UNIMPLEMENTED-style response
    v
pre-auth admission
    |- global concurrency
    |- route concurrency / load shedding
    |- optional trusted-source/IP limiter
    v
authentication
    |
    v
authorization + identity-aware rate policy
    |
    v
compute effective deadline
    |
    v
select upstream endpoint
    |
    v
attempt loop
    |- connect / round-trip
    |- classify failure
    |- retry eligibility
    |- retry budget
    |- backoff bounded by remaining deadline
    v
stream response / trailers
    |
    v
telemetry + release admission tokens
```

Request handling must have one clear owner for each acquired resource: request body, admission token, retry buffer, upstream response body, and any stream pump.

---

## 13. Routing semantics

Routing must be deterministic and testable without network I/O.

Recommended precedence, highest first:

1. exact host before wildcard host;
2. exact path before prefix path;
3. longer path prefix before shorter prefix;
4. method-constrained before unconstrained;
5. header-constrained before unconstrained;
6. stable route ID lexical tie-breaker only where two routes are otherwise semantically equivalent and validation permits it.

Prefer rejecting ambiguous configurations rather than surprising operators with obscure precedence.

### 13.1 Host matching

V1:

- exact host: `api.example.com`;
- single-label wildcard suffix: `*.example.com`;
- normalized lower-case host without port;
- IDNA policy must be explicit before public internet use; do not invent ad-hoc Unicode normalization.

### 13.2 Path matching

V1:

- exact;
- byte-oriented prefix on normalized request path;
- preserve original escaped path for forwarding where Go HTTP semantics require it;
- do not perform filesystem-style path cleaning that can change application meaning unless explicitly documented.

Route matcher fuzzing is mandatory because escaping, repeated slashes, empty path, and malformed percent encodings are security-sensitive.

---

## 14. Protocol handling

### 14.1 HTTP

Use Go `net/http` primitives and a narrowly wrapped reverse-proxy transport rather than writing an HTTP parser.

Requirements:

- strip hop-by-hop headers correctly;
- append/forward `X-Forwarded-*` or standardized `Forwarded` under one documented trust model;
- preserve request host according to route policy;
- support HTTP/1.1 downstream and HTTP/1.1/HTTP/2 upstream as configured;
- propagate context cancellation;
- preserve trailers when present;
- flush streaming responses;
- never buffer an arbitrary response body solely to simplify metrics.

### 14.2 Connect RPC

Connect uses HTTP semantics, so route by host/path while preserving Connect headers/trailers and streaming framing.

Gatehouse does not need to decode application protobuf messages to proxy Connect traffic.

### 14.3 gRPC

gRPC is HTTP/2 but has additional invariants:

- `content-type` classification;
- method path `/package.Service/Method`;
- `grpc-timeout` / context deadline behavior;
- `TE: trailers` semantics;
- final `grpc-status` and `grpc-message` trailers;
- streaming request/response bodies;
- metadata preservation with an explicit denylist for gateway-owned/internal headers.

Gatehouse should proxy application frames without interpreting protobuf payloads. It may use protocol-aware helpers to produce gateway-generated gRPC errors.

Never convert a gRPC upstream failure into a generic HTTP JSON body.

### 14.4 WebSocket

WebSocket support is post-core-streaming. Add it only after cancellation, shutdown, and normal streaming tests are stable.

---

## 15. Deadline and timeout model

Every request gets an effective total deadline:

```text
effective deadline = min(
    client-supplied deadline if trustworthy/applicable,
    route total timeout,
    server hard maximum
)
```

Each upstream attempt may also have a per-attempt timeout, but it cannot exceed remaining total time.

Invariant:

> Gatehouse must not begin an attempt that has no meaningful time left to succeed.

Timeout classes:

- listener header read timeout;
- request total timeout;
- upstream connect timeout;
- TLS handshake timeout;
- response-header timeout for non-streaming routes where appropriate;
- per-attempt timeout;
- graceful-shutdown drain deadline.

Avoid `http.Client.Timeout` as a generic hammer for streaming; use request contexts and transport-specific timeouts.

---

## 16. Cancellation

Cancellation chain:

```text
client disconnect / deadline
          |
          v
request.Context().Done()
          |
          +--> cancel retry backoff
          +--> cancel upstream RoundTrip/RPC
          +--> stop body copy / stream pump
          +--> release admission token
          +--> finish trace span
```

Mandatory tests verify the upstream server observes cancellation, not merely that Gatehouse returns early.

No detached goroutine may continue consuming an upstream response after the downstream request has ended.

---

## 17. Backpressure and bounded resources

Gatehouse must define limits for:

- accepted connections;
- active requests globally;
- active requests per route;
- request header bytes;
- request body bytes where a route requires a body cap;
- retry-buffer bytes;
- pending config updates;
- JWKS refresh workers;
- health-check concurrency;
- telemetry export queue via SDK configuration;
- live-request UI/event buffers if implemented.

For ordinary streaming, let transport flow control carry backpressure. Do not copy entire bodies into memory.

### 17.1 Retry buffering

A request requiring replay must be replayable.

Safe cases:

- empty body;
- body known and buffered below route `max_retry_body_bytes`;
- another explicit replay source.

If the body exceeds the cap, the first attempt may continue but additional retries are disabled and a reason is recorded.

---

## 18. Retry model

Retries default to disabled unless route policy permits them.

Eligibility requires all of:

```text
route retry policy allows this failure
AND method/RPC is declared safe or idempotent
AND request body is replayable
AND response is not committed downstream
AND attempt count < max attempts
AND retry budget has capacity
AND total deadline has remaining time
```

Never retry:

- after response headers/body are committed;
- an active streaming exchange after useful stream progress;
- a non-replayable body;
- arbitrary POST/unsafe RPC by assumption;
- authentication/authorization denials;
- deterministic 4xx application errors unless explicitly configured with strong justification.

### 18.1 Retry budget

Per route or cluster maintain a bounded retry token budget. Successful requests slowly replenish it; retries consume it.

Purpose:

> An unhealthy upstream must not cause Gatehouse to multiply offered load without bound.

Retry metrics must expose attempted, suppressed-by-budget, suppressed-by-deadline, and suppressed-by-nonreplayable counts.

---

## 19. Load balancing and upstream pools

V1 policies:

- round robin;
- weighted round robin.

Each endpoint has runtime health separate from immutable config identity.

Endpoint lifecycle:

```text
configured
  -> unknown
  -> healthy
  -> unhealthy
  -> probing
  -> healthy
```

Do not remove endpoints from configuration simply because they are temporarily unhealthy.

Selection excludes open-circuit/unhealthy endpoints when alternatives exist. If none remain, return a deterministic unavailable response and telemetry reason.

---

## 20. Health checking

Active health checks are bounded background work.

Each cluster config may specify:

```text
interval
jitter
connect/response timeout
healthy threshold
unhealthy threshold
HTTP path or gRPC health service mode
```

Rules:

- jitter to avoid synchronized probes;
- health checks use their own small concurrency budget;
- shutdown cancels them;
- config removal stops them;
- no goroutine-per-endpoint timer leak after repeated revisions;
- health state is local to a data-plane instance.

Passive failure observation may inform circuit breaking but should not secretly rewrite active-health semantics.

---

## 21. Circuit breaker

Use a small local state machine, not a distributed system.

```text
CLOSED
  | failure threshold in rolling window
  v
OPEN
  | cool-down
  v
HALF_OPEN
  | bounded probes succeed -> CLOSED
  | probe fails -----------> OPEN
```

The breaker must cap half-open probes and avoid per-request allocations where practical.

Breaker state is intentionally not shared across Gatehouse replicas. Each replica protects itself based on its observed upstream behavior.

---

## 22. Rate limiting

### 22.1 Local mode

Implement local token bucket first.

Potential keys:

- route only;
- route + authenticated subject;
- route + API key identity;
- route + source IP only when proxy trust configuration makes source IP meaningful.

Do not permit arbitrary header values as metric labels even if they are rate-limit keys.

### 22.2 Distributed mode

Optional Redis/Valkey adapter behind a small interface.

The route policy must state backend-failure behavior explicitly:

```text
on_backend_error: allow | deny
```

No hidden fallback from distributed to local semantics because that changes the promised global limit.

Distributed limiting is tested for Redis timeout, disconnect, stale connection, and fail-open/fail-closed behavior.

---

## 23. Authentication and authorization

### 23.1 JWT/OIDC

A route may require a named auth provider:

```text
issuer
audiences[]
JWKS source
allowed algorithms
clock skew bound
required claims
```

Validation requirements:

- issuer exact match;
- audience policy explicit;
- signature verification;
- reject `alg=none` and disallowed algorithms;
- time claims validated with bounded skew;
- key ID selection explicit;
- no trust of unverified claims;
- authorization happens after authentication.

### 23.2 JWKS cache

The cache must support rotation without creating a per-request fetch dependency.

Rules:

- normal requests use cached keys;
- unknown `kid` may trigger a single-flight refresh;
- concurrent misses do not stampede the provider;
- fetch timeout is bounded;
- cache staleness policy is explicit;
- telemetry distinguishes invalid token from key-provider unavailable;
- tests rotate keys and simulate provider outage.

### 23.3 Route RBAC

Keep V1 authorization small:

- required roles/scopes/claim equality;
- AND/OR semantics documented;
- no general expression language.

### 23.4 mTLS

Client TLS authentication is listener-level because certificate negotiation occurs before HTTP routing.

A route may require that an already-verified client certificate maps to an allowed principal, but it cannot retroactively request a certificate after route selection.

Upstream mTLS may be configured per cluster if implemented, with CA roots and client identity loaded through an explicit secret mechanism. Do not put private keys in config revision blobs.

---

## 24. Secret handling

Configuration stores secret **references**, not secret material, where practical.

Never emit to logs/traces:

- Authorization headers;
- cookies;
- raw JWTs;
- client private keys;
- upstream credentials;
- full request/response bodies by default.

Any debug body capture must be an explicitly disabled-by-default feature with strict size/redaction rules; preferably post-V1.

---

## 25. Control-plane API behavior

Admin mutations use optimistic concurrency.

Conceptual request:

```text
UpdateConfig(base_revision=41, patch=...)
```

If active revision is 42, return conflict instead of silently overwriting another operator's change.

Control-plane operations:

- get active config;
- validate candidate without publishing;
- publish candidate based on expected parent revision;
- list revisions;
- diff revisions;
- rollback by republishing a prior immutable config as a new revision;
- list data-plane instances and applied revisions;
- query recent NACKs/errors;
- manage auth/rate/route definitions through typed APIs.

Rollback creates a new revision; it does not mutate history.

---

## 26. Instance state and partial deployment

The UI/control plane should expose:

```text
instance_id
boot_id
version/build SHA
connected/disconnected
last heartbeat
active revision
active hash
revision age
last ACK
last NACK
listener health
cluster health summary
```

Partial rollout is not hidden. Operators must be able to see that instances are on revisions 100 and 101 simultaneously.

Readiness policy:

- no valid active snapshot: not ready;
- valid snapshot + listener serving: ready;
- control plane disconnected but snapshot still valid: ready but degraded/stale metric;
- staleness above configured threshold: policy may mark not ready, but this must be an explicit deployment choice because removing all stale replicas can worsen an outage.

---

## 27. Observability

Use OpenTelemetry for traces/metrics where practical and structured logging for local diagnostic events.

### 27.1 Trace model

Root span per proxied request with bounded attributes:

```text
gatehouse.route.id
gatehouse.config.revision
gatehouse.cluster.id
gatehouse.protocol
http.request.method
server.address / network protocol attributes per current OTel semantics
attempt.count
retry.reason if any
```

Child attempt spans represent upstream attempts.

Do not put raw token/user IDs or arbitrary full URLs into low-cardinality metric dimensions.

### 27.2 Metrics

Minimum set:

```text
requests_total{route,protocol,outcome}
request_duration_seconds{route,protocol}
active_requests{route}
upstream_attempts_total{cluster,outcome}
upstream_attempt_duration_seconds{cluster}
retries_total{route,reason}
retry_suppressed_total{route,reason}
rate_limited_total{route}
auth_failures_total{route,reason}
circuit_state{cluster,state}
healthy_endpoints{cluster}
config_active_revision
config_apply_total{outcome}
config_staleness_seconds
control_stream_connected
```

If route count becomes large, document metric-cost implications and provide aggregation controls.

### 27.3 Logs

Log state transitions and failures, not every successful byte copied.

Every error log should include stable IDs:

- request/trace ID where available;
- route ID;
- cluster ID;
- revision;
- failure class;
- attempt number.

---

## 28. Operator UI

The UI is an operational surface, not a CRUD skin.

Required screens over the project lifecycle:

1. **Topology** — routes -> clusters -> endpoints, current health.
2. **Route editor** — typed validation, revision preview, diff before publish.
3. **Revision history** — creator, hash, diff, rollout state, rollback action.
4. **Instances** — per-data-plane active revision/staleness/build.
5. **Traffic** — request rate, active requests, outcome, p50/p95/p99.
6. **Retries/errors** — attempts, retry suppression, breaker state.
7. **Rate limits** — policy and rejection trends.
8. **Auth diagnostics** — aggregate failure classes, never token contents.
9. **Trace drill-down** — link/request context into trace backend.
10. **Request playground** — HTTP/Connect and safe gRPC testing against configured routes.

Frontend rules:

- strict TypeScript;
- no unchecked API casts at untrusted boundaries;
- generated client from protobuf/OpenAPI/Connect contracts where possible;
- explicit loading/empty/error/retry states;
- Playwright for critical publish/rollback/diagnostic flows;
- no secrets persisted in browser local storage.

---

## 29. Concurrency ownership

Gatehouse must be explainable in terms of owned long-lived goroutines.

Expected long-lived owners:

```text
process root context
├── public listener serve loop
├── admin/config-stream client or server
├── health-check scheduler
├── stale-snapshot monitor
├── telemetry SDK/export workers (library-owned, explicitly shut down)
└── optional distributed-rate backend housekeeping
```

Avoid:

- one permanent goroutine per route;
- one permanent goroutine per config watcher item;
- unbounded goroutines from auth refresh;
- detached retry timers;
- fire-and-forget telemetry goroutines in application code.

Every background component contract should answer:

```text
Who starts it?
Who cancels it?
Who waits for it?
What is its queue capacity?
What happens when the queue is full?
What resources does it close?
```

---

## 30. Graceful shutdown

Data plane shutdown:

```text
SIGTERM
  -> mark not accepting new readiness traffic if deployment requires
  -> stop accepting new connections / begin HTTP shutdown
  -> cancel config subscription + health schedulers
  -> allow active requests/streams to drain until grace deadline
  -> cancel remaining requests
  -> close idle upstream connections
  -> flush telemetry within bounded timeout
  -> exit
```

Control plane shutdown additionally stops config stream servers and closes DB pools after active admin operations drain.

Tests must include shutdown while:

- unary HTTP request is active;
- gRPC/server stream is active;
- retry backoff is sleeping;
- JWKS refresh is in flight;
- config apply is compiling;
- health probes are running.

---

## 31. Error model

Internal errors should be classified rather than string-matched.

Example classes:

```text
ErrNoRoute
ErrConfigInvalid
ErrConfigStale
ErrNoHealthyUpstream
ErrAdmissionRejected
ErrRateLimited
ErrUnauthenticated
ErrForbidden
ErrDeadlineExceeded
ErrUpstreamConnect
ErrUpstreamProtocol
ErrCircuitOpen
ErrControlPlaneUnavailable
```

Protocol adapters map internal classes to protocol-correct responses.

HTTP examples:

- no route -> 404;
- rate limit -> 429 + optional Retry-After;
- auth missing/invalid -> 401;
- authorization denied -> 403;
- no healthy upstream -> 503;
- gateway timeout -> 504.

gRPC-generated errors must use gRPC status/trailers rather than arbitrary HTTP bodies.

Do not leak internal upstream addresses or secret configuration in client errors.

---

## 32. Security model / threat boundaries

Trust zones:

```text
untrusted client traffic
     |
     v
public listener [high exposure]
     |
     v
routing/policy code
     |
     v
upstream network

operator/browser -> authenticated control-plane API -> config database

data plane <-> control plane: TLS + mutually authenticated deployment channel beyond localhost
```

Production-shaped mode does not treat a private network as authentication. The admin API requires an authenticated operator principal with role checks, and the config stream authenticates data-plane instances. A loopback-only unauthenticated developer mode may exist but must be explicit and must not bind publicly.

Threats to test/design for:

- header smuggling / hop-by-hop confusion;
- malformed URL/path escapes;
- huge headers/body;
- slowloris/slow reader/slow upstream;
- SSRF through operator-supplied upstream/JWKS endpoints;
- JWT algorithm/key confusion;
- JWKS rotation stampede;
- auth-provider outage;
- config privilege escalation;
- stale security policy during control-plane outage;
- secrets in logs/traces;
- retry amplification;
- rate-limit bypass through inconsistent key derivation;
- forged forwarded headers;
- untrusted proxy source-IP assumptions;
- TLS misconfiguration;
- dependency vulnerabilities;
- compromised build/release artifacts.

Security-sensitive configuration changes must be attributable in the audit log.

---

## 33. SSRF and destination policy

Because operators can configure upstreams and possibly JWKS URLs, Gatehouse can become an SSRF primitive if the control API is compromised or overly permissive.

V1 rules:

- only explicit `http`/`https` upstream schemes;
- no `file:`, `unix:`, `gopher:`, etc.;
- host/port validated;
- admin API access strongly authenticated;
- optional deny CIDRs for cloud metadata/link-local/loopback targets where deployment requires it;
- redirect behavior for JWKS discovery/fetch bounded and documented;
- DNS rebinding risk documented before public multi-tenant use.

This does not make Gatehouse a hardened multi-tenant edge proxy; it makes trust assumptions explicit.

---

## 34. Testing architecture

Testing is a first-class subsystem. Detailed matrix lives in `TESTING_CI.md`.

### 34.1 Pure/unit tests

No network or Docker:

- route matching/precedence;
- config validation;
- canonical config hashing;
- retry eligibility;
- timeout calculation;
- retry budget state;
- token bucket state;
- circuit-breaker transitions;
- forwarded-header trust logic;
- JWT claim-to-principal mapping with locally signed fixtures;
- protocol error mapping;
- content/header classification;
- config ordering/staleness decisions.

### 34.2 Fuzz tests

Mandatory targets:

- route matcher/path parser;
- config decode/validate entry points;
- forwarded-header parser;
- gRPC/content-type classifier;
- any custom binary/text framing parser introduced later.

Fuzz invariant: never panic, never allocate without practical bounds for tiny input, never match a route inconsistent with deterministic precedence.

### 34.3 Integration tests

Use real Go servers and real clients for:

- HTTP/1.1;
- HTTP/2 TLS;
- Connect unary + streaming;
- gRPC unary + server streaming;
- trailers;
- cancellation;
- deadlines;
- retries;
- health selection;
- config hot reload;
- auth/JWKS rotation;
- local rate limiting;
- graceful shutdown.

### 34.4 Container integration

Use Testcontainers where real dependencies materially matter:

- PostgreSQL;
- Redis/Valkey distributed limiter;
- Toxiproxy for network faults.

Do not containerize an in-process dependency merely to make the test look more realistic.

### 34.5 Race/leak tests

Run race detector across the Go suite and dedicated churn tests.

Leak scenarios:

- repeated config revisions;
- canceled streaming requests;
- upstream timeout loops;
- auth refresh failures;
- health-check add/remove cycles;
- shutdown.

### 34.6 End-to-end

Boot control plane + data plane + upstreams + UI and verify operator workflow:

```text
publish route
-> data plane ACKs revision
-> request succeeds
-> break upstream
-> UI/metrics show failure
-> recover upstream
-> publish new revision
-> rollback
```

---

## 35. Failure campaign

These are acceptance tests, not demo ideas.

### Configuration

- invalid snapshot;
- stale/out-of-order snapshot;
- same revision with different hash;
- control-plane disconnect;
- concurrent publish conflict;
- config stream reconnect;
- rapid 1,000-revision churn;
- one data plane NACKs while another ACKs;
- partial rollout;
- rollback.

### Network/upstream

- DNS failure;
- connection refused;
- SYN/connect timeout simulation where practical;
- TLS handshake failure;
- slow response headers;
- slow response body;
- downstream client disconnect;
- upstream disconnect mid-response;
- huge streamed response;
- all endpoints unhealthy;
- one endpoint flapping.

### Resilience

- retryable failure burst;
- retry budget exhaustion;
- route total deadline expires during backoff;
- non-replayable request body;
- circuit opens and half-open probes;
- concurrency saturation;
- rate-limit saturation.

### Auth/security

- expired token;
- wrong issuer/audience;
- unknown `kid` then successful rotation;
- JWKS unavailable;
- malformed JWT;
- forged forwarding headers;
- mTLS client absent/invalid/valid;
- SSRF-restricted destination.

### Lifecycle

- SIGTERM under active unary request;
- SIGTERM under streaming request;
- restart control plane with data planes serving;
- restart data plane while control plane is unavailable;
- telemetry backend unavailable;
- PostgreSQL unavailable to control plane.

Every campaign case must define expected client behavior, expected state retention, and expected telemetry.

---

## 36. Performance architecture

Gatehouse performance work must report workload and environment, not marketing adjectives.

Benchmarks:

1. pure route match benchmark by route count;
2. policy overhead benchmark;
3. HTTP pass-through baseline vs direct upstream;
4. HTTP/2/gRPC unary overhead;
5. streaming throughput + memory behavior;
6. config compile/apply time by route/cluster count;
7. local token bucket throughput;
8. JWT verification hot-cache vs key refresh;
9. retry/failure-path overhead;
10. allocation profile under representative request mix.

Report:

```text
benchmark host + CPU
Go version
commit SHA
route count / cluster count
connection reuse settings
payload sizes
concurrency
p50 / p95 / p99
throughput
allocs/op where meaningful
CPU profile
heap profile
what changed
what did not improve
```

Do not gate PRs on noisy shared-runner p99 numbers. Gate deterministic benchmark properties and run performance regression on a controlled runner or as reviewed nightly evidence.

---

## 37. API / compatibility contract

All Gatehouse-owned Protobuf packages are versioned, e.g. `gatehouse.v1`.

Rules:

- field numbers never reused;
- removed fields reserved;
- enums retain unknown/default handling;
- config-stream capabilities negotiate optional behavior;
- data plane must NACK a snapshot requiring unsupported behavior rather than partially applying it;
- generated code is reproducible;
- CI runs Buf lint and breaking-change checks against the mainline baseline.

Backward compatibility matters for two independent surfaces:

1. operator/control-plane API clients;
2. control-plane <-> data-plane config stream.

The latter needs explicit mixed-version tests before claiming rolling-upgrade compatibility.

---

## 38. CI architecture summary

Required pull-request checks should include independent jobs for:

```text
contracts     -> buf lint + buf breaking + generated-code cleanliness
static        -> gofmt + go vet + staticcheck + frontend lint/typecheck
unit          -> Go unit/property tests + frontend unit tests
race          -> go test -race
integration   -> protocol/config/auth tests; containers where needed
e2e           -> composed system + Playwright critical flows
security      -> govulncheck + dependency/container checks as configured
```

Nightly/periodic:

```text
long fuzz
fault/Toxiproxy suite
soak + config churn
load/benchmark run
race-heavy integration
```

Release:

```text
all required checks
-> reproducible binaries/images
-> SBOM
-> vulnerability scan
-> signed/provenance-capable artifacts where repository hosting supports it
-> smoke boot
-> publish
```

Full commands, flake policy, and CI stages live in `TESTING_CI.md`.

---

## 39. Correctness invariants

1. A request uses exactly one active route/config revision for policy decisions.
2. An invalid config revision never replaces the last-known-good revision.
3. Revisions are ordered monotonically; same revision with different hash is a protocol integrity error.
4. Data-plane normal request handling does not require PostgreSQL or a live control-plane RPC.
5. Route matching is deterministic for every accepted configuration.
6. Unknown/ambiguous configuration is rejected, not guessed.
7. Client cancellation propagates to upstream work.
8. Total request deadline bounds all retries and backoff.
9. A response is never retried after downstream commitment.
10. A non-replayable request body is not replayed.
11. Retry attempts are bounded by both max-attempt and retry-budget policies.
12. Streaming paths do not buffer unbounded bodies.
13. Admission/rate/concurrency tokens are released exactly once.
14. Background workers have bounded lifetimes and are canceled on config removal/shutdown.
15. Config churn does not cause unbounded goroutine/timer/transport growth.
16. Auth decisions use verified claims only.
17. Metrics do not contain uncontrolled high-cardinality request data.
18. Secrets are not intentionally emitted into logs/traces.
19. Control-plane mutation conflicts do not silently overwrite newer config.
20. Rollback creates a new auditable revision rather than rewriting history.
22. gRPC gateway-generated failures preserve gRPC status/trailer semantics.
23. Upstream response bodies are always closed exactly once by the owning path.
24. Graceful shutdown either completes active requests or cancels them at a bounded deadline.
25. A required dependency failure has a documented fail-open/fail-closed behavior; no silent semantic fallback.
26. Control-plane/admin and control↔data authentication are explicit outside loopback development.
27. Success criteria are semantic and observable, not merely “process returned 0.”

---

## 40. Dependency policy

Prefer standard library and narrow, mature dependencies.

Expected categories:

```text
Go stdlib net/http, crypto/tls, sync/atomic, context
protobuf / gRPC or Connect libraries for Gatehouse control APIs
golang.org/x/* where justified
OpenTelemetry Go
PostgreSQL driver (pgx)
Redis client only when distributed limiter exists
Testcontainers in tests
small JWT/OIDC library or go-oidc when needed
```

Do not import:

- a complete existing gateway/proxy that implements the core learning problem;
- an Envoy control plane;
- a generic plugin framework;
- a service-mesh framework;
- a giant policy engine unless Gatehouse has outgrown explicit policy code and evidence justifies the cost.

Every production dependency should answer:

```text
What capability does it own?
Why is stdlib/our code not appropriate?
What is its security/maintenance surface?
How is it tested during upgrade?
```

---

## 41. Operational health model

Endpoints:

```text
/livez   process event loop is alive
/readyz  can accept traffic under current readiness policy
/status  authenticated/admin-only detailed state
/metrics Prometheus scrape endpoint where configured
```

`/readyz` should not synchronously probe every upstream or the control plane. It reports Gatehouse's ability to serve according to current local state.

Data-plane degraded conditions are observable separately from hard unready state.

---

## 42. Acceptance transcript / proof

A final demonstration should show more than a happy request:

```text
1. start control plane + DB + two data planes + two upstreams
2. publish revision 1 with weighted route
3. both data planes ACK revision 1
4. HTTP and gRPC requests succeed
5. start streaming request; cancel client; upstream observes cancellation
6. inject upstream latency/failure
7. observe timeout/retry budget/circuit metrics
8. publish invalid revision -> rejected; revision 1 continues serving
9. publish valid revision 2 -> atomic activation
10. disconnect control plane -> data planes continue on revision 2
11. reconnect -> status converges
12. rotate JWKS key -> tokens with new key eventually succeed without stampede
13. run race/integration/E2E suite
14. show benchmark/profile report
15. graceful shutdown under active traffic completes within configured drain window
```

The release is not complete if the demo requires manually hiding an error, disabling a test, or removing fault injection.

---

## 43. Post-core extensions

Only after the above architecture is demonstrably stable:

- WebSocket proxying;
- Redis distributed rate limiter;
- upstream mTLS identities;
- DNS/service discovery refresh;
- Kubernetes EndpointSlice discovery;
- canary/traffic-splitting UI;
- outlier ejection;
- adaptive concurrency experiments;
- config deltas;
- multi-control-plane HA;
- shadow traffic;
- richer policy language;
- HTTP/3;
- plugin/WASM experiments.

Each extension must add a new test/failure model, not only a feature checkbox.
