# GateHouse — Product contract and benchmark targets

**Date:** 2026-10-14 · **Status:** accepted direction; targets are unmeasured hypotheses

**Execution scheduling:** Deferred by user decision D052. Historical P0-before-M4 scheduling below is retained as the campaign design; M4 functional implementation may now proceed first. Workload, measurement and publication contracts remain binding when the campaign resumes, and pending benchmark gates are not passed by deferral.

This is the canonical product, workload, performance, and publication contract. The execution plan owns the checklist. Existing M0–M3 evidence remains valid for its stated tests; it does not establish competitive performance or production readiness.

## Intended user and first product

GateHouse is for developers and small platform teams running roughly 5–20 HTTP/gRPC services on VMs or Docker Compose, who need dynamic routing, safe rollback, and predictable resource use without adopting a service mesh or Kubernetes operator. A Go service developer should be able to use it as an independently deployed gateway; importing GateHouse as a library is not required.

The first product is a self-hosted gateway with immutable revisions, observable activation and rejection, bounded request/stream resources, TLS, authenticated administration and control streams, and a documented upgrade/rollback path. One control-plane process owns instance status (D045); multi-control-plane high availability is outside the first release. A running data plane keeps last-known-good configuration during control/database outages. A cold data-plane restart without a reachable control plane is not covered by that guarantee.

The intended differentiator is **a measured resource envelope plus revision-aware failure insight for small deployments**. Dynamic configuration, HTTP/gRPC forwarding, and metrics are useful prerequisites, not claims of novelty. No competitive advantage has been demonstrated yet.

## What we learned from existing Go projects

| Project / primary source | Established capability | GateHouse consequence |
| --- | --- | --- |
| [Caddy API](https://caddyserver.com/docs/api), [architecture](https://caddyserver.com/docs/architecture) | Graceful configuration loading with rollback; a self-contained Go server | Reloads alone are not a differentiator. Count installation and dependency costs as well as proxy memory. |
| [Traefik configuration](https://doc.traefik.io/traefik/getting-started/configuration-overview/), [metrics](https://doc.traefik.io/traefik/reference/install-configuration/observability/metrics/) | Dynamic routing configuration and operational metrics | A useful gateway must explain traffic and configuration state, not merely expose CRUD. |
| [KrakenD overview](https://www.krakend.io/docs/overview/), [benchmark report](https://www.krakend.io/docs/enterprise/benchmarks/api-gateway-benchmark/) | A Go gateway with an explicit aggregation use case and a published comparison report | Choose a focused use case and publish methods. Aggregation is different work from transparent proxying; do not copy its historical numbers into our scorecard. |
| [k6 open/closed models](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/open-vs-closed/), [wrk2](https://github.com/giltene/wrk2) | Arrival-rate control and coordinated-omission concerns | Offered load, missed arrivals, timeouts, and measurement resolution belong in every report. |

These sources inform the design; their marketing claims and historical benchmark figures are not GateHouse measurements. Pin actual competitor versions and configurations when the campaign starts. Caddy and Traefik OSS are the initial comparison candidates, not presumed losers. KrakenD is a product/methodology reference; add a numeric comparison only for a verified equivalent workload.

The [competitor benchmark execution plan](Gatehouse_COMPETITOR_BENCHMARK_PLAN.md) specifies native configuration, formulas and eligibility for every T01–T09 category. Measure the references first; derive absolute numeric GateHouse budgets from those results; then run GateHouse with the same fixtures. The minimal Go ReverseProxy participates in a separate one-route C1 transport track, not the full C100 gateway track. Native reload, telemetry and control/database dependency categories are N/A for that raw control.

## Frozen workload profiles

All profiles are versioned. Start with a dedicated Linux amd64 gateway host budget of 2 vCPU / 2 GiB RAM. Record the actual CPU, kernel, network, clocks, cgroups, Go/compiler versions, dependency/image digests, and source SHA. This budget describes the gateway host; load generator and upstream run separately and must have measured spare capacity. Also repeat on arm64 before advertising arm64 performance.

| Profile | Configuration and traffic | When runnable |
| --- | --- | --- |
| P100-H | 100 deterministic routes, 20 clusters, 2 endpoints/cluster; HTTP/1.1 keepalive; 1 KiB request and response; uniformly selected routes; 32 persistent 64 KiB/s streams alongside unary traffic | P0, immediately after M3 |
| P100-G | Same topology; HTTP/2 TLS and gRPC unary with 1 KiB messages; 32 server streams; report each protocol independently | Reference conformance can run now; GateHouse waits for M4 + P1 |
| P1000-C | 1,000 routes, same clusters/endpoints; publish one full revision/second during P100-H or P100-G load; 1,000 revisions; up to 1 ms control-to-data RTT | P0 HTTP; M4 gRPC extension |
| P-POLICY | P100-H/P100-G with JWT hot cache, key refresh, local rate limiting, retry budget, and admission; measure each feature separately and then combined | First functional M5/M6 slice + P1 |
| P-FAULT | Fixed demand below saturation; 60-second control outage, 60-second database outage, invalid publish, endpoint resets, slow bodies, overload and telemetry-sink outage, separately | Available faults in P0; full matrix after M6 |

Route-scale runs use 1, 100, 1,000 and 10,000 routes, including worst-case host/header selector collisions. Payload sensitivity runs use 1 KiB, 64 KiB and 1 MiB, plus a 128 MiB streamed response. Report HTTP plaintext, TLS and gRPC separately. Publish stream/unary admission limits; if the current total limit is 128, allocate stream slots within it rather than silently increasing it. Connections and multiplexed streams are distinct counts.

For matched-load metrics, first determine each implementation's sustainable unary capacity: correct responses for at least 99.9% of offered requests, client p99 at most 20 ms, and no resource-budget breach throughout a five-minute run. Then set common offered unary load `Q = 0.5 × min(capacity)` across GateHouse and eligible production references. Keep the 32 background streams identical. If Q is below 1,000 requests/s, report an unsuccessful initial capacity objective; do not relabel it a production-performance win. Repeat at 25%, 50%, 75%, 100% and 125% of the minimum capacity to expose overload behavior.

Before GateHouse is measured, use Q_ref from Caddy/Traefik product capacities. Adding GateHouse establishes Q_all; if that changes Q, remeasure references/direct at the new load. Never reuse higher-demand reference RSS/latency against a lower-demand GateHouse run. C1 transport-control load is frozen independently. Per-candidate faults retain their native meaning; a monolithic admin-listener block is not mislabeled as a separate control-process crash, and references without PostgreSQL have a database-outage N/A row.

## Provisional targets, not measured results

These numbers are engineering budgets chosen for the first product; no source establishes that GateHouse achieves them. P0 must measure feasibility. A miss stays visible and requires a documented optimization, narrower workload, or explicit target amendment with rationale. Never lower the bar silently.

| ID | Metric | Proposed target | Scope / acceptance evidence |
| --- | --- | --- | --- |
| T01 | Data-plane peak RSS at Q | **≤75% of the lower-RSS eligible Caddy/Traefik reference** and ≤128 MiB | P100-H, then independently P100-G; same stream/health/metrics capabilities. This is a 25% memory-reduction hypothesis, not a total-stack saving claim. |
| T02 | Sustainable correct unary requests/s per allocated vCPU | **≥90% of the faster eligible Caddy/Traefik reference** | Same 20 ms p99/error/resource criteria, fixed CPU budget and protocol; report actual req/s/core as well as ratio. |
| T03 | Client p99 latency difference vs direct upstream | **≤2 ms** | At the same Q; `p99(gateway) − p99(direct)`, reported as a distribution difference, not a measured per-request proxy time. |
| T04 | Correct streaming payload goodput | **≥90% of direct upstream** | Separate streaming-only capacity run, 32 streams, same payload and cancellation semantics; also publish mixed-load interference. |
| T05 | Publication-to-activation latency | **p95 ≤250 ms** | P1000-C; operator publish call start through first externally observed response using the acknowledged revision; include DB commit, compile and delivery; coalesced/skipped revisions count as not activated, not fast successes. |
| T06 | Correct request success during control/DB outages | **≥99.9% of offered unary requests**, zero interrupted established streams attributable to control loss | Warm data plane, healthy upstream, same Q, bounded P-FAULT windows; distinguish deliberate overload/policy rejections and upstream failures in separate categories. |
| T07 | Basic telemetry CPU cost | **≤10% added CPU-seconds per successful request** | Metrics + reason-coded access logging vs disabled, same Q; include fixed 1% trace sampling separately. Drops and sink outages remain visible. |
| T08 | Whole deployment memory | **Peak cgroup memory ≤768 MiB** | Data plane + control plane + PostgreSQL under P100-H; report each component and deployment sum, shared pages/cache rules, plus operator-observability stack separately. Data-plane RSS is never substituted for this metric. |
| T09 | Resource stability and recovery | **Post-warmup live heap growth ≤24 MiB; goroutines ≤baseline +8** | Fixed P1000-C fixture and 60-minute soak; separately measure peak RSS during reload, retained old revisions, open connections and post-fault drain. Existing smaller M3 fixture does not prove this envelope. |

Core performance acceptance requires T01–T09 on all supported profiles plus the semantic/security gates. The post-M6 beta requires each then-runnable metric, including basic T07 metrics/logging; full tracing cost remains explicitly pending until M7. An unsupported reference feature is N/A, not zero or failure. A comparative claim needs both references to be eligible for that metric, or must explicitly narrow the claim to the one eligible reference. No single weighted “overall” score hides a latency, security, or reliability regression.

## Fair measurement and public evidence

1. Freeze matching route, health, connection, TLS, stream, logging and metrics behavior before measurement. Use a minimal pinned Go `httputil.ReverseProxy` as a control, and the direct upstream as calibration. They are not full-product competitors. JWT/retry comparisons require equivalent implementations or separate feature-overhead panels; do not disable work only in GateHouse.

   For T05, make the changed route return a unique revision sentinel and observe it externally for every candidate. Map each native API/load/file-reload start and completion explicitly. GateHouse additionally verifies its ACK; competitors without GateHouse's revision/ACK protocol are not penalized for lacking it. Missing comparable reload capability is N/A. Distinguish API return, ACK, external visibility and missed/coalesced activations.
2. Pin releases/images and archive runnable configs, scripts and commands. Run the three roles on separate machines or isolated CPUs with recorded network topology; single-host results are development diagnostics, not external performance claims. The load generator/upstream must remain below 70% CPU and show no limiting NIC, dropped-arrival, or connection exhaustion; otherwise mark the run invalid and adjust the setup openly.
3. Use open arrival-rate load where supported, record planned/started/completed/missed requests, and account for coordinated omission. Report gRPC status/trailers and payload correctness, not HTTP 200 alone. Include failures/timeouts in the outcome denominator and publish their latency/censoring treatment. Never merge unary and streaming latency distributions.
4. Use 30 seconds warmup then five minutes measurement, at least five repeats in randomized implementation order. Publish per-run values, medians, ranges and 95% bootstrap confidence intervals at the run level. A ratio passes only when its conservative confidence bound clears the target; a tie within noise is inconclusive.
5. Verify driver timing resolution before T03. wrk2 documents roughly ±1 ms granularity, so it alone cannot substantiate a fine 2 ms latency-difference boundary. Calibrate a finer-resolution driver or mark that comparison inconclusive. Record histogram resolution and scheduled-start vs transport latency.
6. Capture CPU, heap, allocation, mutex/block and goroutine profiles in separate diagnostic runs; profile overhead is excluded from headline runs. Record CPU-seconds/request, peak/steady RSS, cgroup memory, GC, connections, stream counts, active revisions and retry amplification. Publish total deployment footprint as well as data-plane figures.
7. Archive raw JSON/CSV/histograms, environment manifest, source SHA, configs and failures in a versioned report. Generate any measured scorecard from that dataset. Include losing runs, feature exclusions and uncertainty; a public headline links to the report and reproducible commands.

The target scorecard at `assets/gatehouse-benchmark-targets.png` is a planning graphic. All competitor and GateHouse result cells are pending. Its JSON source contains no measured competitor figures. Do not use its target shading as a ranking or its target row as achieved performance.

## Architecture review against these targets

This is a source-grounded review of the current M3 design, not a fresh repository-wide defect audit.

| Boundary / current evidence | Target at risk | Decision and experiment |
| --- | --- | --- |
| Immutable route trie (`internal/router/router.go`) | T02, T03 | Retain deterministic matching; measure adversarial selector buckets as well as average paths. Index selectors only if profiles justify it. |
| Pairwise ambiguity validation in `router.Compile` | T05 | Current nested route loops are quadratic. Measure 1k/10k compile time and allocations before M4; investigate selector grouping/indexing if activation misses the target, preserving rejection semantics. |
| Manager mutex around acquire/release (`internal/gateway/manager.go`) | T02, T03 | “Atomic activation” does not mean a lock-free request path. Measure contention at 1/2/4 vCPU; change snapshot ownership only with race and in-flight-request proofs. |
| Per-endpoint proxies/transports and retained cluster registry (`internal/gateway/registry.go`) | T01, T08, T09 | Measure sockets, idle pools, health workers and simultaneous old/new revisions. Configure explicit limits; consider immutable-compatible pooling only if ownership/TLS tests remain valid. |
| Control stream polling and one registry per process | T05, T08 | Measure hot publication and missed-notification reconciliation separately, plus connection/session scaling. Add explicit instance/config bounds if absent. No DB work enters request routing. |
| PostgreSQL plus two runtime roles | T08 / installation friction | Keep existing durability, but count its cost. If it defeats the small-deployment envelope, evaluate an embedded durable store or shared managed PostgreSQL in a separate ADR with equivalent transactions/migrations/recovery tests; no store replacement is approved here. |
| Current loopback development mode; TLS/auth remain M6 | Shipping gate | Performance wins cannot excuse insecure public administration. Preserve D043; no public production release until secure mode is tested. |
| Operational metrics/traces mostly planned in M7 | T07 / product usefulness | Bring bounded request/revision/error metrics and reason-coded diagnostics into the first P0/P1 functional slices. Complete traces, collector-failure behavior and operator UI in M7; a UI is not needed to collect baseline evidence. |

The split architecture stays. No runtime optimization or new dependency is implemented by this planning change. P0 must produce a keep/change/reject decision for every row; lasting behavior changes get ADRs and meaningful regression tests.

## Real traffic, insight, and release readiness

Synthetic load demonstrates behavior under defined conditions; it is not evidence of real adoption. After M4–M6 security/protocol gates and early telemetry, ship an explicitly labeled opt-in beta with a versioned binary/image, Compose deployment, secure quickstart, limits, diagnostics and rollback instructions. A fresh user must be able to configure, send HTTP/gRPC traffic, publish/rollback, and diagnose an injected outage without agent-only setup knowledge.

Before claiming external production use, record at least one consenting external deployment running a real non-sensitive workload for seven days. Report observed volume, concurrency, tail latency, failure causes, reloads, resource cost and operator feedback. If no adopter is available, beta can still be published after the technical gates, but adoption remains unproven. Do not fabricate users or treat our own generated traffic as customer traffic.

The insight surface must answer: Which route/revision served this request? Was failure caused by upstream, admission, timeout, auth or control staleness? Which revision was rejected and why? Did CPU, memory, connections, queues or retries grow? Expose bounded metrics and sampled traces/reason-coded logs with configured cardinality limits. Revision IDs belong in controlled logs/traces and status, not unbounded metric labels. Never record bodies, tokens, query strings or arbitrary user IDs by default. Obtain consent for pilot telemetry and publish only aggregates/sanitized fixtures.

## Delivery order

- **P0 before M4:** HTTP/stream/route/compile baselines, comparison harness and profiles, total footprint, basic insight, architecture review. Leave unavailable protocol/policy rows explicitly blocked.
- **P1 alongside the first M4–M6 slices:** baseline HTTP/2/gRPC, each policy, JWT, limiter, retry/failure overhead before optimization; complete every row of the original performance campaign here, not first in M8.
- **Pilot gate after M6 + early telemetry:** secure, reproducible beta, full supported-protocol targets, real-traffic admission and visibility. External-adoption proof is tracked separately from synthetic results.
- **M7:** complete observability/UI and measure its cost. **M8:** extended fault/soak and regression campaign against existing baselines. **M9:** fresh build/upgrade, release provenance and external-usage evidence.

Targets and public claims are governed by D046–D049 in `Gatehouse_DECISIONS.md`.
