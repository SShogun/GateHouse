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

The existing `gatehouse.v1` Protobuf file and its `go_package` option remain unchanged because Buf's `FILE` compatibility rule treats changing that option as breaking. That v1 namespace is frozen; new contracts use `gatehouse.v2` and the canonical module path. This is a source/package-path transition, not a wire-format migration; v1 currently defines no application messages.
