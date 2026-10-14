# Gatehouse — Execution Plan

**Method:** evidence-first, RED -> GREEN, hard capability gates  
**Structure:** dependency-ordered milestones, not a calendar  
**Primary rule:** do not advance because code exists; advance when the milestone’s behavior and failure semantics are proven

---

# 1. Definition of done

## Intended user, workload and product advantage

GateHouse targets developers and small teams running 5–20 HTTP/gRPC services on VMs or Docker Compose, needing dynamic configuration, safe rollback and predictable resource use. The release goal is a gateway another developer can deploy, operate and upgrade in their project. Educational value is not the product acceptance criterion.

The [product and benchmark contract](Gatehouse_PRODUCT_BENCHMARKS.md) is authoritative for the P100-H/P100-G/P1000-C profiles and T01–T09. Initial unmeasured targets include **25% lower data-plane peak RSS with at least 90% of the fastest eligible reference's capacity**, p99 latency difference vs direct upstream ≤2 ms, streaming goodput ≥90% of direct, and publication-to-activation p95 ≤250 ms. Report whole deployment cost including PostgreSQL; these goals are not market-performance claims until reproduced.

Baseline comparison candidates are pinned Caddy and Traefik OSS configurations, with a minimal Go ReverseProxy and direct upstream as controls. Preserve completed M0–M3 evidence. P0/P1 and the secure pilot gates below are new unchecked obligations; M3 completion does not imply a public production release. D046–D049 record this direction and supersede the learning-based restriction in D037.

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
supported workload targets T01–T09 with comparison uncertainty and whole-stack cost
+
secure quickstart + usable diagnostics + beta/pilot evidence with honest adoption status
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
P0  current HTTP/stream/config baseline + footprint + insight + architecture gate
 |
M4  HTTP/2 + Connect + gRPC + streaming semantics + P1 protocol baseline
 |
M5  timeout + retry + budget + concurrency + circuit breaker + P1 policy baseline
 |
M6  JWT/OIDC + rate limiting + TLS/mTLS policy + P1 auth/limiter baseline
 |
secure beta/pilot readiness + early telemetry (external adoption tracked separately)
 |
M7  observability + operator UI
 |
M8  extended fault/soak + performance regression + resource hardening
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
[x] HTTP proxy semantic test GREEN
[x] cancel reaches upstream GREEN
[x] streaming-first-byte test GREEN
[x] huge streamed response does not scale memory with response size (sampled peak HeapAlloc: 8 MiB → 128 MiB body, growth under 24 MiB)
[x] hop-by-hop header tests GREEN
[x] structured failure outcome logged when an upstream response aborts
[x] graceful shutdown in-flight request test GREEN
[x] race + leak checks GREEN (16 concurrent cancellations; active count 16→0 and goroutines return within baseline +4)
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
[x] full route precedence matrix GREEN
[x] fuzz smoke GREEN
[x] health scheduler add/remove leak test GREEN
[x] endpoint selection never chooses known-unhealthy endpoint when healthy choice exists
[x] config with empty/missing cluster rejected
[x] 10k route-match benchmark recorded
[x] race GREEN under concurrent requests + health transitions
```

M2 acceptance evidence is recorded in `Gatehouse_TESTING_CI.md`.

---

# 7. Milestone 3 — Control plane, revisions, and atomic hot reload

## Objective

Prove Gatehouse's defining control/data-plane architecture. PostgreSQL is control-plane-only; the data plane receives complete immutable snapshots and serves its last-known-good configuration through control-plane outages.

**Showcase:** Follow [the M3 walkthrough](Gatehouse_M3_DEMO.md) from a clean checkout: publish blue, switch to green without restarting, inspect instance ACK/history/diff, reject invalid and stale mutations, roll back as a new revision, then stop/restart management while traffic continues. The walkthrough covers interactive backend operation; real-process tests additionally prove in-flight request continuity and database faults. A graphical operator interface remains M7.

## Part 1 — Durable revisions and typed publication

- Define typed protobuf snapshots for current listener, route, cluster, endpoint, and health-check configuration, plus the Admin API for validation, publication, revision history, diff, rollback, and instance status. Persist one current boot row per non-empty instance ID and expose bounded keyset listing now; Part 2 adds stream-driven updates and reconnect behavior.
- Preserve all M2 config fields in lossless conversions. Hash deterministic protobuf snapshot bytes with SHA-256; reject unknown fields, unsupported schema/enums, oversized snapshots, and invalid configs before publication.
- Add PostgreSQL migrations and an immutable revision store. A transaction holds a PostgreSQL advisory lock, checks the expected parent, inserts revision content and audit data, updates the active pointer, and emits a transactional notification. Rollback publishes historical content as a new revision.
- Start the control-plane gRPC service only when explicit mode is enabled and the bind address is a literal loopback IP. Apply migrations and verify database connectivity at startup; bound request and shutdown lifetimes.
- **Part 1 status: CLOSED — local acceptance gates evidenced on 2026-10-13.** Implementation and config/Admin/store tests are present, including instance identity validation and a PostgreSQL upsert for one current boot row per instance ID. The control-plane command tests explicit enablement, literal loopback binding, database-before-listen ordering, graceful gRPC stop, and forced stop at the shutdown deadline. The pinned shuffled unit suite and PostgreSQL integration test passed locally; the integration test exercised migration, instance-row replacement, concurrent publication, rollback, audit persistence, and transaction rollback. Part 1 local test evidence is recorded in `Gatehouse_TESTING_CI.md`; config streaming and dynamic activation remain in later M3 parts.

## Part 2 — Configuration stream and atomic data-plane activation

- Stream the latest full snapshot to data-plane instances; support reconnect, ACK/NACK, and heartbeat/status reporting. Coalesce queued updates to the latest complete snapshot.
- Compile each candidate before activation, enforce revision/hash integrity and process-start listener immutability, and atomically swap the active snapshot. Existing requests retain their starting snapshot; failed candidates preserve last-known-good.
- Reuse cluster runtime only when its complete specification hash matches, including retained A state across A→B→A. Release retired workers/resources after their last request owner exits.
- Dynamic data-plane mode waits for a valid snapshot and remains not ready until activation. A disconnected plane continues serving its active snapshot and exposes connection staleness.
- **Part 2 status: CLOSED — local acceptance gates evidenced on 2026-10-13.** The control-plane stream delivers the latest complete durable revision on connect, publication, and periodic reconciliation; permits one in-flight snapshot per stream; and validates ACK/NACK only for that exact delivered revision. Newer publications coalesce behind it and the latest complete snapshot is sent after ACK/NACK. Registrations, ACK/NACKs, heartbeats, connection loss, and replacement sessions update instance status. The opt-in dynamic data-plane mode validates and compiles before activation, keeps an in-flight request on its starting snapshot, retains identical cluster state safely across A→B→A, and serves last-known-good configuration through disconnection. BufConn tests cover replay/reconnect/status, skipped-revision ACK rejection, client retry behavior, atomic activation, stale/integrity/listener rejection, readiness/staleness, and runtime reuse. The final pinned verification passed the unit and race suites, vet, Staticcheck, Buf lint/breaking compatibility fixtures, generated-file checks, module verification, and vulnerability scan; generation was deterministic. Evidence is recorded in `Gatehouse_TESTING_CI.md`. Part 3 acceptance and exact-head hosted CI are now recorded below.

## Part 3 — Failure campaign and acceptance

- Add isolated PostgreSQL integration coverage and a required CI job. Exercise transaction failures, editor conflicts, rollback durability, invalid publication, reconnect/idempotency, and database outage/recovery.
- Restart the control-plane executable while a long request is active; prove request continuity, durable revision recovery, reconnect, and subsequent publication.
- Run 1,000 health-enabled revisions with a slow subscriber. Prove bounded retained scheduler/probe state, goroutines within baseline +8, and post-warmup heap growth under 24 MiB for the fixed small-config fixture.
- Record exact toolchain, commands, measurements, review results, and remote checks in the testing evidence document. Keep README milestone status synchronized.
- **Part 3 status: CLOSED — local acceptance and exact-head hosted CI evidenced.** PostgreSQL integration now injects revision-insert, active-pointer-update, and audit-insert failures and verifies each leaves the active pointer, revision history, and audit log unchanged. `FuzzConfigDecodeValidate` covers protobuf decode, validation, canonical re-encoding, and hash stability; its seeds and a bounded fuzz run passed. Existing PostgreSQL invalid-publication, editor-conflict, rollback, outage/recovery, and real control-plane restart tests pass against a disposable instance. The restart test retains an in-flight data-plane request through control-plane termination and database outage, then proves reconnection and publication after recovery. A slow subscriber coalesces 1,000 health-enabled publications to the latest revision; a separate 1,000-activation health-runtime test asserts one retained cluster/probe, goroutines within baseline +8, and live heap below 24 MiB. GitHub Actions run [#25](https://github.com/SShogun/GateHouse/actions/runs/38424816123) passed all six jobs on exact head `67c46cb7819d6c90a28c285453eb23f51e4eae75`. Evidence and the connected-status correction are recorded in `Gatehouse_TESTING_CI.md`.

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

## Additional RED cases

```text
RED 3G — publication transaction failure
Fail revision insert, active-pointer update, or audit insert
-> transaction rolls back; previous active revision remains authoritative

RED 3H — rollback durability
Publish A, then B, then rollback to A
-> new revision C contains A's config; history is unchanged; audit exists

RED 3I — validation and contract fidelity
Round-trip M2 config fields; reject invalid config before publication
-> dry-run creates no listener, probe, or retained worker

RED 3J — integrity and duplicate delivery
Same revision/hash is idempotent; same revision with different hash or a bad payload hash
-> integrity failure; active state unchanged

RED 3K — runtime ownership
Route-only update retains unchanged cluster state; changed spec gets independent state
A→B→A with in-flight A request -> retained A state is reused safely

RED 3L — listener immutability
Snapshot changing process listener settings -> reject before activation

RED 3M — initial readiness and staleness
No valid snapshot -> not ready and proxy returns 503
Disconnect after activation -> serve continues and staleness rises; reconnect recovers

RED 3N — real process restart
Restart control-plane executable during a long request
-> request completes, durable config recovers, data plane reconnects

RED 3O — database outage/recovery
Database unavailable -> publication fails without affecting active requests
Database restored -> valid publication commits and converges

RED 3P — bounded churn and subscribers
1,000 health-enabled revisions plus slow subscriber
-> latest revision delivered; runtime state, goroutines, and heap remain bounded
```

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
[x] RED 3A–3P GREEN
[x] DB unavailable cannot corrupt active data-plane state
[x] invalid publish cannot become active
[x] config stream reconnect idempotent
[x] 1,000 revision churn test stable in goroutines/memory
[x] atomic race tests GREEN under -race
[x] control-plane restart does not interrupt current data-plane requests
[x] audit entry exists for publish/rollback
[x] process startup readiness and listener immutability proven
[x] full pinned make verify GREEN on final source
[x] independent review has no unresolved blockers
[x] stale connected-status audit finding resolved and reviewed
[x] M3 config decode/validate fuzz target added and exercised
[x] transaction failure injection covers revision insert, active-pointer update, and audit insert
[x] exact-head required remote CI, including security/integration, GREEN (GitHub Actions run [#25](https://github.com/SShogun/GateHouse/actions/runs/38424816123) on `67c46cb7819d6c90a28c285453eb23f51e4eae75`; see `Gatehouse_TESTING_CI.md`)
```

### Post-M3 CI correction — 2026-10-14

The historical gate above records its tested SHA. The later documentation/showcase branch exposed two CI findings; PR #10 is not ready to merge until these corrections are published and its exact-head checks pass. See D053 and the follow-up evidence in `Gatehouse_TESTING_CI.md`.

- [x] First-branch push baseline: reproduce the all-zero previous-head failure, select the fetched default branch, and cover normal and rejected event cases.
- [x] Restart metadata race: deterministically delay the first ACK, reproduce the missing revision assertion, then wait for server-persisted boot/build/revision/hash before crashing. Preserve request continuity, recovery, and metadata assertions.
- [x] Pinned Go 1.26.9 full `make verify` with disposable PostgreSQL, including race and security checks; bounded independent review has no unresolved blocker.
- [ ] Approved commit/push and successful exact-head hosted CI for this correction. Previous green M3 checks do not satisfy this item.

---

# 7a. Early product and baseline campaign — P0 / P1

**Current scheduling status (2026-10-14): DEFERRED by user decision D052.** Retain the campaign below for later execution. M4 protocol implementation may proceed before P0; benchmark acceptance items remain pending and do not become passed through deferral. The single Debian lab VM exists with 4 GiB RAM; no driver/upstream clones or competitive load runs were created by the interrupted automatic setup.

## Objective

Move the entire first performance campaign out of M8. Establish a controlled HTTP baseline and source-grounded architecture decisions before M4. Benchmark each new protocol/policy when it first works; never claim a gRPC/JWT result from an HTTP/no-policy stand-in. Scope, methods, budgets and research sources are in `Gatehouse_PRODUCT_BENCHMARKS.md`.

**Next sequence:** Follow the [competitor execution plan](Gatehouse_COMPETITOR_BENCHMARK_PLAN.md): Part A locks versions, builds fixtures/config adapters and proves conformance; Part B measures Caddy, Traefik and Go-RP's eligible categories first; Part C publishes the reference report, derives absolute GateHouse budgets, then measures eligible GateHouse profiles. Reference HTTP/2/gRPC can be measured before GateHouse M4, while GateHouse rows remain blocked. No load tests have been run by these documentation revisions.

- [ ] Reference Part A gate: environment, fixtures, native configuration, protocol parity and observer/driver calibration pass.
- [ ] Reference Part B gate: each T01–T09 category has measured/FAIL/N/A/UNAVAILABLE outcomes per candidate, with transport/product and default/tuned settings separate.
- [ ] Reference Part C gate: raw data regenerates the report; absolute GateHouse budgets have units and provenance; no computed goal is called achieved performance.

## Part 1 — Reproducible harness and basic insight (P0)

**Environment sequence:** [VM benchmark learning plan](Gatehouse_BENCHMARK_VM_PLAN.md), E0–E9. The user completed host inventory and a single Debian VM forwarding exercise, then authorized assistance with clones before deferring the campaign under D052. Further provisioning, calibration and benchmark execution remain pending.

- [x] E0: manually inspect host OS, CPU topology, RAM, storage and available virtualization; choose the first lab path from evidence. The user reported Linux x86_64, VT-x and `/dev/kvm` available; host-specific details remain in the conversation, not this project file. Local KVM is the first path to investigate.
- [ ] E1–E3: create one disposable Linux VM manually; learn networking, processes and resource reporting; demonstrate one request through a foreground reverse proxy and clean shutdown.
- [ ] E4–E6: assess capacity; manually create separate driver/upstream/gateway roles if feasible; install pinned candidates, check conformance and pilot calibration.
- [ ] E7–E9: review and execute the run matrix by hand, analyze raw evidence, derive numeric targets and verify cleanup.

**Dependencies:** completed M3; no production-mode or gRPC claim.

- [ ] Build versioned upstream/load/config fixtures for P100-H, P1000-C and available P-FAULT cases, with direct calibration and Go ReverseProxy as the separately scoped C1 transport control; pin Caddy/Traefik OSS versions/images and freeze equivalent product configs.
- [ ] Record the 2 vCPU / 2 GiB gateway-host budget and separate load/upstream roles, source SHA, Go 1.26.9, networking, CPU/kernel/cgroups and timing resolution. Define offered-load correctness, sustainable capacity and common Q exactly as the product contract requires.
- [ ] Collect external CPU/RSS/cgroup/connection measurements plus bounded request outcomes, revision status, activation/rejection and control staleness. Add semantic tests for metric labels and sink outage behavior as instrumentation lands; retain full M7 requirements.
- [ ] Verify driver/upstream spare capacity, missed-arrival/timeouts accounting, status/body correctness and bounded scenario teardown. Export raw machine-readable data, run manifests and profiles; do not expose bodies, secrets or arbitrary identifiers.

**Verification:** replay a small bounded fixture and independently check request counts, fault timestamps, effective config, revision and measurements. Fail on an invalid benchmark environment. Planned benchmark commands become documented commands only when implemented; no new Makefile target is claimed by this plan edit.

## Part 2 — HTTP baseline and architecture gate (P0, before M4)

- [ ] Run five randomized repeated baselines for HTTP overhead/capacity, streaming goodput, 1/100/1k/10k route scaling, 1k-route compile/apply, failure/retry-free behavior, and allocation/CPU/mutex/heap profiles. Re-run the earlier route benchmark under pinned Go; its previous differing-runtime result is not a competitive baseline.
- [ ] Report T01–T06 and T08–T09 on runnable HTTP fixtures, basic T07 metrics/log cost, and each missing feature explicitly. Measure data-plane cost and total control-plane/PostgreSQL deployment separately.
- [ ] Resolve each architecture-review row: request-path mutex contention; quadratic route-ambiguity validation; per-endpoint pools/probes; retained revision lifetime; stream notification/poll reconciliation; configuration/instance bounds; whole-stack store cost; insight/security prerequisites.
- [ ] Keep/change/reject each optimization from measured evidence. Any runtime ownership/store/proxy/dependency change has a separate ADR and meaningful RED→GREEN correctness/race regressions; a neutral speed result is not a performance win.
- [ ] Freeze the initial release targets or amend them with rationale, retaining original proposed values and misses. Publish the initial result report including losing/inconclusive cases.
- [ ] Derive concrete MiB, correct req/s/core and MiB/s GateHouse budgets from measured references/direct results; retain independent latency/activation/reliability limits. Re-run peers at Q_all if adding GateHouse changes the matched offered load.

**P0 gate:** valid environment and reproducible HTTP evidence; architecture review resolved; target feasibility/misses and approved remediation recorded; no omitted runnable metric or unsupported feature marked GREEN. Actual optimizations receive their own completion gates before feature work advances.

## Part 3 — Complete the entire campaign as capabilities arrive (P1)

| Baseline / insight obligation | Owner and dependency | Acceptance evidence |
| --- | --- | --- |
| Direct vs gateway HTTP; streaming memory/goodput; route scale; config compile/apply; allocation/CPU profiles; available faults | P0 before M4 | Part 2 gate and T01–T09 scoped results |
| HTTP/2 TLS and gRPC unary/server streaming, deadline/cancellation under load | First correct M4 slice | P100-G; separate protocol distributions, body/status/trailer checks and streaming interference |
| Policy overhead and retry/failure-path cost | First correct M5 slice | Disabled / each policy / combined runs, retry amplification, rejection and recovery accounting |
| JWT hot-cache/rotation; local token-bucket throughput; secure-channel costs | First correct M6 slice | P-POLICY with equivalent features or clearly separate overhead panel; refresh stampede and TLS/auth proofs |
| Insight and trace/log cost; telemetry-sink failure | Basic insight in P0; M7 completion | T07 with fixed 1% tracing, bounded labels/queues, counted drops and collector outage |

Every row is part of the transferred campaign. An unimplemented row is blocked by its listed milestone, not deferred to the first M8 measurement. Repeat the affected baseline after each material feature/optimization; compare matched configurations on a controlled runner. D032 still prevents noisy shared-CI latency gates.

## Gate P1 and secure beta/pilot readiness

- [ ] All runnable post-M6 campaign rows and supported-profile T01–T09 have valid results and reviewed target misses/amendments; T07's full tracing extension is explicitly blocked until M7, not marked passed by basic metrics/logging.
- [ ] M4–M6 semantic and security gates pass; HTTP/gRPC and production TLS/auth/control channels are real, tested capabilities. Loopback M3 mode is not advertised for public production traffic.
- [ ] Publish a versioned beta binary/image, secure Compose quickstart, example service/client, documented limits, migrations, update and rollback instructions; a fresh uninvolved user completes setup, publish, traffic, rollback and injected-failure diagnosis.
- [ ] Diagnose route/cluster/outcome/revision, request admission, upstream/auth/timeout/retry causes, control staleness and resource growth through bounded metrics and reason-coded logs/status; complete full traces/UI in M7 before core release.
- [ ] Separate synthetic report, opt-in real-traffic pilot and external-adoption status. Before claiming external production use, one consenting external project runs a real non-sensitive workload for seven days with published aggregate outcomes and operator feedback. If absent, mark adoption unproven; technical beta publication does not invent users.

The first beta is a smaller product checkpoint, not a waiver of M7–M9 core-release gates. No deployment, external traffic collection, benchmark execution, dependency migration or public comparison publication is performed by this planning revision.

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
[ ] P1 HTTP/2/gRPC baseline and supported-protocol T01–T06 evidence recorded before optimization
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
[ ] P1 per-policy and retry/failure-path baseline with CPU, memory, latency and amplification recorded
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
[ ] P1 JWT/cache-refresh, limiter and secure-channel baselines recorded
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

# 12. Milestone 8 — Extended failure/soak, performance regression, resource hardening

## Objective

Attack the supported product and validate sustained behavior against the existing P0/P1 baselines rather than introducing its first performance measurement.

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

The complete initial baseline campaign now belongs to P0/P1 above. Re-run it under the final feature set, extended faults and soak for regression, including:

- direct vs Gatehouse HTTP overhead;
- gRPC unary overhead;
- stream throughput;
- route count scaling;
- policy overhead;
- JWT hot-cache overhead;
- local rate limiter;
- config compile/apply.

Capture CPU/heap/allocation/goroutine profiles for representative load.

Compare against the frozen workload and matched baseline environment in `Gatehouse_PRODUCT_BENCHMARKS.md`. Publish observed production/pilot traffic separately from generated load. Failed targets remain visible and require a recorded decision; no aggregate leaderboard score hides failed reliability or resource constraints.

## Gate M8

```text
[ ] entire fault ledger executed
[ ] no unresolved race/leak
[ ] 30-60 minute soak has bounded goroutines/heap after warmup
[ ] config churn resources stabilize
[ ] benchmark report committed
[ ] entire P0/P1 baseline campaign complete and final supported-profile T01–T09 regression report reviewed
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
[ ] public comparison graphics generated from versioned raw results, with units, feature parity, uncertainty and full deployment costs
[ ] secure beta/pilot onboarding and external-adoption status documented honestly
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
| RP0-H | P0 | apparent efficiency from unfair load or excluded dependencies | equivalent config, driver/upstream calibration, correct outcomes, data-plane and whole-stack measurements |
| RP0-C | P0 | reload/memory objective assumed from microbenchmark | 1k-route external activation, selector stress, contention and retained-resource profiles |
| RP1 | P1 | new protocol/policy silently consumes performance budget | matched per-feature baselines and supported-profile T01–T09 evidence |
| R4A-E | M4 | protocol semantics incomplete | real HTTP2/gRPC/Connect tests |
| R5A-G | M5 | resilience unbounded/unsafe | timeout/retry/budget/breaker/admission tests |
| R6A-E | M6 | security policy incorrect | JWT/JWKS/rate/mTLS tests |
| R7A-C | M7 | system not operable | trace/cardinality/UI tests |
| R8 | M8 | sustained faults or feature growth invalidate earlier budgets | fault + soak + regression against P0/P1 baseline evidence |
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
