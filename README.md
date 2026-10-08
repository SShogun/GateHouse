<div align="center">

# GateHouse

### Make gateway correctness visible.

A production-minded Layer 7 gateway in Go, designed around protocol correctness, safe configuration rollouts, and predictable failure behavior.

**Go:** 1.26.8 · **Contracts:** Protobuf · **CI:** [GitHub Actions](https://github.com/SShogun/GateHouse/actions/workflows/ci.yml)

</div>

> **Project state: M0 — repository, contracts, and CI foundation.** The current code has process lifecycle scaffolding and generated Protobuf bindings. It does **not** yet accept network traffic, route requests, store configuration, or publish runtime configuration. This README separates the intended system from what runs today.

[What exists](#what-works-today) · [Run checks](#try-the-current-foundation) · [Target architecture](#target-architecture) · [Roadmap](#roadmap) · [Docs](#repository-map)

---

## The idea

Gateways are easy to describe as boxes and hard to get right at the edges. A client disconnect must stop upstream work. A config update must not expose a half-built state. A streamed response must remain streamed. Retries must not turn an outage into a traffic multiplier.

GateHouse is being engineered in small, reviewable increments, with executable tests for the guarantees that matter under real traffic instead of relying on architecture diagrams alone.

Its eventual boundary is deliberately split:

- **Control plane:** validates configuration, stores immutable revisions, publishes updates, and tracks data-plane status.
- **Data plane:** serves requests from a compiled local configuration snapshot; normal request handling does not depend on PostgreSQL or a synchronous control-plane call.
- **Operator console:** a future interface for configuration and operational state.

## What works today

The current milestone is intentionally small. The repository provides:

- separate Go command entry points for `gatehouse-control` and `gatehouse-data`;
- SIGINT/SIGTERM handling, root cancellation, and bounded shutdown scaffolding;
- a `gatehouse.v1` Protobuf workspace with committed Go and TypeScript generated output;
- Buf linting, compatibility checks, and generated-code drift checks;
- an executable compatibility fixture that proves Buf rejects a removed field and accepts a compatible addition;
- separate CI jobs for contracts, static analysis, unit tests, race tests, and vulnerability scanning.

The command binaries currently wait for a shutdown signal. They do not open listeners or implement gateway behavior yet. The Protobuf package is a contract workspace, not a completed application API.

## Target architecture

This is the destination described by the architecture docs—not a claim that these components are already implemented.

```mermaid
flowchart LR
    Operator[Operator] --> AdminAPI[Control-plane API]
    AdminAPI --> Validate[Validate configuration]
    Validate --> Revision[(Immutable revision<br/>PostgreSQL)]
    Revision --> Publish[Versioned config stream]
    Publish --> Compile[Data plane<br/>validate and compile]
    Compile --> Ack[ACK / NACK]
    Compile --> Snapshot[Atomic active snapshot]

    Client[Client] --> Listener[HTTP-family listener]
    Listener --> Route[Route and policy]
    Snapshot -. supplies config .-> Route
    Route --> Upstream[Upstream service]
    Upstream --> Response[Response or stream]
    Route -. telemetry .-> Ops[Operational evidence]
    Ack -. status .-> Ops
```

The critical separation: the control plane owns durable configuration and publication; the data plane owns the request path and continues serving from its active snapshot when the control plane or database is unavailable.

## Try the current foundation

### Requirements

- Git
- Go 1.26.8 (the `go.mod` toolchain directive is authoritative)
- Buf CLI 1.73.0
- `make`
- Network access on the first verification run to download pinned analysis tools and Buf generators

### Clone and verify

```bash
git clone https://github.com/SShogun/GateHouse.git
cd GateHouse
make verify
```

`make verify` runs formatting checks, `go vet`, Staticcheck, Buf lint and breaking checks, the negative compatibility fixture, generated-code drift detection, module verification, unit tests, race tests, and `govulncheck`.

### Run the lifecycle scaffold

```bash
go run ./cmd/gatehouse-data
```

The process waits for SIGINT (`Ctrl+C`) or SIGTERM, then follows the lifecycle shutdown path. It does not bind a port or proxy requests yet. The control-plane scaffold can be started with `go run ./cmd/gatehouse-control` and behaves the same way at this milestone.

## The proof bar

GateHouse treats correctness as observable behavior. The current test foundation proves lifecycle ordering and cancellation, and the contract fixture runs Buf against both a breaking and a compatible revision. As request-handling milestones arrive, the proof suite grows with the behavior: real HTTP clients and upstreams, cancellation propagation, streaming, race/leak checks, fault injection, and bounded-resource evidence.

No protocol is considered supported because a happy-path demo happened to work. The roadmap requires real HTTP/1.1 and HTTP/2 behavior, Connect/gRPC status and trailer semantics, and streaming/cancellation tests before those claims are made.

## Roadmap

The sequence is dependency-ordered. A later milestone does not begin just because its code exists; it begins when the preceding gate has evidence.

| Milestone | Focus |
| --- | --- |
| M0 | Repository bootstrap, Protobuf contracts, lifecycle, and CI floor |
| M1 | Static HTTP proxy and cancellation |
| M2 | Deterministic routing, clusters, load balancing, and health |
| M3 | Control plane, revisions, and atomic hot reload |
| M4 | HTTP/2, Connect, gRPC, trailers, and streaming |
| M5 | Deadlines, retries, budgets, admission, and circuit breaking |
| M6 | Authentication, rate limiting, TLS, and mTLS |
| M7 | Observability and operator UI |
| M8 | Failure campaign, load, performance, and resource hardening |
| M9 | Compatibility, release, and reproducibility |

See the [execution plan](docs/Gatehouse_PLAN.md) for each milestone's RED/GREEN gates and exact scope.

## Repository map

```text
api/gatehouse/v1/          Protobuf API namespace
cmd/gatehouse-control/     Control-plane process entry point
cmd/gatehouse-data/        Data-plane process entry point
gen/go/                    Committed Go Protobuf bindings
gen/ts/                    Committed TypeScript Protobuf bindings
internal/lifecycle/        Signal, root-context, and shutdown coordination
scripts/                   Contract compatibility proof
tests/fixtures/proto-compat/ Baseline, breaking, and compatible contracts
docs/                      Architecture, decisions, roadmap, testing/CI
.github/workflows/ci.yml   Separate, visible M0 CI gates
Makefile                   Local verification commands
```

Start with the doc that answers your question:

| If you want to understand… | Read… |
| --- | --- |
| System boundaries and invariants | [Architecture](docs/Gatehouse_ARCHITECTURE.md) |
| Why key technical choices were made | [Decisions](docs/Gatehouse_DECISIONS.md) |
| What comes next and what must pass | [Execution plan](docs/Gatehouse_PLAN.md) |
| How behavior is tested and CI is structured | [Testing and CI](docs/Gatehouse_TESTING_CI.md) |

## Design priorities

GateHouse is being built around a request path that can keep working independently of the control plane, configuration changes that can be validated before they become active, and protocol behavior that is verified with tests. Those constraints guide the implementation as the gateway grows from its current foundation.

## Contributing

Changes should be narrow, tested, and consistent with the active milestone. Before opening a pull request, run:

```bash
make verify
```

For behavior changes, add a test that demonstrates the missing behavior before the fix. Generated bindings are committed; if a Protobuf source changes, regenerate with `buf generate` and include the resulting output.
