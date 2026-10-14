# Gatehouse — Engineering Decisions

**Format:** Compact architecture decision record  
**Rule:** A decision stays binding until executable evidence or a changed requirement justifies replacing it. Replacement requires a new ADR; do not silently drift.

---

## D001 — Split control plane and data plane

**Decision:** Gatehouse runs separate control-plane and data-plane binaries/process roles.

**Why:** The request path should remain available when configuration management or PostgreSQL is unavailable. This also makes dynamic config, rollout, staleness, and operational behavior visible.

**Rejected:** one process that queries configuration storage during requests; microservice explosion beyond these two roles.

---

## D002 — The data plane never queries PostgreSQL on the request path

Configuration arrives as validated snapshots. PostgreSQL belongs to control-plane durability only.

This is a hard invariant, not a performance optimization.

---

## D003 — Full immutable snapshots before config deltas

**Decision:** Control plane publishes complete versioned config snapshots.

**Why:** Full snapshots are easier to validate, replay, hash, diff, NACK, rollback, and reason about. Gatehouse config is expected to remain small enough for this during the core project.

**Rejected for core:** incremental xDS-like resource deltas.

---

## D004 — Atomic last-known-good activation

Data plane compiles a candidate completely before one atomic active-pointer swap.

Invalid config produces NACK and preserves current serving state.

No field-by-field mutation of active routing state.

---

## D005 — A request retains one snapshot revision

At request start, load the active immutable snapshot and retain it for routing/policy decisions.

A concurrent config publication affects subsequent requests, not the semantics of the current request.

---

## D006 — PostgreSQL stores immutable revision history

Published revisions are append-only records. Rollback republishes prior content as a new revision.

Do not rewrite history to make rollback appear as if an incident never happened.

---

## D007 — Optimistic concurrency on operator mutation

Admin updates specify the base/parent revision. A stale editor gets a conflict and must rebase/review.

Do not use last-writer-wins for security or routing config.

---

## D008 — Go `net/http` owns HTTP parsing

Do not write an HTTP parser. Gatehouse focuses on proxy semantics, routing, cancellation, policy, and operations.

A narrowly wrapped reverse proxy/transport is preferred over a full gateway framework.

---

## D009 — Protocol-aware proxy, not application-message decoding

Gatehouse understands HTTP/Connect/gRPC transport semantics but does not decode arbitrary application protobuf messages.

This preserves a clear layer boundary and avoids becoming an API-transformation engine.

---

## D010 — Do not clone Envoy

No xDS compatibility, filter-chain ecosystem, WASM runtime, L4 proxying, or huge extension API in the core project.

The point is to learn the essential gateway mechanisms directly.

---

## D011 — Deterministic route precedence; reject ambiguity

Exact/specific matches outrank less-specific matches. Config validation rejects unresolved ambiguity.

A route winner must be explainable from configuration alone.

---

## D012 — Retry is disabled unless explicitly safe

Retries require route policy, replayable body, uncommitted response, time budget, and a safe/idempotent operation contract.

Do not infer safety from “it usually works.”

---

## D013 — Retry budget is mandatory when retries are enabled

Max attempts alone does not prevent retry amplification during widespread failure.

A route/cluster budget can suppress otherwise eligible retries and must emit telemetry explaining suppression.

---

## D014 — Streaming is first-class and normally unbuffered

Do not convert long-lived responses into buffered bodies to simplify middleware.

Streaming requests are not retried after useful stream progress.

---

## D015 — Context cancellation must reach the upstream

A downstream disconnect/deadline is not complete until Gatehouse cancels or stops upstream work and releases local resources.

Tests must observe upstream cancellation.

---

## D016 — Bounded concurrency and memory everywhere

Every queue, retry buffer, health-check worker, JWKS refresh, telemetry queue, and active-request pool has a bound and overload behavior.

No “temporary” unbounded channel in production code.

---

## D017 — Active health and circuit breaking are local

Health and breaker state is per data-plane instance.

Do not add distributed breaker coordination; it raises complexity and can create a new availability dependency.

---

## D018 — Local token bucket first

Rate limiting starts with a benchmarkable local token-bucket implementation.

Redis/Valkey is an optional distributed mode with explicit backend-failure semantics. No silent distributed->local fallback.

---

## D019 — OIDC/JWT keys are cached; auth provider is not a per-request dependency

Requests verify against cached keys. Unknown-key refresh is bounded and single-flight.

Provider outage behavior and cache staleness are explicit and tested.

---

## D020 — mTLS authentication is listener-level

TLS client-certificate negotiation occurs before HTTP route selection. Routes may require an already-verified certificate principal but may not pretend to turn TLS client auth on after routing.

---

## D021 — Secret references, not private keys in revision blobs

Config revisions may contain non-secret policy and references. Sensitive key material should come from a separate runtime secret source.

Never log or trace credentials/tokens.

---

## D022 — OpenTelemetry is the telemetry contract

Traces and metrics use OTel semantics where practical.

Logs remain structured application diagnostics. Gatehouse does not implement its own telemetry database.

---

## D023 — Metric cardinality is an architecture constraint

Allowed labels are stable bounded identifiers such as route/cluster/protocol/outcome.

Raw URL paths, request IDs, user IDs, arbitrary hostnames, and header values are forbidden as metric labels.

---

## D024 — Buf guards Protobuf compatibility

All Gatehouse-owned Protobuf APIs use Buf lint and breaking-change checks in CI against mainline.

Generated client code must be reproducible and checked for drift.

---

## D025 — Mixed-version compatibility must be tested before claimed

A new control plane and old data plane may coexist only for capability combinations covered by tests.

Unsupported required features cause NACK, not partial application.

---

## D026 — RED before GREEN for behavior milestones

Each major behavior starts with a failing executable proof when practical:

```text
EXPLORE
-> RED
-> freeze contract/invariants
-> GREEN minimum implementation
-> targeted tests
-> regression
-> independent review
-> evidence
-> gate
```

Never weaken a RED test because implementation is inconvenient.

---

## D027 — Protocol tests use real clients and servers

Mock-only tests do not prove HTTP/2, trailers, gRPC status, cancellation, or streaming behavior.

Use in-process real servers/clients first; containers only for dependencies/faults that require them.

---

## D028 — Fuzz routing and parser boundaries

At minimum fuzz route/path/header classification and config-decode/validate entry points.

Any future custom framing/parser immediately gains a fuzz target.

---

## D029 — Race detector and leak testing are release requirements

Gateway correctness includes concurrency ownership.

Repeated config churn, canceled streams, health-check lifecycle, auth refresh, and shutdown receive dedicated race/leak coverage.

---

## D030 — Fault injection is mandatory

Toxiproxy or equivalent network fault control is used for connection disruption, latency, and timeout behavior where in-process stubs cannot reproduce transport failure.

Fault tests have deterministic expected outcomes; chaos without assertions is not evidence.

---

## D031 — CI is tiered, not one giant workflow

PR-required checks are split into contracts/static/unit/race/integration/E2E/security so failures identify the broken boundary and can run concurrently.

Long fuzz, soak, fault sweeps, and performance run nightly/periodically.

---

## D032 — Shared-runner latency benchmarks do not hard-fail PRs

Public/shared CI has noisy neighbors. Use it to compile/run benchmarks and catch catastrophic allocation regressions, not to enforce fragile p99 thresholds.

Serious performance comparisons run on a controlled runner with recorded environment.

---

## D033 — UI is an operations interface

The UI must expose topology, health, revisions, rollout state, request/error/latency behavior, and trace links.

A route CRUD form by itself does not satisfy the product requirement.

---

## D034 — Do not add a generic policy language in core

Auth, rate, timeout, retry, and routing policies are typed structures.

CEL/Rego/custom DSL is deferred until concrete requirements prove typed policy insufficient.

---

## D035 — No self-certification

The implementer does not declare a milestone complete.

Completion requires executable evidence plus independent review/verifier checks against the gate.

---

## D036 — Two failed fixes under one hypothesis triggers re-diagnosis

```text
failure
-> inspect evidence
-> fix attempt 1
-> retest
-> fix attempt 2
-> retest
-> still failing: stop editing
-> capture state/logs/trace/pcap where useful
-> reopen protocol/runtime evidence
-> form a new hypothesis
```

Repeated edits without new evidence are not debugging.

---

## D037 — Stable dependency budget

**Status:** Superseded by D048 on 2026-10-14. The original rationale is retained below as history; learning value no longer determines dependency selection.

Prefer stdlib and narrow mature libraries. Adding a complete proxy/gateway framework that owns the project’s central learning surface is rejected.

Test-only dependencies may be richer than runtime dependencies when they improve failure realism.

---

## D038 — Kubernetes comes after gateway correctness

Containerization and a small deployment manifest are useful; a Kubernetes operator/ingress controller is not part of Gatehouse core.

Do not let deployment YAML substitute for networking depth.

---

## D039 — Config-stream disconnect is degraded, not immediate request failure

A running data plane continues serving last-known-good config and emits staleness state.

Cold start with no valid snapshot fails readiness rather than inventing a default permissive config.

---

## D040 — Security policy failure modes must be explicit

If a dependency such as distributed rate limiting or key refresh can fail open or fail closed, the choice belongs in typed configuration and tests.

There is no silent fallback that changes a security promise.

---

## D041 — Cluster runtime reuse requires identical immutable spec

A newer revision may reuse a cluster runtime/transport only when the immutable cluster specification hash is unchanged.

Changed endpoint, TLS, protocol, load-balancing, timeout, or breaker configuration gets a new versioned runtime object. Do not mutate a cluster object still reachable from an older request snapshot.

---

## D042 — Admission is phased around authentication

Cheap global/route concurrency protection may run before authentication to protect Gateway CPU/memory. Identity-aware rate limiting and authorization run only after principal verification.

Do not derive a subject-based limit key from unverified JWT claims.

---

## D043 — Internal channels are authenticated outside loopback development

Production-shaped admin APIs require authenticated operator principals and role checks. Data-plane config streams use TLS and instance authentication/mTLS.

An unauthenticated developer mode may bind loopback only and must be explicit. “Private network” is not an authentication mechanism.

---

## D044 — Canonical Go module path and Protobuf contract version

The Go module path follows the canonical repository identity: `github.com/SShogun/GateHouse`.

Since `gatehouse.v1` defined no application messages and the project was pre-release (M0), the `go_package` option in `gatehouse.v1` was updated directly to match the new canonical repository path.

The zero-consumer migration completed in M0. The temporary `FILE_SAME_GO_PACKAGE` exception was removed in commit `0d366eb`; `buf.yaml` now enforces strict `FILE` compatibility. No temporary exception remains.

---

## D045 — Admin connection status is process-local

**Decision:** The Admin API's `InstanceStatus.Connected` reports whether this control-plane process currently owns a live stream for the instance and that session's registered boot ID matches the boot ID in the stored instance row.

PostgreSQL continues to store the latest reported instance metadata, including its last reported connection flag, for compatibility and diagnostics. That stored flag is not authoritative for the public `Connected` projection. A control-plane restart therefore reports persisted instances disconnected until their streams reconnect. During a database outage, instance listing retains its existing database-error behavior; the process-local projection applies after reads recover.

**Why:** A crash or failed disconnect write cannot reliably clear durable connection state. Tying the API status to the current process's session registry prevents a stale row from claiming a stream exists and prevents a replaced boot from inheriting the old stream's status.

**Scope:** One control-plane process owns this view. Global status across multiple control-plane processes would require a separate shared lease/ownership design. This decision adds no protobuf field, migration, startup reset, or lease worker.

**Evidence:** The session registry projects status after the bounded database page is read; service regressions cover orphaned rows, boot mismatch, failed disconnect persistence, and replacement ownership. A disposable-PostgreSQL process test covers restart and database outage/recovery. See `Gatehouse_TESTING_CI.md`, “M3 connected-status audit correction.”

---

## D046 — A small deployable gateway is the product objective

**Decision (2026-10-14):** GateHouse targets developers and small teams running 5–20 HTTP/gRPC services on VMs or Compose, needing dynamic configuration, safe rollback, bounded resources and operational insight. Learning remains a consequence of building it, not the release acceptance criterion.

**Why:** Correctness proofs alone do not establish usefulness or a reason to adopt another gateway. The intended advantage is a measured resource envelope plus revision-aware failure insight. Dynamic routing and metrics are prerequisites already present in established products.

**Contract:** `Gatehouse_PRODUCT_BENCHMARKS.md` defines the workloads and provisional targets, including 25% lower data-plane peak RSS with at least 90% of the fastest eligible reference capacity. These are unmeasured goals. Whole-stack cost, including PostgreSQL, is mandatory reporting; a smaller data plane cannot justify an unmeasured total-deployment saving claim.

**Consequences:** Retain the split plane, last-known-good and ownership contracts. Require a secure quickstart, real-protocol load/fault evidence and an explicit beta/adoption distinction. One control-plane process remains the D045 scope. No “production-ready” or faster-than-competitor claim follows from this ADR alone.

---

## D047 — Baselines precede architectural optimization

**Scheduling status:** The requirement to execute P0 before starting M4 is superseded by D052. Measurement requirements before performance claims or architecture optimization remain in effect.

**Decision (2026-10-14):** Transfer the entire initial performance campaign from M8 to P0/P1. P0, between M3 and M4, measures currently runnable HTTP, streaming, route scaling, compile/apply, failure and footprint behavior. P1 adds HTTP/2/gRPC and policy/JWT/limiter/retry baselines with their first functional M4–M6 slices. M8 owns extended fault/soak and regression evidence.

**Why:** Waiting until all features exist leaves costly hot-path and resource decisions unmeasured. Measuring unimplemented capabilities is equally misleading.

**Consequences:** Every original campaign metric has an owner and dependency in the execution plan. Unavailable rows remain blocked, never passed by a different protocol. Basic bounded operational insight moves forward; full M7 observability/UI remains required. D032's controlled-runner rule is retained.

---

## D048 — Dependencies are selected for product outcomes

**Decision (2026-10-14):** Supersedes D037. Prefer the simplest maintained component that satisfies protocol, security, performance and operational contracts. Reuse mature Go libraries when they reduce defect/maintenance burden or improve measured outcomes. Do not reject a component merely because it solves a learning exercise.

**Acceptance:** Record capability, alternatives, transitive footprint, license, security/maintenance history, upgrade/pinning strategy and integration tests. Hot-path substitutions need matched before/after measurements and protocol regressions. A neutral result does not justify complexity on performance grounds; a separate demonstrated correctness or maintenance benefit may justify it explicitly.

**Boundary:** This approves a selection policy, not an immediate dependency or framework migration. Replacing the proxy engine, durable store, config ownership, or protocol implementation still requires its own ADR, contract tests and migration/rollback plan. Preserve D001–D007, D041, D043 and D045 unless explicitly superseded. No broad plugin/service-mesh framework is added without a workload need.

---

## D049 — Benchmark graphics separate targets from results

**Decision (2026-10-14):** Public benchmark comparisons must identify exact versions, workload, environment, correctness outcomes, uncertainty and reproducible artifacts. Proposed targets, synthetic measurements and external pilot observations are separate evidence classes.

**Why:** An attractive table with predicted competitor numbers is not comparative evidence. Gateway throughput, memory, latency and failure behavior cannot be collapsed into an unsupported “overall” score.

**Consequences:** The target graphic is labeled unmeasured and leaves every result cell pending. Measured graphics are generated from versioned raw data. Publish resource boundaries and whole-stack cost, failed runs, exclusions and loss cases. Unsupported features are N/A, not zero. Target revisions require rationale and retain the previous value; public claims wait for the corresponding reproducible gate.

---

## D050 — Reference-first benchmarks preserve native scope and failure boundaries

**Decision (2026-10-14):** Measure pinned Caddy and Traefik product configurations first, with a minimal Go ReverseProxy in a separate one-route transport-control track. Follow `Gatehouse_COMPETITOR_BENCHMARK_PLAN.md` for category formulas, configuration parity, defaults/tuning, run budgets and data provenance. Derive absolute GateHouse goals from measured results; do not treat those calculations as implementation forecasts.

**Why:** A forwarding library does not implement gateway health/reload/telemetry contracts. Stock monolithic proxies also have different control dependencies from GateHouse's split process/database model. Mixing tracks or inventing a common outage can manufacture an advantage.

**Consequences:** Keep default and tuned provider behavior separate, including Traefik throttling. Missing native capabilities are N/A; missing counters are UNAVAILABLE; supported behavior that fails is FAIL. Never label an admin-listener block as a crashed separate control plane, or simulate an unused PostgreSQL dependency for a peer. GateHouse measurements use Q_all; if adding GateHouse changes common load, references must be rerun. Whole-deployment accounting and original independent safety/latency budgets remain binding under D046–D049.

---

## D051 — Hands-on benchmark environment and learning sequence

**Status (2026-10-14):** Manual learning approach approved by the user. Exact VM topology and any paid resource choice remain open; this decision is not permission to purchase cloud resources or modify existing servers.

**Decision:** Proceed one user-operated step at a time, beginning with a read-only inventory of the user's current machine. The user manually creates and inspects virtual machines and runs services; each command and configuration choice is explained before it is used. Provisioning automation is excluded from this learning sequence unless the user later changes this preference. Start with one disposable Linux VM for learning, then determine whether hardware supports separate driver, gateway and upstream roles. The controlled target remains the competitor plan's two-vCPU Linux amd64 gateway with a 2 GiB parent deployment cgroup ceiling, subject to resource headroom and measurement validity.

**Why:** The user prioritizes understanding the system over setup speed. Manually inspecting OS, CPU, memory, networking, process lifetime and resource accounting makes later benchmark decisions understandable. A VM still does not prove physical isolation; local learning measurements stay labeled diagnostic.

**Consequences:** Preserve D050's native scope and failure distinctions. Do not select a provider or price until local capacity and need are understood. The three-role campaign, calibration, full run matrix, cost ceiling and publication remain later gates. Do not skip a learning step until the user can explain its output.

**First action completed:** User ran the read-only host inventory from E0. Linux x86_64, VT-x and `/dev/kvm` are available; next inspect which KVM management tools are already installed. Exact host specifications remain in the conversation, not this project file. D032 and D046–D050 remain unchanged.

---

## D052 — Defer benchmark execution and resume protocol implementation

**Status (2026-10-14):** Accepted at the user's explicit request to postpone benchmarking and return to project implementation.

**Decision:** Defer the P0/P1 competitor campaign and further benchmark VM provisioning. Proceed with M4 protocol correctness without requiring P0 execution first. This supersedes D047's scheduling prerequisite and pauses D051's environment sequence; retain the benchmark plans, workload contracts, targets and evidence requirements for later execution.

**Why:** The user chose to resume gateway implementation after the manual Linux/forwarding exercise rather than spend more development time on benchmark infrastructure.

**Consequences:** Performance targets remain unmeasured hypotheses. Deferred benchmark checkboxes remain open, including the M4 baseline item; functional protocol acceptance can be reported separately, but a full performance-inclusive gate cannot be claimed closed without its evidence. Do not optimize architecture or publish competitive claims based on assumed performance. The existing Debian lab VM was resized to 4 GiB; two additional clones were not created. An automatic setup helper was prepared but not executed because host administrator authentication was unavailable. Credentials are excluded from project records.
