# Gatehouse — Execution Plan

**Method:** evidence-first, RED -> GREEN, hard capability gates  
**Structure:** dependency-ordered milestones, not a calendar  
**Primary rule:** do not advance because code exists; advance when the milestone’s behavior and failure semantics are proven

---

# 1. Definition of done

Gatehouse core is complete only when the repository can prove:

```text
deterministic routing
+
HTTP/1.1 + HTTP/2 proxying
+
Connect/gRPC semantics including trailers/streaming
+
client cancellation reaches upstream
+
versioned dynamic config with atomic last-known-good apply
+
control-plane outage does not break existing request routing
+
bounded deadlines/retries/retry budget
+
health selection + circuit breaker + concurrency limits
+
JWT/OIDC rotation/outage behavior
+
local rate limiting (+ distributed mode only if implemented)
+
useful OTel traces/metrics with bounded cardinality
+
operator UI for topology/revisions/health/traffic
+
race + leak + fuzz + integration + E2E + fault suites GREEN
+
Buf compatibility checks GREEN
+
govulncheck/security checks GREEN
+
performance report with p50/p95/p99 + profiles
+
fresh build/deploy/demo reproducible
        |
        v
GATEHOUSE CORE RELEASE
```

---

# 2. Operating rules

## Rule 1 — Read the protocol/runtime contract before wrapping it

For unfamiliar behavior:

```text
official Go/gRPC/Connect/Buf/OIDC/OTel docs
-> identify exact semantic obligation
-> write invariant
-> write RED test/probe
-> implement minimum
-> inspect wire/runtime evidence
```

## Rule 2 — One failure claim per RED test

A failing test is useful only if its failure means the intended behavior is missing.

## Rule 3 — Prefer real protocol evidence over mocks

A mock cannot prove HTTP/2 trailers, client disconnect propagation, connection reuse, or stream shutdown.

## Rule 4 — No hidden scope creep

Do not add Kubernetes controllers, WASM, xDS, HTTP/3, or distributed coordination to solve a local Gatehouse problem.

## Rule 5 — Two repair attempts, then re-diagnose

Keep the same discipline as the IronVM plan. Capture packet/trace/goroutine evidence when relevant.

## Rule 6 — Independent gate

No milestone passes from an implementer summary. Tests + review + evidence decide.

---

# 3. Capability map

```text
M0  repository + contracts + CI floor
 |
M1  static HTTP proxy + cancellation
 |
M2  deterministic router + clusters + health
 |
M3  control plane + revisions + dynamic atomic config
 |
M4  HTTP/2 + Connect + gRPC + streaming semantics
 |
M5  timeout + retry + budget + concurrency + circuit breaker
 |
M6  JWT/OIDC + rate limiting + TLS/mTLS policy
 |
M7  observability + operator UI
 |
M8  fault campaign + performance + resource hardening
 |
M9  mixed-version/release/reproducibility audit
```

Parallelism is allowed inside a milestone only when workstreams do not create conflicting architecture.

---

# 4. Milestone 0 — Bootstrap, contracts, and CI floor

## Objective

Make the repository capable of rejecting bad changes before feature work begins.

## Learn first

Understand:

- Go HTTP server/client cancellation;
- HTTP/2 basics and trailers;
- Protobuf compatibility rules;
- Buf lint/breaking workflow;
- race detector/fuzzing/govulncheck roles;
- process signal/graceful-shutdown model.

## RED 0A — contract generation is not reproducible

Define the proto workspace and generated Go/TS outputs. CI should fail when generated files drift.

## RED 0B — no compatibility protection

Add a deliberately breaking proto change on a scratch branch/test fixture and prove `buf breaking` detects it.

## GREEN 0

Create only:

- module/workspace;
- command skeletons;
- proto package `gatehouse.v1`;
- Buf lint/breaking/generate config;
- Makefile task contract;
- CI skeleton;
- root cancellation/shutdown wiring;
- test helpers, not product features.

## Gate M0

```text
[x] go test ./... GREEN (`make verify`)
[x] go test -race ./... GREEN on current skeleton (`make verify`)
[x] go vet ./... GREEN (`make verify`)
[x] staticcheck ./... GREEN (`make verify`)
[x] govulncheck ./... GREEN; no vulnerabilities reachable from the code (`make verify`)
[x] buf lint GREEN (`make verify`)
[x] buf breaking test proven capable of failing (breaking fixture rejected and compatible fixture accepted by `make compatibility-test`)
[x] buf generate is clean/reproducible (`make verify`)
[x] frontend lint/typecheck test harness exists if web initialized (not applicable; no web frontend initialized)
[x] CI required jobs are separate and visible (`.github/workflows/ci.yml`)
```

Stop. Do not build routing until the repository can protect contracts.

---

# 5. Milestone 1 — Static HTTP proxy and cancellation

## Objective

Prove the smallest correct data-plane request lifecycle before dynamic config or policy.

## RED 1A — basic proxy

```text
given one static route and one upstream
when client sends request
then upstream receives method/path/headers/body
and client receives status/headers/body
```

## RED 1B — client cancellation propagation

Upstream handler blocks on `ctx.Done()` and records when canceled.

```text
client starts request
-> gateway forwards
-> client cancels
-> upstream context must cancel within bounded time
-> gateway active-request count returns to baseline
```

## RED 1C — slow/streamed response is not fully buffered

Stream chunks with delays and prove first chunk reaches client before upstream finishes.

Memory should remain bounded for a large streamed response.

## GREEN 1

Implement:

- listener/server limits;
- static route config in process;
- HTTP reverse proxy path;
- hop-by-hop header handling;
- request context propagation;
- response-body ownership/closure;
- basic structured request outcome logging;
- graceful server shutdown.

Do not add retries yet.

## Gate M1

```text
[ ] HTTP proxy semantic test GREEN
[ ] cancel reaches upstream GREEN
[ ] streaming-first-byte test GREEN
[ ] huge streamed response does not scale memory with response size
[ ] hop-by-hop header tests GREEN
[ ] graceful shutdown unary request test GREEN
[ ] race + leak checks GREEN
```

---

# 6. Milestone 2 — Deterministic routing, clusters, load balancing, health

## Objective

Move from one hard-coded upstream to a pure, testable routing/cluster system.

## RED 2A — precedence matrix

Table tests for:

- exact host vs wildcard;
- exact path vs prefix;
- longer prefix vs shorter;
- method constraint;
- header constraint;
- ambiguous route rejection.

## RED 2B — route matcher fuzz

Properties:

```text
never panic
same input + same config -> same route
invalid config never reaches matcher
winner obeys precedence
```

## RED 2C — weighted selection

With injectable deterministic selection source, prove configured weights and no selection of disabled/unhealthy endpoints.

## RED 2D — health transition

A failing endpoint crosses unhealthy threshold and leaves selection; recovery requires healthy threshold.

## GREEN 2

Implement:

- config model for listeners/routes/clusters;
- semantic validator;
- compiled route index;
- round-robin + weighted round-robin;
- cluster runtime;
- active health scheduler with jitter/cancellation;
- stable route/cluster IDs in telemetry.

## Gate M2

```text
[ ] full route precedence matrix GREEN
[ ] fuzz smoke GREEN
[ ] health scheduler add/remove leak test GREEN
[ ] endpoint selection never chooses known-unhealthy endpoint when healthy choice exists
[ ] config with empty/missing cluster rejected
[ ] 10k route-match benchmark recorded
[ ] race GREEN under concurrent requests + health transitions
```

---

# 7. Milestone 3 — Control plane, revisions, and atomic hot reload

## Objective

Prove Gatehouse's defining control/data-plane architecture.

## RED 3A — config publication

```text
publish revision 1
-> data plane receives
-> validates/compiles
-> atomically activates
-> ACKs revision 1
```

## RED 3B — invalid config last-known-good

```text
revision 1 active
publish invalid revision 2
-> NACK 2
-> requests continue using revision 1
```

## RED 3C — stale/out-of-order

```text
revision 5 active
receive revision 4
-> ignore/stale response
-> remain on 5
```

Same revision with different hash must surface integrity failure.

## RED 3D — request/config race

Start long request on revision 10; activate revision 11 while it is in flight. The existing request must retain revision-10 route/policy semantics; later request uses 11.

## RED 3E — control-plane outage

Disconnect config stream after a valid revision is active. Requests continue; staleness metric rises; reconnect converges.

## RED 3F — concurrent editor conflict

Two mutations based on same parent; only one publication succeeds without explicit rebase.

## GREEN 3

Implement:

- PostgreSQL revision store;
- typed control-plane Admin API;
- validation + compile dry-run;
- immutable revision insert + active pointer transaction;
- config gRPC stream;
- ACK/NACK + heartbeat;
- data-plane candidate compile + atomic swap;
- versioned cluster-runtime replacement (reuse only for identical spec hash);
- instance status;
- reconnect with current revision;
- revision diff/rollback primitives.

Do not implement deltas.

## Gate M3

```text
[ ] RED 3A-3F GREEN
[ ] DB unavailable cannot corrupt active data-plane state
[ ] invalid publish cannot become active
[ ] config stream reconnect idempotent
[ ] 1,000 revision churn test stable in goroutines/memory
[ ] atomic race tests GREEN under -race
[ ] control plane restart does not interrupt current data-plane requests
[ ] audit entry exists for publish/rollback
```

---

# 8. Milestone 4 — HTTP/2, Connect, gRPC, trailers, streaming

## Objective

Prove protocol correctness beyond ordinary HTTP/1.1.

## RED 4A — HTTP/2 TLS

Real HTTP/2 client -> Gatehouse -> HTTP/2 upstream. Verify headers/body/status and connection reuse where observable.

## RED 4B — gRPC unary

Real generated gRPC client calls a test service through Gatehouse.

Verify:

- method path routing;
- metadata propagation policy;
- success status;
- upstream error status/trailers;
- deadline propagation.

## RED 4C — gRPC server streaming

Receive multiple streamed messages progressively. Gatehouse must not buffer entire stream.

## RED 4D — gRPC cancellation

Cancel client stream and prove upstream handler sees cancellation.

## RED 4E — Connect unary/server-streaming

Use a real Connect client/server pair and verify transport semantics.

## GREEN 4

Add only protocol-specific handling required to preserve semantics. Keep application payload opaque.

## Gate M4

```text
[ ] HTTP/2 integration GREEN
[ ] gRPC unary + server streaming GREEN
[ ] gRPC status/trailer propagation GREEN
[ ] gRPC deadline/cancel propagation GREEN
[ ] Connect unary + streaming GREEN
[ ] no unbounded stream buffering
[ ] shutdown active stream behavior documented/tested
[ ] protocol error mapper produces valid HTTP vs gRPC responses
```

---

# 9. Milestone 5 — Deadlines, retries, retry budget, admission, circuit breaker

## Objective

Add resilience without producing retry storms or hidden queues.

## RED 5A — total deadline

Route timeout must bound all attempts and backoff.

## RED 5B — safe retry

A replayable safe request receives one retry on configured transient failure and succeeds.

## RED 5C — unsafe/non-replayable suppression

POST/unsafe RPC or body above replay cap must not be retried unless explicit operation policy proves replay safety.

## RED 5D — response commitment

Once downstream response headers/body are committed, later upstream failure cannot trigger a retry.

## RED 5E — retry budget

Under a failure burst, budget suppresses attempts before maxAttempts can amplify load indefinitely.

## RED 5F — concurrency saturation

When route/global concurrency is full, new work is rejected quickly with deterministic protocol-correct overload response; no hidden unbounded queue.

## RED 5G — breaker state machine

Drive CLOSED -> OPEN -> HALF_OPEN -> CLOSED and failed half-open -> OPEN with fake clock where possible.

## GREEN 5

Implement:

- total/per-attempt timeout calculation;
- body replay cap;
- retry eligibility classifier;
- exponential backoff + jitter using injectable clock/RNG;
- retry budget;
- global/route semaphores;
- local breaker;
- reason-coded metrics.

## Gate M5

```text
[ ] all retry suppression reasons tested
[ ] deadline cancels sleeping retry immediately
[ ] no retry after commitment
[ ] saturation test proves bounded goroutine/request count
[ ] failure burst cannot exceed configured retry budget
[ ] breaker deterministic tests GREEN
[ ] Toxiproxy latency/reset cases GREEN where needed
[ ] race/leak GREEN
```

---

# 10. Milestone 6 — Authentication, rate limiting, TLS/mTLS

## Objective

Add policy boundaries with explicit dependency failure behavior.

## RED 6A — JWT validation matrix

Cover:

- valid token;
- expired/not-yet-valid;
- wrong issuer;
- wrong audience;
- disallowed algorithm;
- malformed token;
- authorization scope/role denied.

## RED 6B — JWKS rotation

Start on key A, rotate provider to B, send token B, trigger bounded refresh, then succeed. Concurrent unknown-kid requests cause one refresh wave, not N fetches.

## RED 6C — JWKS outage

Prove behavior for known cached key vs unknown key when provider is down.

## RED 6D — token bucket

Use fake clock/property tests for burst, refill, boundary precision, concurrent access.

## RED 6E — mTLS

Listener requiring client auth rejects no/invalid cert and accepts valid chain. Route requiring cert principal denies an unauthenticated listener identity.

## GREEN 6

Implement:

- JWT/OIDC provider cache;
- principal model;
- route RBAC;
- local token bucket;
- listener TLS/mTLS;
- authenticated operator/admin API mode;
- TLS + authenticated control↔data config channel for production-shaped deployment;
- rate/auth metrics and audit-safe diagnostics.

If distributed limiter is included, add Redis Testcontainers + failure-mode tests before gate.

## Gate M6

```text
[ ] JWT matrix GREEN
[ ] JWKS rotation + stampede prevention GREEN
[ ] auth-provider outage behavior GREEN
[ ] local limiter concurrency/property tests GREEN
[ ] mTLS matrix GREEN
[ ] admin API rejects unauthenticated/unauthorized operator calls outside explicit loopback-dev mode
[ ] control/data config stream rejects untrusted peer in secure mode
[ ] no secrets in captured logs/traces test fixtures
[ ] SSRF/destination validation tests GREEN for configured URLs
[ ] security review no unresolved blocker
```

---

# 11. Milestone 7 — Observability and operator UI

## Objective

Make failures diagnosable without SSHing into the process.

## RED 7A — trace evidence

One request with one retry must produce root request span + two attempt spans with stable route/cluster/revision attributes.

## RED 7B — cardinality guard

Tests/config review ensure raw path/user/token/request ID cannot appear in metric labels.

## RED 7C — rollout UI

Playwright flow:

```text
create/publish revision
-> see instance ACK
-> inspect diff
-> issue request
-> see health/traffic state
-> rollback
-> see new revision active
```

## GREEN 7

Implement:

- OTel tracing + metrics;
- structured logs;
- instance/revision status API;
- operational web UI;
- trace deep links/query integration as practical;
- request playground for safe protocols.

## Gate M7

```text
[ ] trace test GREEN
[ ] metrics semantic tests GREEN
[ ] no uncontrolled metric cardinality
[ ] Playwright critical publish/rollback flow GREEN
[ ] UI handles disconnected/stale instance state
[ ] auth diagnostics do not expose tokens
[ ] frontend typecheck/lint/unit/E2E GREEN
```

---

# 12. Milestone 8 — Failure campaign, load, performance, resource hardening

## Objective

Attack the system rather than adding features.

## Required campaign

Run at least:

```text
bad DNS
connection refused
TLS failure
slow headers
slow body
upstream reset mid-response
client cancel
all endpoints unhealthy
flapping endpoint
retry burst
config stream cut
invalid config
rapid config churn
JWKS outage
huge streamed response
concurrency saturation
SIGTERM under unary + streaming load
telemetry backend unavailable
```

Each scenario records:

```text
expected client outcome
expected upstream behavior
expected active revision
expected breaker/health state
expected metric/log/trace evidence
resource cleanup evidence
```

## Performance campaign

Publish reproducible results for:

- direct vs Gatehouse HTTP overhead;
- gRPC unary overhead;
- stream throughput;
- route count scaling;
- policy overhead;
- JWT hot-cache overhead;
- local rate limiter;
- config compile/apply.

Capture CPU/heap/allocation/goroutine profiles for representative load.

## Gate M8

```text
[ ] entire fault ledger executed
[ ] no unresolved race/leak
[ ] 30-60 minute soak has bounded goroutines/heap after warmup
[ ] config churn resources stabilize
[ ] benchmark report committed
[ ] at least one measured optimization with before/after evidence
[ ] failed optimization/negative result documented
```

---

# 13. Milestone 9 — Compatibility, release, reproducibility

## Objective

Prove the repository can be built, upgraded, and demonstrated from a fresh environment.

## Mixed-version matrix

At minimum once `v1` protocol stabilizes:

```text
old control -> old data   baseline
new control -> new data   current
new control -> old data   compatible capabilities only
old control -> new data   compatible capabilities only
```

Unsupported required capabilities must NACK clearly.

## Fresh-run proof

From a clean checkout/environment:

```bash
make bootstrap
make verify
make integration
make e2e
make build
make demo-up
```

Exact targets may differ, but one documented sequence must reproduce the system.

## Release audit

```text
[ ] required PR CI GREEN
[ ] nightly/fault suite recent GREEN
[ ] buf compatibility GREEN
[ ] govulncheck GREEN
[ ] dependency licenses reviewed as needed
[ ] container/image scan acceptable
[ ] SBOM produced
[ ] binary/image version embeds commit/version
[ ] config schema/version documented
[ ] operations + security + failure-mode docs current
[ ] benchmark environment recorded
[ ] release smoke test GREEN
```

Gatehouse core ends here. New features require a new milestone/ADR.

---

# 14. RED test ledger

| ID | Milestone | Claim | Green proof |
|---|---|---|---|
| R0A | M0 | generated contracts drift | generation clean check |
| R0B | M0 | breaking proto change undetected | Buf breaks CI |
| R1A | M1 | request not proxied semantically | real HTTP client/upstream |
| R1B | M1 | cancel not propagated | upstream sees `ctx.Done()` |
| R1C | M1 | response buffered | progressive chunk timing/memory |
| R2A | M2 | route precedence wrong | table matrix |
| R2B | M2 | matcher brittle | fuzz/property |
| R2C | M2 | weighted selection wrong | deterministic statistical/invariant test |
| R2D | M2 | health ignored | endpoint leaves/returns selection |
| R3A | M3 | config not activated | ACK + request on revision |
| R3B | M3 | invalid config replaces good | NACK + LKG stays active |
| R3C | M3 | stale update regresses state | active revision unchanged |
| R3D | M3 | request sees split config | in-flight revision retention |
| R3E | M3 | CP outage breaks traffic | LKG serves disconnected |
| R3F | M3 | editor race overwrites | optimistic conflict |
| R4A-E | M4 | protocol semantics incomplete | real HTTP2/gRPC/Connect tests |
| R5A-G | M5 | resilience unbounded/unsafe | timeout/retry/budget/breaker/admission tests |
| R6A-E | M6 | security policy incorrect | JWT/JWKS/rate/mTLS tests |
| R7A-C | M7 | system not operable | trace/cardinality/UI tests |
| R8 | M8 | failure/perf unknown | fault + soak + benchmark evidence |
| R9 | M9 | release not reproducible | clean build + compatibility matrix |

---

# 15. Evidence record per milestone

```text
MILESTONE:
COMMIT:
ENVIRONMENT:

RED:
- tests/probes:
- expected failure:
- observed failure:

GREEN:
- implementation summary:
- targeted commands:
- results:

REGRESSION:
- unit:
- race:
- integration:
- e2e:
- fuzz:
- security:

FAILURE TESTS:
- scenario:
- expected:
- observed:

PERFORMANCE (if relevant):
- workload:
- before:
- after:
- profiles:

REVIEW:
- reviewer/verifier:
- blockers:
- resolutions:

FILES CHANGED:
- ...

KNOWN LIMITATIONS:
- ...

GATE:
PASS / FAIL
```

---

# 16. Build discipline for agentic execution

Use multiple agents only for independent evidence lanes, for example:

```text
protocol researcher: HTTP2/gRPC semantic uncertainty
router/test engineer: route invariants + fuzzing
control-plane reviewer: revision/atomicity/failure model
security reviewer: JWT/JWKS/TLS/SSRF
performance reviewer: profiles/benchmark methodology
```

Do not have five agents independently redesign the whole system and vote. Resolve disputes by the protocol spec, Go runtime behavior, executable tests, and measured evidence.

A useful per-milestone loop:

```text
EXPLORE
-> RESEARCH exact unknowns
-> RED
-> ARCHITECT contract
-> ADVERSARIAL CRITIQUE
-> GREEN minimum
-> targeted tests
-> race/fuzz/fault as relevant
-> full regression
-> independent review
-> evidence report
-> gate
```

---

# 17. What to cut first

If scope pressure appears, cut:

```text
1. fancy topology visualization
2. WebSocket support
3. Redis distributed limiter
4. upstream mTLS
5. sophisticated traffic-split UI
6. Kubernetes deployment polish
7. custom live request-feed cosmetics
```

Do not cut:

```text
cancellation propagation
streaming correctness
atomic last-known-good config
route determinism
bounds/backpressure
safe retry rules + budget
race/leak tests
Buf compatibility
security failure behavior
fault campaign
reproducible CI
```
