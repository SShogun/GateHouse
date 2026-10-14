# GateHouse — Competitor benchmark execution plan

**Date:** 2026-10-14 · **Status:** research and measurement design complete; implementation and runs pending

**References:** Caddy OSS, Traefik OSS, and a pinned Go `net/http/httputil.ReverseProxy` control. Direct upstream is a calibration path. The product contract is `Gatehouse_PRODUCT_BENCHMARKS.md`; the execution checklist remains in `Gatehouse_PLAN.md`. This plan specifies the next baseline work and does not claim measured competitor numbers.

**Environment proposal:** [VM provisioning and operation plan](Gatehouse_BENCHMARK_VM_PLAN.md) defines the three machine roles, deployment resource boundary, local diagnostic mode, approval/cost controls, implementation slices and cleanup. It is awaiting approval and does not authorize infrastructure creation.

The next campaign measures the references first. We then derive absolute GateHouse budgets from those results and compare GateHouse on the same fixtures. A computed target is not a prediction of GateHouse's achieved performance. Published/vendor numbers from different hardware, configurations or years are not entered into this dataset.

## 1. Reference implementations and separate comparison tracks

| Candidate | Exact thing to run | Product scope / native mechanism |
| --- | --- | --- |
| Caddy | Stock OSS release binary or official image pinned by version, digest and build metadata | Native routing, proxy pools, HTTP health probes and observability; replace JSON through `POST /load`. |
| Traefik | Stock OSS release binary or official image pinned the same way | Native routers/services, HTTP health probes and observability; directory-watched file provider for standard routing/reload runs. |
| Go-RP/raw | Small source-pinned binary compiled with Go 1.26.9: one `ReverseProxy`, explicit listener/transport, one upstream | Forwarding/streaming/TLS control; no gateway-owned routes, health scheduler, reload API, metrics middleware or database. |
| Go-RP/pooled | Optional second build/config of the same control using a bounded 32 KiB `BufferPool` | Separate optimization sensitivity, never substituted for raw results. Report both and its added memory. |
| Direct upstream | Same upstream binary, payload, protocol and scheduled client fixture | Network/application calibration; not a gateway, and not an efficiency reference for full-product claims. |

Pin exact releases and archive effective configurations before execution; do not use `latest` images. Record official binaries' embedded Go versions instead of claiming all were built with the same compiler. If a common-toolchain source rebuild is useful, report it as a separate experiment, conditional on that release supporting the selected compiler. No forked competitor implementation enters the stock-product panel.

**Transport track C1:** one route, one upstream, no health/policy/access logging/application metrics, identical payload, client limits, forwarding/header contract and protocol. Compare all three references plus direct. Go-RP is comparable here; a lower memory figure here says nothing about a 100-route gateway.

**Product track C100:** P100-H/P100-G from the product contract: 100 logical routes, 20 clusters, 40 upstream endpoints and 32 established streams. Use idiomatic native routing and shared service/handler definitions; do not force one product to duplicate pool/health workers per route. All logical paths and cluster destinations must be exercised and verified. Caddy and Traefik are eligible after conformance; Go-RP/raw is N/A for this track. An optional Go router/health wrapper would be a separately named custom reference, not “Go ReverseProxy.”

**Reload/stability track C1000:** P1000-C (1,000 routes, 1,000 version changes). Native reload defaults and tuned settings are distinct panels. Never let results from one-route C1 substitute for C100/C1000 acceptance. Freeze certificate, protocol, endpoint and route semantics in a capability manifest; an unsupported capability is N/A, a supported capability that fails a test is FAIL, and an unavailable counter is UNAVAILABLE.

## 2. Native controls and research findings

### Caddy

[API documentation](https://caddyserver.com/docs/api) defines configuration loading/rollback. Use JSON rather than timing a Caddyfile adapter: generate both formats outside timed runs if useful, but keep configuration parsing/validation performed by the server inside reload timing. Record API response and externally observed route activation separately.

[Reverse-proxy documentation](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy) describes active probes, per-handler state and streaming controls. Use native shared handlers where possible; verify actual outgoing probe counts. Preserve default flush behavior in the primary run. HTTP unknown-length streams and gRPC must pass progressive-delivery/cancellation checks before goodput is comparable. The documented default reload closure is specifically about WebSockets; do not assume it applies to every HTTP/gRPC stream. Any stream-preservation tuning is an additional measured panel, including retained resources.

[Metrics documentation](https://caddyserver.com/docs/metrics) supplies native request and runtime measurements. Aggregate request outcomes/duration and explicitly configured access logging form the common basic-telemetry panel. Do not invent per-route labels to match another product.

### Traefik

[File-provider documentation](https://doc.traefik.io/traefik/reference/install-configuration/providers/others/file/) documents directory watching and a default two-second provider throttle that can discard intermediate events. Mount the parent directory and atomically replace a file inside it. Keep the default panel; add a separately labeled 100 ms throttle panel only after verifying the pinned release accepts it. Sparse reloads wait for observed completion; one-Hz churn retains coalesced/missing updates in its denominator.

[HTTP-provider documentation](https://doc.traefik.io/traefik/reference/install-configuration/providers/others/http/) supplies a distinct provider-loss experiment. Its documented polling default is five seconds; record it and the throttle. That experiment includes a configuration-server fixture and its resource cost. It is not the default file-provider deployment and is not called a PostgreSQL outage.

[Service documentation](https://doc.traefik.io/traefik/reference/routing-configuration/http/load-balancing/service/) defines load balancing, health checking and flush settings. Verify equivalent upstream distribution, probe traffic, streaming and HTTP/2 negotiation in conformance. Any proxy-engine option is pinned; do not enable an experimental engine silently.

[Metrics documentation](https://doc.traefik.io/traefik/reference/install-configuration/observability/metrics/) distinguishes aggregate and router/service labels. Match the common aggregate telemetry contract; expose router insight separately with its series count and cost.

### Go-RP

[The standard-library contract](https://pkg.go.dev/net/http/httputil#ReverseProxy) and locally inspected Go 1.26.9 `reverseproxy.go` are the source of truth. Use `Rewrite`/`SetURL`, one persistent cloned transport and explicit `http.Server` settings. Pin Host/forwarded-header behavior, timeout/idle/pool limits and TLS negotiation; never construct a transport per request. Raw means nil `BufferPool`, default streaming flush semantics, no hidden instrumentation or reload wrapper. Preserve application bodies, status/trailers and cancellation.

TLS HTTP/2/gRPC can be investigated for all references now, even before GateHouse M4. The Go control must explicitly negotiate HTTP/2 on both legs and pass real gRPC conformance. Cleartext h2c is a different optional configuration/dependency, not an assumed stdlib capability. GateHouse protocol rows stay BLOCKED until M4 proves them.

## 3. Configuration contract before timing

- Dedicated Linux amd64 gateway host: 2 allocated vCPU and 2 GiB cgroup memory ceiling. Generators and upstreams run separately with recorded CPU isolation/topology. The later GateHouse deployment includes data plane, control plane and PostgreSQL within the same declared host budget. Shared CI/local loopback is a labeled diagnostic mode, not the public comparison environment.
- One common client fixture: 96 unary HTTP/1.1 persistent connections plus 32 stream connections in C100; C1 uses 128 unary connections. HTTP/2 uses separately declared connection and stream limits with the same maximum 96 unary / 32 stream operations for C100. Count streams, not merely TCP connections. Sensitivity tests change these numbers for all candidates together.
- Unary route requests: deterministic 1 KiB body, 1 KiB reply, request ID, expected cluster and digest; one-second request deadline measured from scheduled start; no artificial upstream delay in the healthy baseline. All candidates preserve identical application semantics. Use method/path combinations supported identically; avoid ambiguous/normalizing path cases in the baseline and test them separately.
- Use 20 distinct service definitions with two equally eligible endpoints each. HTTP active health contract: healthy 200, GET `/health`, five-second interval, one-second timeout, one pass/fail where configurable. Observe actual probe multiplicity and expose implementation differences; do not disable health only in the heavier candidate. C1 has all health disabled.
- Compression, cache, JWT, retries and request/response transformations are off for the common baseline. Record native buffering/flush, connection reuse, transport limits and stream timeouts. An explicit setting unsupported by a candidate is disclosed and either excludes a strict-parity panel or moves the run to a default-behavior panel.
- Separate HTTP/1.1 plaintext, HTTP/2 TLS and gRPC/TLS datasets. Use the same test CA, server certificate/key and negotiated TLS version on all eligible paths; run both direct HTTP/2 and direct gRPC calibration. No ACME/public DNS dependency in the harness.
- Direct C100 calibration selects the same expected cluster/endpoints without traversing a gateway, using the frozen traffic distribution; it does not replace the 40-endpoint workload with one unusually fast upstream. Verify upstream CPU/NIC and per-endpoint counts at every capacity bracket.
- Basic telemetry has named off/metrics/logging/combined panels. Use identical scrape frequency (one second) and common aggregate semantics; record actual series/bucket counts and access-log fields. Collector and log-drain run outside candidate CPU budgets; verify they keep up. Include any local logging buffers/cache in candidate memory.

Conformance must establish all paths, all upstreams, byte digests, proper status/trailers, progressive streams, cancellation and connection reuse before measurement. A native stream that is buffered or corrupt is FAIL, not high throughput. Warm all routes and pools, then run 30 seconds warmup and 300 seconds measurement, five randomized repeats with fresh candidate processes. Do not drop host caches selectively or tune one candidate after seeing its final score.

## 4. Every category: formula, instrument and eligibility

Counters below refer to a single cohort of operations scheduled in the 300-second measurement window. `N_offered` includes scheduled requests that were not started. `N_correct` includes only payload/status-correct responses completed within the scheduled-start deadline, including the bounded final one-second completion tail. Correct throughput is that cohort's correct count divided by 300 seconds. Measure CPU from cohort start through its bounded completion/drain horizon and archive those exact timestamps; do not count warmup responses, later operations or unpaid asynchronous logging work. Use scheduled-start latency for capacity/overload accounting and separately publish transport-start latency. Lost/failed/timed-out requests never disappear from the denominator; do not present success-only p99 as the full outcome distribution.

| Category | Calculation / units | Caddy | Traefik | Go-RP/raw |
| --- | --- | --- | --- | --- |
| T01 data-plane memory | Sample `/proc/<pid>/status` VmRSS every 100 ms; `RSS_peak = max(samples)/2^20` MiB, plus median/p95 RSS and VmHWM. Fresh processes make high-water lifetime explicit. | C1 and C100 proxy process, includes in-process administration | C1 and C100 proxy/provider process | C1 only; no fair C100 comparison |
| T02 sustainable capacity | Highest verified scheduled rate satisfying `N_correct/N_offered ≥0.999`, p99 ≤20 ms and declared resource ceiling in each confirmation run. Report passing/failing rate bracket, correct req/s and `correct req/s / 2 vCPU`. CPU cost is separately `CPU_seconds/N_correct`. | C1/C100, independently per protocol | C1/C100, independently per protocol | C1, independently per proven protocol |
| T03 latency delta | Client p50/p95/p99 and `p99(candidate,Q) − p99(direct,Q)` milliseconds, paired run blocks. Not per-request proxy time. Timing/probe resolution recorded. | C1/C100 | C1/C100 | C1 |
| T04 stream goodput | Verified application payload bytes / measured seconds / `2^20` = MiB/s; ratio to direct with identical 32-stream fixture. Report per-stream progress, fairness and interrupted streams. | Conformant HTTP/gRPC | Conformant HTTP/gRPC | Conformant HTTP/gRPC |
| T05 reload visibility | `t_first_correct_sentinel − t_submit_start`, milliseconds; report p50/p95/p99, completion fraction, pending/coalesced updates and request/stream disturbance. | Native JSON load API, defaults and optional tuning labeled | Directory replacement; default and tuned throttle separate | N/A: no native configuration model |
| T06 outage behavior | `100 × N_correct/N_offered` during known fault window; interrupted established streams / streams open at fault start, recovery time and error reasons separately | Loss of access to admin endpoint with proxy process alive; native invalid reload/recovery | Provider-update loss with process alive; optional HTTP-provider server outage | No control/provider/DB outage claim; generic upstream fault control only |
| T07 telemetry cost | For matched Q: `c = Δcgroup_cpu_seconds/N_correct`; cost `100 × (c_on/c_off − 1)`%. Report native metric/log capabilities, latency/RSS change and drained/drop counts. | Native aggregate metrics/log panels | Native aggregate metrics/log panels | N/A for native telemetry; any instrumented wrapper gets its own name |
| T08 whole deployment footprint | Whole-deployment parent cgroup `memory.current` series and `memory.peak` in MiB. Include all required local processes; report idle/load/reload phases and per-child breakdown without double counting shared hierarchy. | Single process + state/config footprint; optional external collector reported separately | File-provider single process; HTTP-provider fixture explicitly included in its variant | Raw process only, explicitly reduced scope |
| T09 stability | Comparable external memory/connection/drain curves over 1,000 reloads + 60-minute fixed-load soak. Also heap/goroutine deltas only when native diagnostic counters are available and comparable. | Native reload; runtime metrics/diagnostic availability recorded | Native reload; runtime metrics/diagnostic availability recorded | Load/stream/cancel soak; reload and unavailable runtime counters N/A |

T01 sampled RSS and T08 cgroup peak are different measurements. Do not relabel cgroup memory (which includes charged caches and other memory) as RSS. Reset or use a fresh cgroup for every run; capture OOM/throttling events. Total peak is the parent group's simultaneous charge, not the sum of independently observed child peaks. [Linux cgroup v2 documentation](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html) governs CPU and memory accounting.

For T09, record steady load before and after reload and after a bounded drain with all benchmark streams ended. Do not compare a GC-forced Go control with ordinary stock-product heap readings. Retained Go heap, goroutines and mutex profiles can be diagnostic evidence without becoming a headline comparative score when other candidates cannot expose equivalent counters. Startup peak, process RSS, heap allocation, live heap and cgroup memory are separate fields.

## 5. Load calibration and capacity search

Use k6's pinned `constant-arrival-rate` executor for standard HTTP/gRPC unary runs if its connection/arrival controls pass the fixture; one iteration equals one request, with no user `sleep` throttling. Preallocate sufficient VUs and export missed/dropped iterations and exact operation counts. Independent bounded stream clients validate sequence/digest and scheduled progress; long-lived streams are not short unary iterations. [k6 executor](https://grafana.com/docs/k6/latest/using-k6/scenarios/executors/constant-arrival-rate/), [dropped iterations](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/dropped-iterations/).

A capability probe must demonstrate the specified connection/stream budget. If k6 cannot enforce that or expose the required scheduled-start/histogram data, use a small pinned Go driver implementing the same frozen arrival contract; do not silently change the workload. Driver validation includes injected stalls, exact counter conservation, clock/timer resolution, payload checks, protocol negotiation and cancellation. Reconcile planned operations with started/completed/failed/cancelled/dropped totals. k6 default summary percentiles alone are not the raw report.

1. Calibrate generator and upstream on direct traffic at more than the intended load; require <70% CPU and no NIC/socket/connection-budget bottleneck. Measure observer overhead. Establish timing resolution suitable for T03 (target below 0.2 ms), or label the latency-boundary result inconclusive.
2. Explore each candidate/protocol starting at 250 requests/s and doubling for at most ten steps; a failing initial rate is bracketed down with at most five steps. Use 30-second exploratory windows only for bracketing, never headline results. Stop on safety ceilings/generator invalidity; report bounded/unbracketed capacity honestly.
3. Refine the passing/failing bracket with at most six steps, stopping at ≤5% relative bracket width. Verify the highest proposed passing rate with five full 30-second warmup + 300-second trials; if one fails, step down and reconfirm within the allocated run budget. Report the remaining interval if budget expires; do not extrapolate an exact maximum.
4. Freeze `Q_ref = 0.5 × min(Caddy_C100_capacity, Traefik_C100_capacity)` for the reference-only product panel. C1 has its own Q including all three transport controls. Go-RP/raw never determines the C100 product load.
5. Measure memory, CPU and latency at 25%, 50%, 75%, 100% and 125% of the slower eligible production reference's capacity. Use the same Q and route/stream fixture for direct latency calibration. Mixed-load benchmarks keep 32 background streams active; the separate streaming-only capacity run ramps offered bytes, not unary RPS.
6. When GateHouse is eligible, establish its capacity and freeze `Q_all = 0.5 × min(reference capacities, GateHouse capacity)`. If Q changes, rerun references and direct at Q_all; never compare GateHouse's lower-demand measurements to the old Q_ref memory/latency figures. Q below 1,000 requests/s remains a missed initial objective, not a useful performance win.

Within a valid run, driver-side dropped arrivals caused by insufficient generator CPU/provisioning invalidate the capacity estimate; gateway rejection/timeout under a properly provisioned generator is a candidate outcome and remains in the denominator. Waiting for the frozen client connection/operation budget because a candidate stalls is modeled backpressure: record scheduled queue delay and deadline outcomes, not a convenient “invalid generator” exclusion. If the driver cannot distinguish these causes or account for planned operations, it fails Part A. When latency is censored by a request deadline, publish the censored count and deadline, completed-response quantiles and schedule-to-outcome quantiles separately. Do not replace unresolved latency with a fabricated fast value.

## 6. Reload and fault experiments need different boundaries

**Sparse reload latency:** generate a 1,000-route configuration outside timing, then change a routing/header sentinel emitted by the gateway. The same fixed upstreams stay up; do not time an unrelated upstream deployment. Start a monotonic clock in the orchestration process immediately before native submission. Send sentinel probes every 5 ms from that same host, with sufficient HTTP/gRPC concurrency to avoid self-serialization. First externally verified new sentinel bounds visibility; add up to a probe interval plus measured scheduler error to its uncertainty. Collect API/file-write completion, first visibility and stable visibility separately. Sparse updates are at least five seconds apart and wait for terminal outcome.

**Churn:** publish at one Hz for 1,000 updates with unary/stream traffic. Track each submitted sentinel; out-of-order, skipped and coalesced updates remain separate, and the completion fraction is reported alongside conditional latency quantiles. Invariant: no observed routing regression or corrupted response. A default throttle may intentionally coalesce updates; that is a native policy outcome, not a fast activation or a protocol bug.

**Caddy faults:** gate access to its admin listener for 60 seconds without terminating the proxy process; inject an invalid configuration through the API; restore administrative access and publish a valid sentinel. Killing the monolithic process would kill the data plane too and is not comparable to GateHouse control-plane loss.

**Traefik faults:** prevent provider updates with the running proxy and upstreams healthy. For a true external-provider-loss case, use a separately pinned HTTP-provider fixture, stop that server for 60 seconds, then restore it and externally verify a new sentinel. Validate native error/recovery signals. File-provider runs have no database/network control service to cut, so that specific row is N/A.

**Go-RP faults:** upstream delay/reset, client cancel, huge stream and bounded saturation are meaningful transport controls. PostgreSQL loss, control-plane loss, failed native reload and rollback are N/A. Adding a fake unused database and showing 100% success would not measure dependency resilience.

**GateHouse later:** separately cut its actual control process and PostgreSQL path, preserve the warm data-plane process, await server completion for failed stream/persistence paths, restore dependencies, verify a new boot/revision and request outcome. Existing M3 tests inform correctness; timed P-FAULT trials provide new workload-specific evidence. Do not equate a blocked admin listener with a crashed separate control process; show the fault kind in each result.

All operations have budgets: readiness 30 seconds; each native update/probe sequence 10 seconds; no indefinite retry loops; five-minute load trial process deadline 360 seconds including drain; 60-second dependency cut in a 120-second observation window; churn run 1,100 seconds; 60-minute soak process deadline 3,700 seconds. Cleanup must reap driver/server processes and close streams within ten seconds after termination. Wall-clock durations define experiments, not synchronization sleeps: wait for actual readiness, activation, stream end and drain outcomes using bounded polling/events.

## 7. Traffic insights are a capability panel, not an invented score

For each injected fault archive request outcomes, upstream counters, native error logs, native health/reload/connection signals and the external timeline. An operator should distinguish overload, upstream failure, invalid configuration and stale control state. Record whether that evidence is native, supplied by the harness or unavailable. Do not attribute externally observed revision sentinels or an added wrapper's reason codes to stock products.

The first panel is an evidence/capability checklist, not a subjective “insight 92/100” score. Detection latency can be measured only for a named native signal with a fixed definition and collection interval. Human diagnosis time requires a separate controlled user study; it is not inferred from a throughput run. No request bodies/tokens or unsanitized customer traffic enter public artifacts.

## 8. Analysis, results and absolute GateHouse budgets

Store five repeated randomized run blocks, per-run measurements, median, range and run-level 95% bootstrap intervals with fixed analysis seed/method. A five-run interval is modest evidence; show every run and inconclusive cases rather than giving unjustified precision. For capacity report the verified rate bracket as well as replicate variability. For paired deltas/ratios, bootstrap whole run blocks; do not treat millions of requests from one trial as millions of independent environment replicates.

The output table contains actual units and measured values, not just comparator operators:

| Candidate / exact version / track | Correct req/s/core | Peak RSS MiB | Client p99 ms / delta ms | Goodput MiB/s | Reload p95 ms + activation fraction | Fault-window success % / interrupted streams | CPU µs/correct request / telemetry cost % | Whole-stack peak MiB | Soak / recovery outcome |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Caddy — pending execution | Pending | Pending | Pending | Pending | Pending | Per named fault | Pending | Pending | Pending |
| Traefik — pending execution | Pending | Pending | Pending | Pending | Default / tuned pending | Per named fault | Pending | Pending | Pending |
| Go-RP/raw — C1 only | Pending | Pending | Pending | Pending | N/A | Generic upstream faults only | CPU pending; native telemetry N/A | Pending (reduced scope) | Reload N/A; load soak pending |

Do not copy this structural table into a numeric leaderboard until data exists. Each protocol/track/load gets its own panel; a combined row does not mix C1 capacity with C100 memory or C1000 reload. The raw report is canonical; round graphic values only after uncertainty analysis.

After the reference-only report, compute **absolute target budgets** on that frozen profile:

- GateHouse RSS budget (MiB) = `0.75 × lower eligible reference peak-RSS estimate`, also retaining the independent 128 MiB ceiling. Display the stricter resulting numeric budget, plus which reference/uncertainty produced it.
- GateHouse capacity goal (correct req/s/core) = `0.90 × faster eligible reference capacity estimate`; retain its bracket/interval. This is a derived goal, not an achieved speed figure.
- GateHouse total client p99 budget (ms) = `direct p99 at the same Q + 2 ms`. The latency-delta budget remains 2 ms and must pass the calibrated-resolution check.
- GateHouse streaming goal (MiB/s) = `0.90 × direct goodput` for the same protocol and fixture.
- Activation 250 ms, outage 99.9%, telemetry 10%, deployment 768 MiB and stability budgets remain independent product hypotheses. Competitor observations inform an explicit target review; they do not automatically replace correctness/reliability requirements.

Use conservative uncertainty bounds for acceptance, not rounded means: the upper confidence bound for GateHouse/reference RSS must be ≤0.75; lower bounds for capacity/goodput ratios must be ≥0.90. Numerical comparator measurements can refine the product direction, but cannot forecast a not-yet-measured implementation.

## 9. Bounded implementation and run sequence

All paths below are **planned**, not implemented commands or files. This task writes the plan only; it does not install tooling, provision hosts, execute load or publish a competitive claim.

### Part A — Harness and conformance

1. **Lock environment and artifacts.** Files: `benchmarks/manifest.json`, `benchmarks/README.md`. Acceptance: exact tags/digests/builds, source/fixture SHA, host budgets, run matrix and tool/observer calibration fields are recorded. Verification: resolve versions, inspect effective process flags, validate cgroup/topology access. Dependency: available execution hosts; local diagnostic mode may precede publication-grade hosts.
2. **Build upstream/direct and raw Go control.** Files: `benchmarks/upstream/main.go`, `benchmarks/go-reverseproxy/main.go`, `benchmarks/control_test.go`. Acceptance: fixture digests/status/stream sequence, TLS/H2/gRPC and cancellation checks pass; raw vs pooled/control scope is explicit. Verification: meaningful positive and failing contract probes; bounded startup/drain tests. Dependency: step 1.
3. **Generate native configs.** Files: `benchmarks/configs/caddy.json`, `benchmarks/configs/traefik-static.yml`, `benchmarks/configs/traefik-dynamic.yml`, `benchmarks/conformance.md`. Acceptance: C1/C100/C1000 route/upstream/probe/telemetry manifests match intent; default/tuned variants are archived; all reference conformance probes pass or are correctly labeled. Verification: exercise every generated logical route and protocol, effective config and observed upstream/health counters. Dependency: steps 1–2.
4. **Build driver/observer/orchestrator.** Files: `benchmarks/driver/`, `benchmarks/observe.py`, `benchmarks/run.py` (slice these further if a module exceeds one focused session). Acceptance: no lost operation counts, calibrated scheduled latency, bounded fault/update/teardown waits, memory/CPU units and fake stalled-driver controls verified. Verification: small known-size direct run plus injected stalls/failures/censored outcomes. Dependency: steps 1–3.

**Part A gate:** pinned and equivalent candidate configs; all applicable protocol/payload probes pass; driver/upstream/observer are not the bottleneck; non-applicable and unavailable measurements are explicit. Do not proceed to headline runs on a failed or uncalibrated fixture.

### Part B — Reference measurements

5. **Capacity and matched-load HTTP.** Use C1 and C100-H, direct calibration, bounded bracket/refinement and five randomized confirmation blocks. Acceptance: valid correct-capacity bracket plus raw CPU/RSS/p99/connection outcomes at Q_ref; no changed effective config between repeats. Verification: direct-driver counter conservation and independent parsing of actual units. Dependency: Part A.
6. **Streams and reference H2/gRPC.** Acceptance: goodput, progress/cancel, client status/trailers and protocol-specific capacity/latency are recorded; GateHouse M4 remains blocked independently. Verification: same binary payload/service and direct protocol baseline. Dependency: Part A and step 5 calibration; reference protocol conformance, not GateHouse capability.
7. **Reload, native-fault and telemetry panels.** Acceptance: default/tuned reload distributions plus skipped-update counts; native fault kind and stream continuity; per-candidate off/on CPU/RSS/latency and counted output drops. Verification: external sentinel and known fault timelines, server-side completion, replay of native signals. Dependency: steps 5–6; a gRPC panel waits only on that protocol's conformance.
8. **Footprint and stability.** Acceptance: parent cgroup peak with component breakdown, route-scale curves, one-Hz 1,000 reloads and a 60-minute soak per eligible production reference; Go control has a separate no-reload soak. Verification: startup/warm/load/churn/drain phases, streams ended before idle-recovery check, no orphan processes. Dependency: steps 5–7.

**Part B gate:** every T01–T09 category has valid measured output, FAIL, N/A or UNAVAILABLE per candidate/protocol; no pending row is quietly omitted; default/tuned and C1/C100 figures stay separated. Complete the planned run matrix or declare/report an intentionally narrower pilot matrix before discussing comparisons.

### Part C — Report, target calibration and GateHouse follow-up

9. **Analyze reproducibly.** Files: `benchmarks/analyze.py`, `benchmarks/results/<campaign-id>/manifest.json`, `docs/benchmarks/<campaign-id>.md`. Acceptance: raw inputs, manifest and analysis regenerate all metrics/uncertainty; include invalid/losing runs and eligibility. Verification: an independent unit/dimension/counter audit of report samples. Dependency: Part B.
10. **Calibrate hypotheses.** Update product targets/evidence and create a separate measured reference scorecard from the dataset. Acceptance: absolute numeric GateHouse budgets with workload/date/reference provenance; explicitly no achieved GateHouse results. Verification: source-to-cell checks, visual inspection, no target/result mixing. Dependency: step 9.
11. **Run eligible GateHouse profiles against frozen references.** Acceptance: same inputs/protocol proof; Q_all rule; matched reruns if load changes; measured GateHouse cells replace pending only with actual results. Verification: semantic/race tests, raw result and independent benchmark review. Dependency: steps 9–10 and corresponding GateHouse P0/P1 capability gates.

Public comparisons wait for the reproducibility/eligibility gates and authorized publication. Local measured results may guide engineering sooner, with their environmental limits labeled. The next requested execution should begin at Part A, not by guessing competitor scores.
