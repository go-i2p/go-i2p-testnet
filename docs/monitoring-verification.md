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
- **Topology model** (`lib/monitor/topology`): observed edge matched by peer
  hash prefix; an ambiguous prefix is never guessed (falls back to address,
  then to an external node); match by address; inferred edge from netDb
  membership; observed always dedupes inferred in both arrival orders; a
  router's own RouterInfo produces no self-edge; unmatchable sessions warn
  rather than vanish; hash match needs no address; `generated_at` recorded
  and defaulted; identical inputs give an identical graph over 25 runs;
  degree counts kinds; empty roster is not an error.
- **Topology collection** (`collector_test.go`): session rows parsed from a
  real i2pd transports page including the `&#8658;` entity and per-section
  transport name; multi-transport pages; nine kinds of malformed or
  non-session line ignored, while a session to a non-testnet peer still
  parses; netDb listing parsed from paths or bare names, deduped, rejecting
  directory rows and shell noise; `router.info` parsed to the ident hash that
  the router's own netDb file name uses (fixture captured from a live
  router), accepting both binary and base64 forms, and rejecting junk.
- **Discovery** (`discovery_test.go`): container list filtered to `router-*`
  on the testnet network, unrelated containers ignored, unaddressed or
  unattached routers skipped, missing network settings tolerated,
  implementation classified by name then image, container id and IP threaded
  into poller targets.
- **State machine** (`poller_test.go`): driven through real poll cycles —
  a router that stops answering goes `stale` while its last sample is still
  recent (sample retained, failure classified, history not extended by
  failures), then `down` once the data ages out; `not-serving` for
  connection refused; failure counts and reason cleared on recovery;
  classification table for refused/timeout/auth/protocol/internal/unreachable;
  network totals summed, nil when nothing reports the field, ignoring
  hour-old samples; network history recorded and capped at 60.
- **Charts** (`chart_test.go`): SVG contains polylines built from samples;
  different data produces different geometry and a rising series plots
  upward; a value no router reports renders as an explanatory gap, never a
  fabricated zero line; a single sample explains itself; axis scale fits the
  data and stays integral for integer metrics; router-controlled titles and
  labels are escaped; all-zero data still renders.
- **Routes and rendering** (`routes_test.go`, `sort_test.go`): `/topology`
  renders nodes, both edge kinds, transport, peer address, generation time
  and an SVG; `/api/topology` shape and per-edge `match`; no credentials in
  either payload; topology unavailable explains itself with 503; index shows
  summed network totals and charts; detail page charts and
  "not served by i2pd" labelling; down routers render no broken cells and
  show reason plus failure streak; sorting puts trouble first, orders
  uptime/peers correctly, falls back to name for unknown columns, and marks
  the active column; `parseDurationDisplay` distinguishes "2m 10s" (130)
  from "45s" and refuses to turn an explanation into a number.
- **Config generation** (`lib/i2pd/config_test.go`): the real generator
  embeds the password i2pd needs and enables `[i2pcontrol]` on the container
  IP, while the redaction applied before logging removes it and leaves the
  rest intact; routers default to `loglevel=info`, honour
  `TESTNET_I2PD_LOGLEVEL`, and reject an invalid value by falling back
  rather than letting i2pd exit on it.
- **Log-derived diagnosis** (`logs_scrub_test.go`): a router stuck below the
  peer floor is explained from its own captured log, as is a missing netDb;
  healthy, unrelated, missing or unreadable logs produce no claim; only the
  recent tail is read; and the explanation is rule text that can never echo
  log content, so a secret in a log line cannot reach the page.
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
- After `sync`: the i2pd routers were `up` and peer counts became non-zero
  (`known_peers` 1→…), proving live data flows through the dashboard. The
  go-i2p router stayed `down` even after `sync` on a 4-router net, because
  the shared netDb held 3 RouterInfos against its 40-peer floor — see
  limitation 1. An earlier version of this document claimed all routers came
  up at this scale; that was only true once enough routers existed.

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

## 8. Graphs, network totals and topology (added after the feature audit)

The audit found charts and the topology view entirely absent, and network
totals limited to health counts. All three are now implemented and were
verified against a live 4-router testnet (3x i2pd + 1x go-i2p).

**Routes.** `/`, `/healthz`, `/api/status`, `/topology` and `/api/topology`
all returned 200 against the running server.

**Charts are drawn from real samples.** `/api/status` history for one router
held 23 samples with 23 distinct `collected_at` values at 5 s spacing and a
monotonically increasing `uptime_ms` (14000 -> 256000). The index page rendered
5 chart boxes containing 5 `<polyline>` elements; the router detail page
rendered 5 polylines from its own history. Axis labels are integral for integer
metrics (`0/5/10/50/100/150`, `9.8 KiB/s/19.5 KiB/s/29.3 KiB/s`) rather than
the fractional values a naive scale produces.

**Network totals.** `/api/status` `network` object with all routers up:

```json
{"routers": 4, "up": 3, "known_peers": 9, "active_peers": 6,
 "tunnels_participating": 435, "bw_inbound_1s": 124, "bw_outbound_1s": 123}
```

After stopping one router the sums fell to `known_peers: 6, active_peers: 3`,
confirming the aggregate tracks the roster rather than being frozen. Fields no
router reports stay absent instead of becoming a fabricated zero.

**Topology, live graph with observed edges.** After `sync` and enough settle
time the graph reported 6 observed and 3 inferred edges:

```text
router-i2pd-1 -> router-i2pd-2 | observed | hash | SSU2  | rx 41457
router-i2pd-2 -> router-i2pd-3 | observed | hash | NTCP2 | rx 15576
router-goi2p-4 -> router-i2pd-1 | inferred | netdb
```

Every observed edge matched its peer **by ident hash**, not by address guess:
the hash prefixes come from each i2pd router's own transport table, and each
node's hash was derived by parsing that router's `router.info`. The page shows
the generation time (`Graph generated 2026-11-10 18:44:38 EST (just now)`),
solid lines for observed and dashed for inferred, and 4 nodes / 11 edges in the
SVG. `warnings` was empty when all routers were reachable.

**Disruption.** `docker stop router-i2pd-1` produced, within about 30 s:

- status: `down`, reason `timeout`, last 42 samples retained (history is not
  truncated by a failure);
- topology: `running=False`, the 4 edges that had been observed from that
  router correctly **demoted to inferred** (its peers still store its
  RouterInfo, but no session can exist), and the warning
  `router-i2pd-1 is not running, so its sessions are not in this graph`.

`docker start` restored `up`, cleared `failure_reason` and the consecutive
failure count, and returned the graph to 6 observed / 3 inferred edges.

**Stale is now reachable.** The audit found the amber state unobservable in
live operation because any failure jumped straight to red. `docker pause
router-i2pd-2` now produces the intended progression, one poll interval apart:

```text
t+6s  up    -> stale  (reason timeout, last sample still shown)
t+30s stale -> down    (sample aged past the window)
```

**Unobservable router explained.** `router-goi2p-4` shows `down` with reason
`not-serving`, and both the index row and its detail page carry a plain-language
diagnosis derived from the router's own captured log:

```text
Why: the router is still bootstrapping: it refuses to start its control
interface until it has the minimum number of peers (the log names that floor).
Publish RouterInfos to it with `sync`, or add enough routers, then it will come up.
```

**Credential handling re-verified with the new log level.** With a known probe
password, `docker logs <router>` produced **0** matches for all three i2pd
routers (the audit reproduced 1 match at debug level), and `/`, `/api/status`,
`/topology`, `/api/topology` and `logs/*.log` all produced 0 matches. The
generated config now carries `loglevel = info`.

**Standalone mode.** `./bin/go-i2p-testnet -dashboard -dashboard-addr
127.0.0.1:8899` discovered all 4 containers from the Docker topology with no
in-memory roster and served the same topology graph (4 nodes, 6 observed / 3
inferred edges, hashes derived for the three i2pd routers).

## Known limitations / open questions

1. **go-i2p bootstrap floor**: upstream go-i2p requires 40 RouterInfos
   before it starts its I2PControl server. Confirmed not configurable:
   `--bootstrap.low-peer-threshold 1` and `--bootstrap.minimum-reseed-peers 1`
   both still log `threshold=40`, and after 5 attempts the process exits
   (`EXIT=1`) and the container's retry loop restarts it, so nothing ever
   listens on 7650 below that floor. A small testnet therefore shows the
   go-i2p router as `down` / `not-serving` with no metrics, now accompanied by
   the log-derived explanation above. Fixing the floor belongs to go-i2p.
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
   Addressed at the source: routers are configured at `loglevel = info` by
   default, so the credential no longer reaches container stdout at all
   (verified: 0 matches in `docker logs`). Captured files remain scrubbed, and
   `TESTNET_I2PD_LOGLEVEL=debug` still opts back in for debugging, with a
   startup warning that the password will appear in raw container logs.
5. Dashboard history is per-process in memory (by design): restarting the
   CLI clears it; `stop` also removes routers from the roster since the
   router list is the testnet's state.
