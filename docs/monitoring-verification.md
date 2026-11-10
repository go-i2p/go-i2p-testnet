# Dashboard verification

Environment: Linux x64, Docker 24.0.7, Go 1.26.8. All commands below were
run in this repo; outputs are the actual results.

## 1. Static checks

```
$ go build ./...          # clean
$ go vet ./...            # clean
$ gofmt -l .              # no output
$ go test ./...
ok      go-i2p-testnet/lib/monitor            0.010s
ok      go-i2p-testnet/lib/monitor/dashboard  0.015s
ok      go-i2p-testnet/lib/monitor/i2pcontrol 0.418s
ok      go-i2p-testnet/lib/monitor/web        6.254s
$ go test -race ./lib/monitor/...             # all ok, no races
```

Baseline before any change: `go build`, `go vet`, `go test ./...` were
already green (no test files existed), and stayed green.

Unit coverage:
- **Client** (`lib/monitor/i2pcontrol`): authenticate + RouterInfo happy
  path; wrong password (spec error *and* i2pd's empty-result quirk);
  token caching + re-authentication on expired token (then no re-auth on
  the next poll); malformed JSON; per-router timeout (ErrTimeout); HTTP
  503; string-vs-number property encodings; absent properties → nil;
  error-message redaction (no token/password leaks); closed client.
- **Pinning** (`pinning_test.go`): pinned cert matches → accepted;
  different cert → rejected with "does not match the pinned one"; no pin
  against a self-signed server → normal verification fails closed;
  `WithInsecureTLS` works only when explicitly set.
- **Poller** (`lib/monitor/web`): mixed up/down/slow routers; slow router
  does not block `DispatchNow` or other routers; down after router dies
  (with last error); stale sweep marks old samples amber and leaves fresh
  ones up; dynamic add/remove of routers; bad pinned cert → down with
  setup error; history capped at 60.
- **Server**: `/healthz` 200; index renders totals/table/refresh tag and
  red class for down; **escaping** — hostile router names, versions,
  statuses and error strings containing `<script>` never appear raw in
  index or detail HTML; `/router/{name}` detail + 404 for unknown;
  `/api/status` JSON shape (top-level, totals, router, and sample keys)
  and no secret fields; unknown paths 404; `Serve` shuts down cleanly on
  context cancel.
- **Dashboard wiring**: password resolution order (env > file > generated
  0600 file), default `itoopie` refused without opt-in and accepted with
  it; per-implementation target URLs (https+pinned for i2pd, http for
  go-i2p); IP-less routers skipped.
- **Log scrubbing**: registered secrets are replaced with `[REDACTED]` in
  captured lines; clean lines untouched.

## 2. Property-name confirmation against live routers

Confirmed against real routers (not docs alone) — see
`docs/monitoring-discovery.md` for the full table. Highlights from the
live 53-router testnet:

- i2pd 2.56.0 (Alpine 3.22 package, as used by `i2pd-node`):
  `i2p.router.version`, `.uptime`, `.status` (numeric-as-string),
  `.net.status`, `.net.bw.{inbound,outbound}.{1s,15s}`,
  `.netdb.{known,active}peers` all present; `.net.tunnels.pools` and
  `.netdb.isreseeding` are *not* served by i2pd and are tolerated as
  missing (rendered `—`).
- go-i2p master (`0.1.0-go`): same core set plus
  `.net.tunnels.inbound/outbound/participating` and
  `.netdb.{fast,highcapacity}peers`; enforces tokens (auth-error re-auth
  path exercised).

## 3. End-to-end run (small, final)

```
$ make build
$ ./bin/go-i2p-testnet            # dashboard auto-starts on "start"
go-i2p-testnet» start
Dashboard: http://127.0.0.1:8765/ (I2PControl password from
GO_I2P_TESTNET_I2PCONTROL_PASSWORD or logs/.i2pcontrol-password)
go-i2p-testnet» add i2pd_router
go-i2p-testnet» add i2pd_router
go-i2p-testnet» add goi2p_router
go-i2p-testnet» sync
```

- After `add`: both i2pd routers `up` (green) within one 5 s cycle over
  pinned HTTPS; go-i2p `down` with a connection error — **expected** per
  the README (go-i2p crash-loops until the first `sync` publishes
  RouterInfos; the retry loop keeps the container alive).
- After `sync`: all routers `up`; peer counts became non-zero
  (`known_peers` 1→…), proving live data flows through the dashboard.

## 4. End-to-end run (scale, 53 routers)

A 42-router i2pd + 1 go-i2p testnet was run to push go-i2p past its
hardcoded 40-peer bootstrap floor (upstream `emergencyReseedFloor = 40`):

```
totals: {'routers': 53, 'up': 53, 'down': 0, 'stale': 0, 'pending': 0}
router-goi2p-4  up  ver=0.1.0-go  known_peers=47
router-i2pd-1   up  ver=2.56.0    known=50 active=18
```

- `/` stayed responsive during the entire `sync` (which sequentially
  restarts every i2pd router): restarted routers flipped `down` (red,
  "request timed out") and back `up` within 1–2 cycles — the required
  red/recovery behavior observed organically at scale, then re-tested
  deliberately (below).
- Polling 53 routers with 5 s timeouts never serialized: dashboard
  refresh cadence held at 5 s throughout (`/api/status`
  `generated_at` deltas).

## 5. Stop / recover test (deliberate)

```
$ docker stop router-i2pd-2
t+7s:  down | i2pcontrol: request timed out: router router-i2pd-2
t+14s: down | ...
$ docker start router-i2pd-2
after ~12s: up | ver 2.56.0
```

Red within one poll interval (≤ ~10 s, the requirement is two intervals);
recovery on the next successful poll. The other routers stayed `up` the
whole time (one down router does not affect the rest).

## 6. Security verification (live)

- **Loopback bind**: dashboard listens on `127.0.0.1:8765` by default
  (`-dashboard-addr` to change).
- **No secret leakage**: `grep -c "$PASSWORD"` over the CLI log,
  `logs/*.log`, dashboard HTML and `/api/status` → 0 matches with the
  current binary. Found and fixed one live leak during e2e: i2pd logs the
  I2PControl password at debug level on authenticate, and router docker
  logs are captured verbatim — the log manager now scrubs the session
  password from every captured line before it reaches files or live
  `logs` subscribers (commit "monitor: scrub the I2PControl password…").
  Pre-fix log files from earlier runs still contain one such line each;
  new runs are clean.
- **TLS pinned, never silently disabled**: i2pd's generated
  `i2pcontrol.crt.pem` is read from the router volume via the Docker API
  and byte-pinned; mismatch → hard failure (unit-tested). `-insecure-tls`
  is the only bypass and prints a warning at startup.
- **Default password refused**: `GO_I2P_TESTNET_I2PCONTROL_PASSWORD=itoopie`
  → CLI exits with an error unless
  `TESTNET_ALLOW_DEFAULT_I2PCONTROL_PASSWORD=1` (unit-tested).
- **Generated password**: with no env/file, a 48-hex-digit password is
  written to `logs/.i2pcontrol-password` (mode `-rw-------`, gitignored).

## 7. Standalone dashboard mode

```
$ make dashboard            # = ./bin/go-i2p-testnet -dashboard
Dashboard: http://127.0.0.1:8765/
$ curl -s http://127.0.0.1:8765/healthz   # 200
$ curl -s http://127.0.0.1:8765/api/status # JSON snapshot
```

With no testnet running it serves an empty roster (routers: 0) and
`/`; against the live testnet it discovered all 53 containers from the
Docker topology with no hardcoding.

## Known limitations / open questions

1. **go-i2p bootstrap floor**: upstream go-i2p hardcodes
   `emergencyReseedFloor = 40` — a testnet with fewer than 40 RouterInfos
   in the shared netDb makes go-i2p exit before I2PControl starts, so it
   shows red until enough routers exist or the first `sync` lands. The
   dashboard reports this faithfully; fixing go-i2p itself is out of
   scope here.
2. **go-i2p I2PControl over cleartext HTTP** on the internal bridge
   (go-i2p has no TLS cert generation for I2PControl; the explicit
   `allow_plaintext_non_loopback: true` in its config is the required
   opt-in). Acceptable for an internal-only testnet; noted for anyone
   reusing these configs beyond the bridge.
3. **Signal handling in the CLI (pre-existing)**: SIGINT/SIGTERM prints
   "Initiating cleanup…" but the readline loop keeps running; only the
   `exit` command (or EOF) actually cleans up. The dashboard follows the
   same lifecycle (`stop`/`exit` shut it down). Not changed here.
4. **i2pd logs the I2PControl password at debug level** on authenticate.
   We scrub it from captured logs, but the router's own stdout still
   contains it inside the container; consider `loglevel = warn` for
   i2pd routers in future if that's a concern.
5. Dashboard history is per-process in memory (by design): restarting the
   CLI clears it; `stop` also removes routers from the roster since the
   router list is the testnet's state.
