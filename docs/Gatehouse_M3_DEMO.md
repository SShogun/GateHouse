# Demonstrate M3 from a clean checkout

This is a local development demonstration of configuration publication, activation, history, conflicts, rollback and recovery. It is not a production deployment or performance benchmark. No graphical UI is implemented yet. All listeners use literal loopback IPs.

Use a normal clone, not a Git worktree, and start each repository command from its root. Requirements: Go 1.26.9, Python 3, curl, and PostgreSQL server tools (`initdb`, `pg_ctl`, `createdb`). The Go binding files are committed; Buf is not required to run this demo. The first build and grpcurl install need network access. Ports 18080–18082, 19090 and 15433 must be free; stop only your own older demo if they are occupied.

```sh
git clone https://github.com/SShogun/GateHouse.git
cd GateHouse
```

The example files must be present in the checked-out revision. Unpublished documentation/examples are not available from the remote until committed and pushed.

## 1. Control plane terminal: create a disposable database and start management

Run as your ordinary user, not root. This creates a separate database cluster in a new temporary directory; it does not use or modify an existing PostgreSQL cluster. Trust authentication is confined to this loopback development cluster; do not reuse this setup for a public deployment.

```sh
gh_demo_dir=$(mktemp -d /tmp/gatehouse-m3.XXXXXX)
mkdir -m 700 "$gh_demo_dir/socket"
mkdir "$gh_demo_dir/bin"
initdb -D "$gh_demo_dir/postgres" -A trust -U gatehouse_demo -E UTF8 --no-locale
pg_ctl -D "$gh_demo_dir/postgres" -l "$gh_demo_dir/postgres.log" \
  -o "-h 127.0.0.1 -p 15433 -k $gh_demo_dir/socket" -w -t 30 start
createdb -h "$gh_demo_dir/socket" -p 15433 -U gatehouse_demo gatehouse_demo
GOTOOLCHAIN=go1.26.9 go build -o "$gh_demo_dir/bin/" ./cmd/gatehouse-control ./cmd/gatehouse-data

GATEHOUSE_CONTROL_DEV_MODE=loopback \
GATEHOUSE_CONTROL_LISTEN_ADDR=127.0.0.1:19090 \
GATEHOUSE_DATABASE_URL='postgres://gatehouse_demo@127.0.0.1:15433/gatehouse_demo?sslmode=disable' \
"$gh_demo_dir/bin/gatehouse-control"
```

Keep the terminal open: it retains `gh_demo_dir` for restart and cleanup. If an earlier command fails, resolve that error before starting the control plane. Wait for its `control plane listening` log.

## 2. Upstreams terminal: start two distinguishable services

```sh
gh_upstream_dir=$(mktemp -d /tmp/gatehouse-upstreams.XXXXXX)
mkdir "$gh_upstream_dir/blue" "$gh_upstream_dir/green"
printf 'blue\n' > "$gh_upstream_dir/blue/index.html"
printf 'green\n' > "$gh_upstream_dir/green/index.html"
python3 -m http.server 18081 --bind 127.0.0.1 --directory "$gh_upstream_dir/blue" &
gh_blue_pid=$!
python3 -m http.server 18082 --bind 127.0.0.1 --directory "$gh_upstream_dir/green"
```

Blue runs in the background with its exact PID saved; green stays in the foreground. Keep this terminal open. These sample servers serve files; they are not the benchmark fixture.

## 3. Gateway terminal: start the dynamic data plane

From the repository root:

```sh
GATEHOUSE_LISTEN_ADDR=127.0.0.1:18080 \
GATEHOUSE_CONTROL_ENDPOINT=127.0.0.1:19090 \
GATEHOUSE_INSTANCE_ID=demo-gateway \
GOTOOLCHAIN=go1.26.9 go run ./cmd/gatehouse-data
```

Keep it running. Until publication, `http://127.0.0.1:18080/_gatehouse/readyz` returns 503. The instance ID is stable; the executable creates a new boot ID each time it starts.

## 4. Operator terminal: install the API client and define inspection helpers

From the repository root:

```sh
GOTOOLCHAIN=go1.26.9 go install github.com/fullstorydev/grpcurl/cmd/grpcurl@v1.9.3

ghadmin() {
  "$(go env GOPATH)/bin/grpcurl" -plaintext -max-time 5 \
    -import-path api -proto gatehouse/v1/control.proto -d "$2" \
    127.0.0.1:19090 "gatehouse.v1.AdminService/$1"
}

ghwait() {
  python3 - "$1" "$2" <<'PY'
import json, sys, time, urllib.request
field, expected = sys.argv[1], json.loads(sys.argv[2])
deadline = time.monotonic() + 20
last = None
while time.monotonic() < deadline:
    try:
        with urllib.request.urlopen('http://127.0.0.1:18080/_gatehouse/status', timeout=2) as response:
            last = json.load(response)
        if last.get(field) == expected:
            print(json.dumps(last, indent=2))
            break
    except OSError:
        pass
    time.sleep(.1)
else:
    raise SystemExit(f'Timed out waiting for {field}={expected}; last status: {last}')
PY
}
```

`grpcurl` uses the repository's Protobuf files because the server does not register reflection. `ghwait` polls the observable outcome with a deadline; it does not assume delivery is instantaneous.

## 5. Publish blue, then switch to green

```sh
curl --noproxy '*' -i --max-time 3 http://127.0.0.1:18080/_gatehouse/readyz
ghadmin PublishConfig @ < examples/m3/publish-blue.json
ghwait active_revision 1
curl --noproxy '*' -sS --max-time 3 http://127.0.0.1:18080/index.html
ghadmin ListInstances '{}'

ghadmin PublishConfig @ < examples/m3/publish-green.json
ghwait active_revision 2
curl --noproxy '*' -sS --max-time 3 http://127.0.0.1:18080/index.html
ghadmin ListRevisions '{}'
ghadmin DiffRevisions '{"fromRevision":"1","toRevision":"2"}'
```

Expected: initial readiness 503, then `blue` at revision 1, then `green` at revision 2 without restarting the gateway. Instance status converges to active revision 2 after its ACK is persisted. The diff lists `demo` as a changed cluster. History includes both publications with actors/reasons/hashes. Publication success and data-plane ACK are separate observations.

## 6. Reject invalid configuration and a stale editor

```sh
ghadmin PublishConfig '{"expectedParentRevision":"2","actor":"demo-operator","reason":"Invalid schema","config":{"schemaVersion":99}}'
ghadmin PublishConfig @ < examples/m3/publish-blue.json
ghadmin GetActiveConfig '{}'
curl --noproxy '*' -sS --max-time 3 http://127.0.0.1:18080/index.html
```

The first mutation must fail with `InvalidArgument`. The second must fail with `Aborted`: its expected parent is still zero, while revision 2 is active. These commands intentionally return nonzero; do not treat either as a setup failure. The active configuration remains revision 2 and requests still return `green`.

## 7. Roll back without erasing history

```sh
ghadmin Rollback '{"expectedParentRevision":"2","sourceRevision":"1","actor":"demo-operator","reason":"Restore blue"}'
ghwait active_revision 3
curl --noproxy '*' -sS --max-time 3 http://127.0.0.1:18080/index.html
ghadmin ListRevisions '{}'
```

Expected: requests return `blue`; revision 3 has operation `rollback` and source revision 1. Earlier revisions remain in history. These revision numbers assume a fresh demo database and that each successful publication was run once; repeating a publication requires inspecting and explicitly choosing the current parent.

## 8. Stop management while traffic continues, then recover

Press Ctrl+C in **Control plane**, leaving Gateway and Upstreams running. A persistent config stream can keep graceful shutdown open until the 10-second bound; the process then stops and may report the shutdown deadline. This is the current shutdown behavior, not a promise of a zero exit status with a live stream.

In **Operator**:

```sh
ghwait connected false
curl --noproxy '*' -sS --max-time 3 http://127.0.0.1:18080/index.html
curl --noproxy '*' -i --max-time 3 http://127.0.0.1:18080/_gatehouse/readyz
```

Expected: connected false, stale true, active revision 3, continued `blue` responses and readiness 200. This proves retention in the running data plane; it does not prove cold data-plane startup during an outage.

In the same **Control plane** terminal, restart using its existing `gh_demo_dir`:

```sh
GATEHOUSE_CONTROL_DEV_MODE=loopback \
GATEHOUSE_CONTROL_LISTEN_ADDR=127.0.0.1:19090 \
GATEHOUSE_DATABASE_URL='postgres://gatehouse_demo@127.0.0.1:15433/gatehouse_demo?sslmode=disable' \
"$gh_demo_dir/bin/gatehouse-control"
```

In **Operator**:

```sh
ghwait connected true
ghadmin GetActiveConfig '{}'
ghadmin ListInstances '{}'
```

The durable active revision remains 3; the existing data plane reconnects. Inspect instance status again if its registration or ACK is still finishing.

## 9. Clean up only this demo

1. Press Ctrl+C in **Gateway**, then in **Control plane**. This order lets the management stream close before control shutdown.
2. Press Ctrl+C in **Upstreams** to stop green, then stop the exact background blue process:

   ```sh
   kill -TERM "$gh_blue_pid"
   wait "$gh_blue_pid"
   ```

3. In **Control plane**, stop this newly created PostgreSQL cluster:

   ```sh
   pg_ctl -D "$gh_demo_dir/postgres" -m fast -w -t 30 stop
   ```

4. Check that the demo listeners are gone:

   ```sh
   ss -ltn '( sport = :18080 or sport = :18081 or sport = :18082 or sport = :19090 or sport = :15433 )'
   ```

Expect a header with no listener rows. Temporary data/log directories are retained for inspection; their paths are in `gh_demo_dir` and `gh_upstream_dir`. Do not delete an existing database or kill unrelated processes for cleanup.

The automated real-process test additionally covers an in-flight request through restart and a PostgreSQL outage. Fault injection, race/fuzz and resource-churn evidence are in [Testing and CI](Gatehouse_TESTING_CI.md#m3-part-3-local-verification-evidence). A short interactive demo does not replace those acceptance checks.
