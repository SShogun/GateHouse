# PR #5 Sourcery review follow-through

This record distinguishes the bot's findings from their verified dispositions.
The review started from M1 commit `365cc46` and the dependent M2 commit
`4e59787`. Follow-up changes are local until the user approves commit and push;
the existing GitHub threads are not manually resolved by this record.

## Findings and corrections

| Finding | Disposition |
| --- | --- |
| [Externally reachable unauthenticated listener](https://github.com/SShogun/GateHouse/pull/5#discussion_r4237918690) | Confirmed configuration exposure. The development command validates a literal loopback IP and numeric port before constructing its runtime. Wildcard addresses, non-loopback IPs, and hostnames are rejected. The reusable server remains configurable for later authenticated deployment. |
| [Unbounded waits and concurrent work](https://github.com/SShogun/GateHouse/pull/5#discussion_r4237918693) | Confirmed missing static safeguards. Listener admission, individual request-body read waits, upstream response-header waits, and response-header size are bounded. Rejected and unread-body cleanup must also be bounded because net/http can drain the original body outside the handler. |
| [Missing Hijacker forwarding](https://github.com/SShogun/GateHouse/pull/5#discussion_r4237918697) | False positive for the pinned Go 1.26.9 runtime. ReverseProxy calls ResponseController.Hijack, which follows statusRecorder.Unwrap. A real bidirectional HTTP 101 characterization test verifies this behavior without another forwarding wrapper. |
| [Failure outcomes logged as success](https://github.com/SShogun/GateHouse/pull/5#discussion_r4237918701) | Already fixed in `365cc46` and resolved by Sourcery. The correction and its existing regressions are preserved. |
| [Connection-header coverage gap](https://github.com/SShogun/GateHouse/pull/5#discussion_r4237918705) | Confirmed test gap. The upstream request and raw client response are checked for Connection and its nominated headers. Controlled leak mutations demonstrate that the assertions reject regressions. |
| [Shutdown timing assumption](https://github.com/SShogun/GateHouse/pull/5#discussion_r4237918707) | Confirmed test gap. A bounded listener-closure barrier replaces the 25 ms assumption; a blocked-request deadline test rejects a server that closes immediately instead of draining. |

## Static defaults and scope

| Limit | Default |
| --- | --- |
| Concurrent HTTP handlers per listener | 128; excess work receives HTTP 503 without a queue |
| Individual request-body read wait | 30 seconds |
| Upstream response-header wait after writing the request | 30 seconds |
| Upstream response-header size | 1 MiB |

Zero values use these defaults; negative values for the new resource settings
are rejected. Request-body waiting limits are not a total upload deadline.
Response bodies retain progressive streaming without a write or client-wide
timeout. These static controls do not promise a total request deadline or bound
all upstream backpressure.

The listener admission safeguard is added earlier than the policy milestone as
part of the authorized review correction. M5 still owns total/per-attempt
deadlines, route admission, retries, budgets, and circuit breakers. M6 still owns
authentication and TLS/mTLS. None of those milestone gates is marked complete
by this correction, and these development commands do not accept public binds.

## Verification

Final integrated `GOCACHE=/tmp/gatehouse-gocache make verify` passed with exit
code 0 on both corrected trees using Go 1.26.9, Staticcheck v0.7.0 and
govulncheck v1.8.0. The gate includes formatting, vet, Staticcheck, Buf lint and
breaking/fixture checks, clean generation, module verification, shuffled unit
tests, shuffled race tests, and vulnerability scanning. No vulnerabilities
were found. M1 was verified in the primary checkout; M2 was verified in the
source-identical normal clone described below. Their baseline commits remain
`365cc46` and `4e59787`; corrections are uncommitted.

Focused protocol tests pass with Go 1.26.9. Controlled request-header,
response-header, and no-drain mutations fail at the intended assertions. These
are mutation/coverage proofs rather than claims that the existing proxy leaked
headers or lacked graceful shutdown.

The pinned source proof for upgrades is in Go 1.26.9's
`net/http/httputil/reverseproxy.go:853-854` and
`net/http/responsecontroller.go:66-75`.

The independent Luna review identified a new regression introduced by the
request-body limiter: generic body-bearing handlers lost direct Hijacker
support, and deferred cleanup could change a connection's deadline after
ownership transferred to the handler. The limiter now preserves the interface
and tracks ownership. Real retained-connection tests cover direct and
ResponseController hijacks. The follow-up also covers repeated hijacking and
restoring the body-read deadline when an attempted transfer fails.
The reviewer confirmed those bounded corrections without repeating the test
runs and reported no remaining blocker within the reviewed ownership cases.

## Process deviations

The initial static-safety API/helper and behavior were implemented before the
first focused RED. Subsequent body-cleanup and overload failures had real
assertion-level RED/GREEN evidence, but they do not retroactively establish
test-first ordering for every initial change. The concurrent-read case was
characterization coverage rather than a failing RED. The overload RED record
was copied from observed tool output; it is not a directly captured shell log.

One protocol test command ran while another worker's shared package scaffold
was incomplete and failed to compile. That was a coordination failure, not a
behavioral RED. Protocol characterization and mutations then ran in an
isolated copy of the exact M1 baseline. The external engineering lessons record
the required compile barrier and per-behavior RED checkpoint.

An initial full M1 gate passed before the independent review's hijack correction;
it is not evidence for the final source. Subsequent full checks stopped at
Staticcheck because two new tests deferred connection cleanup before checking
the dial error. Those warnings were corrected before final verification.

The pinned Go toolchain's VCS discovery failed in the M2 worktree in this
environment: it did not recognize the worktree's `.git` file and attempted
`git status` in the sandbox's synthetic `/tmp/.git`. M2 verification therefore
uses a normal local clone at the same baseline, with identical working source
and the same `main` reference. VCS stamping and verification gates remain
enabled. This environment failure is not a behavioral RED.

## Local evidence index

Raw logs are retained outside the repository under `/tmp`; they are local
session evidence, not portable CI artifacts.

| Evidence | Log |
| --- | --- |
| Static limit focused GREEN | `/tmp/gatehouse-pr5-focused-green.log` |
| Protocol characterization GREEN | `/tmp/gatehouse-pr5-protocol-green.log` |
| Request/response header mutations | `/tmp/gatehouse-pr5-protocol-request-mutant.log`, `/tmp/gatehouse-pr5-protocol-header-mutant.log` |
| Shutdown GREEN and no-drain mutation | `/tmp/gatehouse-pr5-protocol-shutdown-green.log`, `/tmp/gatehouse-pr5-protocol-shutdown-mutant.log` |
| M2 resource config RED/GREEN | `/tmp/gatehouse-pr5-m2-config-red.log`, `/tmp/gatehouse-pr5-m2-config-green.log` |
| Initial connection ownership RED/GREEN and race | `/tmp/gatehouse-pr5-hijack-red.log`, `/tmp/gatehouse-pr5-hijack-green.log`, `/tmp/gatehouse-pr5-hijack-race.log` |
| Failed/repeated hijack RED/GREEN and race | `/tmp/gatehouse-pr5-hijack-edge-red.log`, `/tmp/gatehouse-pr5-hijack-edge-green.log`, `/tmp/gatehouse-pr5-hijack-edge-race.log` |
| Final M1 full gate | `/tmp/gatehouse-pr5-m1-verify.log` |
| Final M2 full gate | `/tmp/gatehouse-pr5-m2-verify.log` |

The existing remote heads still have successful CI: [M1 `365cc46`](https://github.com/SShogun/GateHouse/actions/runs/38063221638)
and [M2 `4e59787`](https://github.com/SShogun/GateHouse/actions/runs/38063661407).
Those runs cover the previous commits, not these uncommitted corrections.
Merge readiness still requires approved delivery and CI for the new heads.

The failed/repeated-hijack RED predates the final test's strengthened barrier
on read-deadline installation. The revised test passes normally and with the
race detector; the earlier transition was not replayed after that test-only
revision. This distinction is preserved instead of claiming a new historical
RED.
