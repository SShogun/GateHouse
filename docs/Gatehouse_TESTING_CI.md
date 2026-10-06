# Gatehouse — Testing and CI Contract

**Purpose:** Define what Gatehouse must prove automatically and how CI separates fast correctness from expensive failure/performance evidence.

---

# 1. Test philosophy

A gateway can pass ordinary unit tests while still being wrong in the ways that matter: cancellation can leak work, trailers can disappear, retries can amplify outages, dynamic config can race, a slow client can explode memory, and auth-key rotation can stampede an identity provider.

Therefore Gatehouse uses a ladder:

```text
pure/unit
-> property/fuzz
-> protocol integration
-> dependency integration
-> race/leak
-> fault injection
-> end-to-end/UI
-> load/soak
-> security/compatibility
```

Use the cheapest proof that genuinely exercises the failure class. Do not replace deterministic unit tests with containers, and do not replace protocol integration with mocks.

---

# 2. Test taxonomy

## 2.1 Pure unit tests

Target runtime: milliseconds.

Required subjects:

- route match and precedence;
- host normalization rules;
- path match behavior;
- semantic config validation;
- content hash/canonicalization;
- revision ordering rules;
- timeout/deadline calculation;
- retry eligibility;
- retry budget;
- token bucket;
- circuit breaker;
- endpoint selection;
- auth claim mapping;
- forwarding-header trust logic;
- protocol error mapping;
- metrics label sanitization/classification.

Rules:

- fake clock where time drives state;
- injectable RNG where jitter/weighted selection matters;
- no sleeps in deterministic unit tests;
- table tests for semantic matrices;
- every bug fix adds a regression test at the lowest useful layer.

---

## 2.2 Property and fuzz tests

### Mandatory fuzz targets

```text
FuzzRouteMatch
FuzzPathNormalizationOrPreservation
FuzzForwardedHeaderParsing
FuzzConfigDecodeValidate
FuzzProtocolClassifier
```

If a custom parser/framer is later added, it gains a fuzz target before release.

### Core invariants

For arbitrary input:

- no panic;
- no out-of-bounds access;
- deterministic result;
- invalid config never becomes active;
- canonicalization is idempotent;
- route matcher never returns an ID absent from compiled config;
- protocol classifier never trusts malformed headers enough to bypass policy;
- tiny malicious input should not cause absurd allocations.

### CI tiers

PR smoke:

```bash
go test ./internal/router -run=^$ -fuzz=FuzzRouteMatch -fuzztime=20s
```

Nightly:

- run all fuzz targets for longer bounded windows;
- persist newly discovered corpora as reviewed regression inputs;
- never auto-commit opaque corpus failures without minimization/review.

---

# 3. Protocol integration matrix

Use real clients and real upstream servers.

| Area | Required cases |
|---|---|
| HTTP/1.1 | GET/POST, headers, body streaming, trailers where applicable, cancel, timeout |
| HTTP/2 | TLS negotiation, multiplexed concurrent requests, cancel, streaming, connection reuse |
| Connect | unary, server stream, error, cancellation, deadline |
| gRPC | unary, metadata, server stream, error status/trailer, deadline, cancellation |
| proxy headers | trusted/untrusted forwarding chain, Host behavior, hop-by-hop stripping |
| upstream pool | weighted selection, unhealthy endpoint exclusion, all-unhealthy behavior |
| config | publish, NACK, stale, reconnect, atomic request revision |

Do not mark a protocol supported because one happy unary request passed.

---

# 4. Cancellation tests

Cancellation is a mandatory semantic proof.

Harness:

```text
client
  -> Gatehouse
      -> upstream handler blocks and records ctx.Done()
```

Cases:

1. client explicitly cancels;
2. client socket disconnects;
3. route deadline expires;
4. server shutdown cancels after drain deadline;
5. retry backoff is interrupted by cancellation;
6. streaming downstream disconnect cancels upstream stream.

Assertions:

```text
client receives expected terminal state
upstream observes cancellation within bound
active request gauge returns to baseline
gateway goroutine count returns near baseline
no retry occurs after cancellation
```

Avoid strict millisecond timing that flakes under CI; use bounded eventually assertions with generous test-only upper bound.

---

# 5. Streaming/backpressure tests

Required:

- upstream sends first chunk then pauses: client receives first chunk before completion;
- client reads slowly: gateway memory remains bounded;
- upstream sends large response: gateway does not buffer response proportional to total size;
- downstream disconnect mid-stream: upstream canceled/response body closed;
- graceful shutdown with active stream obeys configured drain policy;
- gRPC stream preserves final status/trailers;
- SSE flush cadence reaches client.

Memory evidence may use allocation profiles or process heap sampling in a dedicated integration test, not fragile exact-byte assertions.

---

# 6. Dynamic config tests

## 6.1 Atomicity

- revision N active;
- block an in-flight request after route match;
- publish N+1 with different upstream/policy;
- release request;
- assert old request used N and new request uses N+1.

## 6.2 Last-known-good

- N active and serving;
- publish malformed or semantically invalid N+1;
- N+1 NACKed;
- N remains active;
- request traffic unaffected except status telemetry.

## 6.3 Ordering/integrity

- stale revision ignored;
- duplicate same revision/hash idempotent;
- same revision/different hash fatal protocol error/NACK;
- stream reconnect from current revision converges;
- config resend does not leak resources.

## 6.4 Churn

Publish hundreds or thousands of revisions changing routes/clusters repeatedly.

Measure:

- goroutines before/after quiescence;
- timers/workers;
- heap after GC/quiescence;
- open idle transport behavior where inspectable;
- health-check scheduler count.

The test is about bounded lifecycle, not exact identical heap bytes.

---

# 7. Retry/deadline tests

Matrix dimensions:

```text
method/RPC safety
body replayable yes/no
failure before/after response commitment
retryable status/error yes/no
remaining deadline
retry budget state
attempt count
streaming yes/no
```

Required proofs:

- safe replayable GET retries on configured transient failure;
- unsafe POST does not retry by default;
- explicit safe operation can retry only if body is replayable;
- body above replay limit suppresses retry;
- response header received/committed suppresses retry;
- total deadline limits all attempts/backoff;
- retry budget exhaustion suppresses attempts;
- cancellation stops backoff immediately;
- metrics record suppression reason;
- failure burst does not cause amplification above configured envelope.

Use fake clock in pure state tests and real clock only at transport boundary.

---

# 8. Health and circuit tests

Health:

- initial unknown behavior documented;
- unhealthy threshold;
- healthy threshold;
- probe timeout;
- probe cancellation on config removal;
- jitter not zero/synchronized in production path;
- no endpoint chosen when marked unavailable unless policy explicitly permits fallback.

Circuit:

- closed failure counter/window;
- open rejects quickly;
- cooldown;
- half-open probe cap;
- successful probe closes;
- failed probe reopens;
- config removal destroys state cleanly;
- concurrent requests do not violate state invariants under `-race`.

---

# 9. Authentication/security tests

## JWT/OIDC matrix

- valid signature;
- expired;
- not-before;
- bad issuer;
- bad audience;
- missing required claim;
- wrong role/scope;
- unknown `kid`;
- disallowed algorithm;
- malformed token;
- oversized token/header rejected by server/header limits.

## JWKS

- cache hit no network;
- rotation;
- concurrent unknown-kid single-flight;
- provider timeout;
- provider invalid JSON/key set;
- stale-cache behavior;
- shutdown while refresh in flight.

## TLS/mTLS

- valid server cert path;
- hostname mismatch;
- unknown CA;
- missing client cert;
- invalid client chain;
- valid client identity;
- route authorization against verified certificate principal;
- control↔data mutual-auth rejection/acceptance matrix in secure mode.

## SSRF/destination config

- forbidden scheme;
- malformed authority;
- blocked CIDR if policy enabled;
- JWKS redirect policy;
- upstream URL validation.

## Redaction

Capture logs/traces in tests and assert fixture secrets/tokens are absent.

---

# 10. Rate-limit tests

Local token bucket:

- initial burst capacity;
- refill using fake clock;
- exact boundary;
- concurrent callers;
- independent keys;
- bounded key-state eviction policy if dynamic keys are supported;
- no metric-cardinality coupling to key value.

Distributed mode only if implemented:

- Redis success;
- timeout;
- disconnect;
- script/transaction error;
- fail-open route;
- fail-closed route;
- two data planes share the intended global limit;
- no silent local fallback.

---

# 11. Fault injection

Use Toxiproxy/Testcontainers when socket-level behavior matters.

Scenarios:

```text
latency
bandwidth constraint if useful
connection cut/reset
upstream unavailable
control-plane stream cut
Redis cut
PostgreSQL connectivity loss for control plane
```

For DNS failures, use controlled resolver/test host strategy rather than relying on public DNS.

Each fault test must define:

```text
fault insertion point
expected client response
expected retries
expected breaker/health transition
expected config revision retention
expected telemetry
recovery expectation
```

---

# 12. Race and goroutine-leak testing

PR required:

```bash
go test -race ./... -count=1
```

Dedicated lifecycle tests may use `goleak` or explicit goroutine ownership assertions.

Focus:

- health workers across config churn;
- request cancel + response copy;
- breaker state;
- retry budget/token bucket;
- JWKS refresh single-flight;
- config active-pointer swap;
- instance heartbeat/reconnect;
- shutdown.

Do not treat a stable global goroutine count as a universal assertion because runtime/library goroutines vary. Prefer ownership-specific leak checks and eventual stabilization.

---

# 13. Database integration

Use real PostgreSQL via Testcontainers for:

- revision transaction atomicity;
- optimistic conflict;
- rollback publication;
- audit insert;
- restart persistence;
- concurrent publishers;
- DB outage during validation vs transaction;
- migration forward/backward policy as the schema matures.

Do not mock SQL and claim revision semantics are proven.

---

# 14. Frontend tests

## Unit/component

- route/config form validation;
- revision diff rendering;
- instance staleness state;
- retry/error charts transform bounded API data;
- secret/token fields not persisted.

## Playwright critical flows

1. publish valid route and observe ACK;
2. publish invalid config and see validation error without revision activation;
3. revision diff + rollback;
4. data-plane disconnect/stale state visible;
5. upstream unhealthy state visible;
6. request playground surfaces protocol error correctly;
7. auth failure diagnostics show category, not token.

CI should run with deterministic seeded backend fixtures; avoid dependence on public internet.

---

# 15. Performance and load tests

Use a dedicated load tool (`k6`, `vegeta`, `ghz`, or a small Go harness) based on protocol.

Workloads:

```text
W1 HTTP small unary payload
W2 HTTP moderate payload
W3 gRPC unary
W4 gRPC server streaming
W5 many routes / route lookup
W6 JWT verified hot-cache
W7 local rate limit enabled
W8 upstream 1% transient error with retries
W9 concurrency saturation
W10 rapid config publish while serving load
```

Always compare against direct-upstream baseline when measuring gateway overhead.

Collect:

- throughput;
- p50/p95/p99;
- error rate;
- retry rate;
- CPU;
- heap;
- allocations;
- goroutines;
- connection counts where available.

Hard PR thresholds only for deterministic measurements such as allocations in a pure benchmark if stable. Latency regression alerts belong on controlled runners.

---

# 16. Soak tests

Nightly/periodic soak should combine:

- steady HTTP/gRPC traffic;
- config revision churn;
- endpoint health transitions;
- JWT validation;
- occasional transient upstream failure;
- telemetry export.

Acceptance after warmup:

- heap does not trend without bound;
- goroutines stabilize;
- config revision convergence remains correct;
- no accumulation of stale health workers/transports;
- error rate matches injected fault envelope.

Record seed/config so failures are reproducible.

---

# 17. Compatibility tests

## Protobuf

Required on PR:

```bash
buf lint
buf breaking --against '.git#branch=main'
buf generate
git diff --exit-code -- gen/
```

Adapt the exact baseline syntax to repository layout.

## Mixed binary versions

Once releases exist, keep test fixtures/images for prior supported version(s) and verify control/data-plane compatibility matrix.

No compatibility claim without automation.

---

# 18. Security CI

Required baseline:

```bash
govulncheck ./...
```

Recommended repository protections:

- dependency review for PRs where hosting supports it;
- secret scanning;
- minimal workflow permissions;
- pinned/approved third-party actions policy;
- container image vulnerability scan on release/regular schedule;
- SBOM generation on release;
- signed/provenance-capable release artifacts using OIDC where practical.

Static security linters may be added selectively, but noisy tools must not become checkbox gates that everyone ignores.

---

# 19. CI workflow topology

Recommended GitHub Actions layout:

```text
pull_request / push
|
+-- contracts
|    +-- buf lint
|    +-- buf breaking against main
|    +-- buf generate + git diff
|
+-- static
|    +-- gofmt check
|    +-- go vet ./...
|    +-- staticcheck ./...
|    +-- web lint
|    +-- web typecheck
|
+-- unit
|    +-- go test ./... -count=1 -shuffle=on
|    +-- web unit tests
|    +-- short fuzz smoke targets
|
+-- race
|    +-- go test -race ./... -count=1
|
+-- integration
|    +-- PostgreSQL container
|    +-- protocol suite
|    +-- auth/TLS fixtures
|    +-- optional Redis only if feature exists
|
+-- e2e
|    +-- build control/data/web
|    +-- boot deterministic local stack
|    +-- Playwright critical flows
|
+-- security
     +-- govulncheck
     +-- repository dependency checks as supported
```

Run independent jobs in parallel. Do not put race, E2E, lint, and generation behind one serial 30-minute job.

---

# 20. Nightly / scheduled CI

```text
nightly-fuzz
- longer fuzz windows
- upload minimized failures/artifacts

nightly-fault
- Toxiproxy transport faults
- control stream interruptions
- DB/Redis dependency failures

nightly-soak
- 30-60+ minute sustained workload
- config churn
- memory/goroutine trend report

nightly-perf
- controlled runner preferred
- direct baseline + Gatehouse
- store benchmark JSON/profiles
- compare against reviewed baseline
```

A nightly failure opens an actionable artifact/report; do not silently rerun until green and erase evidence.

---

# 21. Release pipeline

Release runs only from a tested commit/tag.

```text
verify required CI state
-> build Go binaries with version/commit metadata
-> build web assets
-> build minimal container images
-> smoke boot images
-> vulnerability scan
-> generate SBOM
-> sign/attest if supported
-> publish artifacts
-> run post-publish smoke pull/run
```

Do not run arbitrary release scripts with broad write permissions on untrusted PR code.

---

# 22. Makefile / task contract

Use stable top-level commands so local and CI behavior match.

Recommended targets:

```text
make fmt
make lint
make contracts
make test
make race
make fuzz-smoke
make integration
make e2e
make security
make benchmark
make verify        # required local pre-PR set
make demo-up
make demo-down
```

CI should call these targets (or equivalent scripts) rather than duplicating complex command logic in YAML.

---

# 23. Required PR checks

Branch protection should require, at minimum:

```text
contracts
static
unit
race
integration
e2e
security
```

If E2E runtime becomes excessive, keep one deterministic smoke flow required and move extended Playwright coverage to a parallel non-blocking/nightly job only after data shows the split is needed. Do not weaken coverage preemptively.

---

# 24. Flaky-test policy

A flaky gateway test is a correctness problem because production failures are also timing-sensitive.

Policy:

1. record failing seed/logs/trace/artifacts;
2. reproduce locally or in an isolated CI rerun;
3. identify nondeterministic dependency/time assumption;
4. use fake clock/synchronization hooks where appropriate;
5. quarantine only with an issue, owner, expiry, and non-blocking visibility;
6. never add blind `sleep(2s)` until green as the final fix;
7. never add automatic N retries to turn a flaky required test green.

---

# 25. CI artifacts on failure

Upload only non-secret artifacts:

- Go test JSON/logs;
- race reports;
- minimized fuzz corpus input;
- Playwright trace/screenshots;
- service logs with redaction;
- OTel trace export from test backend;
- goroutine/heap profile for soak leak;
- benchmark JSON;
- config revision/status dump.

Do not upload JWTs, cookies, private keys, database passwords, or raw production-like secrets.

---

# 26. Coverage policy

Do not use one repository-wide percentage as the quality target.

Require coverage on critical state/decision code by review and test inventory:

```text
router precedence
config validation/apply
retry classifier/budget
breaker
rate limiter
auth claim mapping
revision ordering
protocol error mapping
```

Integration/e2e correctness matters more than inflating line coverage on generated/bootstrap code.

---

# 27. CI gate evidence

Every milestone report should retain:

```text
commit SHA
Go/Node versions
required checks and result
fuzz seeds/failures if any
fault scenarios run
race result
security result
benchmark environment/result if relevant
known quarantined tests (ideally none)
```

A green badge without the ability to reproduce commands locally is insufficient.
