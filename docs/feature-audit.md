# Feature Audit: is go-i2p-testnet feature-complete for a testnet operator?

Audited commit: `6f75e72` (branch `experimental`). Method: every requirement mapped to
code, tests and docs, plus a live end-to-end run performed by the auditor (a 4-router
testnet was started, polled, disrupted, and torn down again — see
[Live verification](#live-verification)).

## Verdict

**Partially complete.** The dashboard genuinely observes and records per-router state
and aggregate health *counts* for reachable routers, but two of the five operator
capabilities — time-series **graphs** and the **topology/edges** view — are entirely
absent, and the go-i2p router (the very component this testnet exists to debug) is
never observable because its I2PControl endpoint never binds.

Note on branch: the audit was requested "on `main`". This repository has no `main`
branch — `origin/HEAD -> origin/experimental`, and `experimental` is the only shared
branch (`git ls-remote --heads origin` returns exactly one ref) — so the audit was
performed on and is committed to `experimental`. Creating `main` would have conflicted
with "do not create branches". This divergence is already recorded in
[monitoring-discovery.md](./monitoring-discovery.md), "Divergences from the task
prompt", item 1.

---

## 1. Operator capabilities

### 1.1 Observe each router individually

*Status, uptime, version, tunnels, peers, bandwidth, recent history; clear indication
when a router is down or stale.*

**Status: Partial** — works well for i2pd routers; tunnel counts are blank for i2pd,
and go-i2p routers cannot be observed at all.

Evidence:

- All fields are requested: `RequestedProperties` covers uptime, version, status, net
  status, tunnels in/out/participating, bandwidth 1s/15s, and known/active/fast/
  high-capacity peers
  ([client.go:64-69](../lib/monitor/i2pcontrol/client.go)).
- All fields are decoded onto `Sample` ([client.go:103-121](../lib/monitor/i2pcontrol/client.go))
  and rendered per router: index table
  ([index.tmpl:35-52](../lib/monitor/web/templates/index.tmpl)) and detail key/value
  list ([router.tmpl:24-40](../lib/monitor/web/templates/router.tmpl)).
- Down and stale are clearly indicated: states at
  [poller.go:19-23](../lib/monitor/web/poller.go), down assigned on poll failure at
  [poller.go:214](../lib/monitor/web/poller.go), stale sweep at
  [poller.go:233-249](../lib/monitor/web/poller.go) (3 missed intervals,
  `StaleIntervals` at [poller.go:30](../lib/monitor/web/poller.go)), colour classes at
  [format.go:60-71](../lib/monitor/web/format.go), rendered with the last error at
  [index.tmpl:53-55](../lib/monitor/web/templates/index.tmpl).
- Recent history: last 60 samples kept and shown
  ([poller.go:26](../lib/monitor/web/poller.go),
  [poller.go:222-224](../lib/monitor/web/poller.go),
  [router.tmpl:42-59](../lib/monitor/web/templates/router.tmpl)).
- Tests exercise behaviour, not existence: mixed up/down/slow routers, down-after-death,
  stale sweep, amber rendering, dynamic add/remove, history cap
  ([poller_test.go:81-296](../lib/monitor/web/poller_test.go)); rendered content and
  HTML escaping asserted ([server_test.go:56-137](../lib/monitor/web/server_test.go)).

Two real gaps, both confirmed live:

1. **go-i2p routers are never observable.** On a fresh 4-router testnet (3× i2pd + 1×
   go-i2p) at `6f75e72`, `router-goi2p-4` stayed `down` with
   `dial tcp 172.28.0.5:7650: connect: connection refused` for its entire life while all
   three i2pd routers were `up`. Inside the container, **nothing listens on 7650**
   (`netstat -ltn` shows only the embedded DNS resolver and the NTCP2 port). go-i2p's
   own captured log proves it reads and validates the config, then never gets as far as
   serving it:

   ```text
   level=warning msg=I2PControl is bound to a non-localhost address over HTTP ... address=0.0.0.0:7650 at=validateI2PControlConfig
   level=debug   msg=I2PControl configuration validated successfully at=validateI2PControlConfig
   level=warning msg=initial reseed incomplete, retrying after backoff ... current_size=3 threshold=40
   ```

   The router re-enters bootstrap retry forever because upstream go-i2p enforces a
   40-peer floor despite this repo's `low-peer-threshold: 1`
   ([config.go:99-115](../lib/go-i2p/config.go)). `monitoring-verification.md`
   "Known limitations" 1 attributes this to upstream `emergencyReseedFloor = 40` and
   calls fixing go-i2p out of scope — fair for the *router*, but the **dashboard has no
   fallback** (no log-derived or webconsole-derived status, and no state distinct from
   "down" for "control endpoint not yet serving"), so an operator sees no state,
   uptime, version, tunnels, peers, bandwidth or history for the router under test. For
   a tool whose stated purpose is debugging go-i2p's netDb behaviour
   ([README.md:3-7](../README.md)), that is a capability-1 failure, not cosmetic.
2. **Tunnels are blank for i2pd.** Live `/api/status` for i2pd 2.56.0 returned
   `"tunnels_inbound": null, "tunnels_outbound": null` — only `participating` is served —
   so the column renders `—/—/429` via `numI64`
   ([format.go:80-85](../lib/monitor/web/format.go)). i2pd does not expose those two
   properties over I2PControl and there is no alternate source. Note the inversion: the
   only implementation that *does* serve tunnel counts (go-i2p) is the one that cannot
   be polled.

### 1.2 Observe the network as a whole

**Status: Partial** — aggregate *router counts* by health exist; no aggregate peer,
tunnel or bandwidth totals, and no network-level health verdict.

- `Totals()` returns routers/up/down/stale/pending only
  ([poller.go:346-363](../lib/monitor/web/poller.go)); rendered as the summary header
  ([index.tmpl:26-32](../lib/monitor/web/templates/index.tmpl)) and serialized at
  [server.go:329](../lib/monitor/web/server.go).
- Live confirmation: `/api/status` top-level keys were exactly
  `['generated_at','refresh_seconds','totals','routers']` and `totals` keys exactly
  `['routers','up','down','stale','pending']`. An operator can read "4 routers, 3 up,
  1 down" but cannot read total peers, total tunnels or network bandwidth without
  summing rows by hand.
- No aggregate is derived even though the inputs are already in memory
  (`Snapshot()` at [poller.go:316-325](../lib/monitor/web/poller.go) hands every
  caller the full per-router set).

### 1.3 Graph network state over time

**Status: Missing** — nothing is plotted anywhere. History exists as data and as a
table, but there are no charts.

Evidence for absence:

- No charting markup exists in the source. A repo-wide search of `*.go`, `*.tmpl` and
  `*.md` for `topolog|graph|edge|chart|sparkline|svg|polyline|canvas` returns only
  prose (`main.go:484` comment, `dashboard.go:3` doc comment, `README.md:155`) — no
  implementation.
- The templates contain **zero** `<script>` tags (verified: `grep -c '<script'` → 0 for
  both [index.tmpl](../lib/monitor/web/templates/index.tmpl) and
  [router.tmpl](../lib/monitor/web/templates/router.tmpl)), so no client-side plotting
  is possible.
- Live confirmation: the served index HTML matched
  `svg|<canvas|polyline|<path |chart.js|d3.|<img` **0 times**.
- What history does reach the operator is a table of up to 60 rows, newest first
  ([router.tmpl:42-59](../lib/monitor/web/templates/router.tmpl)), plus the JSON array
  in `/api/status` ([server.go:344-348](../lib/monitor/web/server.go)). That is recent
  values, not trends.

The data side is **not** the problem — it is real and ready, verified live:

- Per-router history grew 4 → 25 → 31 → 33 → 60 samples over ~5 minutes of polling.
- The samples are genuine time-series, not a duplicated value: 60 entries produced
  **60 distinct `collected_at` timestamps** spanning `15:07:33Z → 15:12:28Z`, with
  `uptime_ms` monotonic (newest ≥ oldest) and bandwidth varying sample to sample.
- Peer/tunnel/bandwidth over time are therefore all available per router
  (`apiSample`, [server.go:264-282](../lib/monitor/web/server.go)). Network-level
  trends additionally need the 1.2 aggregates to be retained over time, which is not
  done today.

Nothing is hardcoded — but nothing is drawn either.

### 1.4 See the edges (topology view)

**Status: Missing** — no topology route, no edge data in the dashboard, no graph update
time, no observed-vs-inferred distinction.

- Registered routes are exactly `/`, `/router/`, `/api/status`, `/healthz`
  ([server.go:68-71](../lib/monitor/web/server.go)). Live probes on the running
  dashboard:

  | path | status |
  |---|---|
  | `/` | 200 |
  | `/router/router-i2pd-1` | 200 |
  | `/api/status` | 200 |
  | `/healthz` | 200 |
  | `/topology` | **404** |
  | `/api/topology` | **404** |
  | `/graph` | **404** |
  | `/topology.json` | **404** |

- No edge/adjacency type exists anywhere under `lib/monitor/`. `Sample` carries peer
  *counts* only ([client.go:103-121](../lib/monitor/i2pcontrol/client.go)) and the
  requested property set contains no peer identities
  ([client.go:47-62](../lib/monitor/i2pcontrol/client.go)) — so a topology cannot be
  built from I2PControl as currently queried.
- The sub-requirements ("show the graph's update time", "observed or inferred") have no
  implementation to inspect.

Nuance that matters for whoever fixes this: **the edges are already derivable from real
router data, not a hardcoded list.** The CLI `peers` command
([main.go:563-632](../main.go)) parses i2pd's webconsole transport table
(`i2pdSessionRe`, [main.go:558](../main.go)) plus `netstat` ESTABLISHED sessions, and
maps peer IPs back to router names. The auditor ran it live and got real edges:

```text
router-i2pd-1 (172.28.0.2) rx=324972 bytes, tx=265548 bytes
  SSU2 session -> router-i2pd-2 (172.28.0.3:4567) sent=53316 recv=54357 bytes
  SSU2 session -> router-i2pd-3 (172.28.0.4:4567) sent=22757 recv=28345 bytes
router-i2pd-2 (172.28.0.3) rx=264564 bytes, tx=257437 bytes
  SSU2 session -> router-i2pd-1 (172.28.0.2:4567) sent=54357 recv=53316 bytes
  SSU2 session -> router-i2pd-3 (172.28.0.4:4567) sent=74694 recv=59936 bytes
router-goi2p-4 (172.28.0.5) rx=113008 bytes, tx=14214 bytes
  no active transport sessions
```

So the raw material for an observed-edge topology already exists in the repo, is
config- and topology-derived, and even distinguishes transport types (SSU2 vs NTCP2 vs
TCP) — but it is terminal-only output of an interactive command, never surfaced in the
dashboard, never persisted, and never timestamped. It also re-exposes the go-i2p gap:
go-i2p reports no sessions, so it would appear disconnected.

### 1.5 Read it easily

**Status: Implemented**, with minor reservations.

- Consistent units: bytes/sec with explicit binary suffixes and a dash for unknown
  ([format.go:9-24](../lib/monitor/web/format.go)), durations as `Xh YYm ZZs`
  ([format.go:26-41](../lib/monitor/web/format.go)), relative "last seen"
  ([format.go:43-58](../lib/monitor/web/format.go)). All display values are precomputed
  in Go so templates never dereference a nil sample (`RouterRow`/`rowFrom`,
  [server.go:119-170](../lib/monitor/web/server.go)).
- Clear labels and headers ([index.tmpl:35-40](../lib/monitor/web/templates/index.tmpl)),
  unknown values as `—` rather than a misleading `0`, colour-coded health with counts in
  the header, auto-refresh driven by the configured interval
  ([index.tmpl:5](../lib/monitor/web/templates/index.tmpl)).
- No page requires reading raw JSON: `/` and `/router/{name}` are HTML, and JSON is an
  opt-in footer link ([index.tmpl:61](../lib/monitor/web/templates/index.tmpl)).
- Sensible sorting: **partially.** Routers are sorted by name only
  ([poller.go:323](../lib/monitor/web/poller.go),
  [server.go:331](../lib/monitor/web/server.go)) — deterministic, but it never brings a
  sick router to the top. On a 50-router testnet the single `down` router is found by
  scanning. No sort control exists.
- Minor defects: the detail page hardcodes a 5-second refresh instead of using the
  configured interval ([router.tmpl:5](../lib/monitor/web/templates/router.tmpl) vs
  `{{.RefreshSecs}}` on the index), and the index shows only 1-second bandwidth while
  the steadier 15-second averages appear only on the detail page.

---

## 2. Supporting requirements

| Requirement | Status | Evidence | Note |
|---|---|---|---|
| Router discovery from existing config/topology, not hardcoded | **Implemented** | CLI in-memory list snapshot `cliRouters()` ([main.go:66-80](../main.go)); Docker discovery `RoutersFromDocker` ([dashboard.go:225-268](../lib/monitor/dashboard/dashboard.go)); target building `Source.Targets` ([dashboard.go:133-156](../lib/monitor/dashboard/dashboard.go)); re-synced every cycle by `syncTargets` ([poller.go:251-313](../lib/monitor/web/poller.go)) | Verified live in **both** modes: in-process (`start` + `add`), and standalone `-dashboard`, which discovered all 4 running containers by name prefix and IP with no router list in memory. Tests: dynamic add/remove ([poller_test.go:199-227](../lib/monitor/web/poller_test.go)), per-implementation URLs and IP-less skipping ([dashboard_test.go:89-117](../lib/monitor/dashboard/dashboard_test.go)). No router name, IP or count appears as a literal anywhere in `lib/monitor/`. |
| Concurrent polling, ~5 s per-router timeout, one router cannot block the others | **Implemented** | `web.NewPoller(5*time.Second, 5*time.Second, src.Targets)` ([main.go:98](../main.go)); `DefaultTimeout = 5 * time.Second` ([client.go:23](../lib/monitor/i2pcontrol/client.go)) applied per request ([client.go:431](../lib/monitor/i2pcontrol/client.go)); one goroutine per router ([poller.go:200](../lib/monitor/web/poller.go)); skip-if-still-in-flight instead of waiting ([poller.go:189-197](../lib/monitor/web/poller.go)); the cycle returns without joining stragglers ([poller.go:164-171](../lib/monitor/web/poller.go)) | Verified live: `router-goi2p-4` refused connections continuously while the 3 i2pd routers kept refreshing every 5 s; later, stopping one i2pd router left the other two `up` throughout. Tests assert non-blocking explicitly ([poller_test.go:132-161](../lib/monitor/web/poller_test.go)) and per-router timeout ([client_test.go:206-229](../lib/monitor/i2pcontrol/client_test.go)). `go test -race ./lib/monitor/...` clean. |
| Routes `/`, `/router/{name}`, `/api/status`, `/healthz` | **Implemented** | Registration [server.go:68-71](../lib/monitor/web/server.go); handlers `handleHealthz` ([server.go:106](../lib/monitor/web/server.go)), `handleIndex` ([server.go:217](../lib/monitor/web/server.go)), `handleRouter` ([server.go:243](../lib/monitor/web/server.go)), `handleAPIStatus` ([server.go:325](../lib/monitor/web/server.go)) | All four returned 200 live. 404 for unknown paths and unknown routers tested ([server_test.go:116-137,189-198](../lib/monitor/web/server_test.go)). `/healthz` is path-exact; `/router/{name}` accepts a single segment. |
| **Plus a topology route** | **Missing** | No handler registered; `/topology`, `/api/topology`, `/graph`, `/topology.json` all 404 live | Explicit requirement, unmet. See §1.4. |
| Last 60 samples per router kept in memory | **Implemented** | `const HistorySamples = 60` ([poller.go:26](../lib/monitor/web/poller.go)); prepend + truncate ([poller.go:222-224](../lib/monitor/web/poller.go)); exposed on the detail page ([server.go:255-257](../lib/monitor/web/server.go)) and API ([server.go:344-348](../lib/monitor/web/server.go)) | Verified live: three routers each held exactly 60 samples with 60 distinct timestamps; growth measured 4 → 60. Cap enforced by test ([poller_test.go:243-265](../lib/monitor/web/poller_test.go)). In-memory only — a CLI restart clears it (documented as by design). |
| Bind to 127.0.0.1 by default | **Implemented** | `cfg.BindAddr = "127.0.0.1:8765"` ([server.go:42-44](../lib/monitor/web/server.go)); flag default identical ([main.go:59](../main.go), [main.go:734-735](../main.go)) | Verified live with `ss -ltnp`: `LISTEN 127.0.0.1:8765 users:(("go-i2p-testnet",pid=...))` — not `0.0.0.0`. |
| Passwords only from environment variables or gitignored files, never logged | **Implemented** | Order env → file → generated: `EnvPassword` ([dashboard.go:32](../lib/monitor/dashboard/dashboard.go)), `ResolvePassword` ([dashboard.go:166-195](../lib/monitor/dashboard/dashboard.go)), 0600 write ([dashboard.go:190](../lib/monitor/dashboard/dashboard.go)); factory default `itoopie` refused without opt-in ([dashboard.go:197-203](../lib/monitor/dashboard/dashboard.go)); `Target.Password` absent from every rendered/serialized shape (`RouterRow` [server.go:119-142](../lib/monitor/web/server.go), `apiRouter` [server.go:284-293](../lib/monitor/web/server.go)); RPC errors redacted ([client.go:345](../lib/monitor/i2pcontrol/client.go), `redact` [client.go:568-576](../lib/monitor/i2pcontrol/client.go)); captured router logs scrubbed (`SetSecrets`/`scrub` [logs.go:79-97](../lib/monitor/logs.go), applied [logs.go:205](../lib/monitor/logs.go), wired [main.go:787](../main.go)) | Verified live: `logs/.i2pcontrol-password` is `-rw-------` and matched by `.gitignore:2 logs/`; fixed-string grep for the live 48-hex password returned **0** matches in `/`, `/api/status`, `/router/*` and all four `logs/*.log`. A real leak was caught and fixed here — `logs/router-i2pd-1.log` contains `I2PControl: Authenticate API=1 [REDACTED]`. Tests: password order and default refusal ([dashboard_test.go:13-88](../lib/monitor/dashboard/dashboard_test.go)), error redaction ([client_test.go:279-293](../lib/monitor/i2pcontrol/client_test.go)), scrubbing ([logs_scrub_test.go](../lib/monitor/logs_scrub_test.go)). Residual, acknowledged in `monitoring-verification.md` limitation 4: i2pd runs at `loglevel = debug` ([config.go:147](../lib/i2pd/config.go)), so the password still appears in the router's own stdout *inside* the container — only the captured copies are scrubbed. |
| Standard library only | **Partial** | `lib/monitor/web` and `lib/monitor/i2pcontrol` import **only** stdlib (verified by enumerating every import block: `context`, `embed`, `encoding/json`, `html/template`, `net`, `net/http`, `crypto/tls`, `crypto/x509`, …) | Read strictly, this fails: the monitoring feature also pulls the Docker SDK (`dashboard.go:24-25`, `logs.go:15-17`) and `sirupsen/logrus` via the shared logger (`lib/utils/logger/log.go:9`), because discovery and log capture need the Docker API. The README's narrower claim is accurate — "stdlib only: no external web server, database, CDN, or frontend framework" ([README.md:138-140](../README.md)) — and it holds: templates are `go:embed`ed, no JS dependency, storage in memory. |
| Docs: `docs/monitoring-discovery.md` | **Implemented** | [monitoring-discovery.md](./monitoring-discovery.md), 167 lines | Present and substantive, but carries several claims contradicted by the code — see [Documentation vs code](#documentation-vs-code). |
| Docs: `docs/monitoring-verification.md` | **Implemented** (added late) | [monitoring-verification.md](./monitoring-verification.md), 184 lines, commit `3bf0502` | **Absent for most of this audit.** At the state the audit began from it did not exist, while `monitoring-discovery.md:7` pointed at it ("see `docs/monitoring-verification.md` for the end-to-end results") — a dangling reference. Added mid-audit by concurrent work. Spot-checked: its static claims reproduce exactly (`go build ./...`, `go vet ./...`, `gofmt -l .` silent, `go test ./...` and `go test -race ./lib/monitor/...` all green) and its §5 stop/recover test reproduced independently. Its §4 53-router run was not reproduced — **Unverified**. |
| Docs: README | **Implemented** | "Status dashboard" section, [README.md:136-190](../README.md) | Accurate on routes, polling model, discovery, password handling and pinning; overstates tunnel availability and I2PControl coverage (see below). |

---

## Prioritized gap list

### Blocking

1. **No topology/edges view (§1.4).** No route, no edge model, no observed-vs-inferred
   flag, no graph update timestamp. An operator cannot answer "is everyone actually
   connected, or is the testnet partitioned?" — arguably the central question for a
   multi-implementation testnet. Only partially mitigated by the terminal `peers`
   command, which is not part of the dashboard and is unavailable in standalone mode.
2. **No charts (§1.3).** Peer count, tunnel count and bandwidth appear as a table of up
   to 60 rows; there is no trend view at all, despite 60 real timestamped samples per
   router already being held in memory and exposed over `/api/status`.
3. **The go-i2p router is never observable (§1.1).** Its I2PControl endpoint does not
   bind until upstream go-i2p clears the 40-peer bootstrap floor, so on any testnet
   smaller than ~40 RouterInfos the router under test is permanently red with a dial
   error and no history. The root cause is upstream, but the *blocking dashboard gap* is
   the absence of any fallback observation path — log-derived state, a webconsole scrape,
   or a "control endpoint not serving yet" state distinct from "down".

### Important

4. **No network-wide aggregates (§1.2).** Totals are health counts only: no sum of
   peers, tunnels or bandwidth across routers, and no network-level health verdict.
5. **Tunnel in/out unavailable for i2pd.** i2pd does not serve those properties over
   I2PControl (live `null` → `—`), so the tunnel columns are empty for the majority of
   routers, with no alternate source and no caveat in the README.
6. **Sorting cannot surface problem routers.** Name-only ordering
   ([poller.go:323](../lib/monitor/web/poller.go)); on a large testnet a single
   down/stale router is easy to miss.
7. **Detail page ignores the configured poll interval** — hardcoded `content="5"`
   ([router.tmpl:5](../lib/monitor/web/templates/router.tmpl)) while the index uses the
   real value.
8. **"Standard library only" is not literally true** for the monitoring feature (Docker
   SDK + logrus), although the narrower README claim holds. Either reword the
   requirement or scope the stdlib assertion to `web` + `i2pcontrol`.

### Minor

9. **Documentation drift** in `monitoring-discovery.md` — wrong flag name, wrong i2pd
   version, wrong bind address, and a promised command-line redundancy that does not
   exist (next section).
10. **Password still present in container stdout** because i2pd runs at `loglevel =
    debug` ([config.go:147](../lib/i2pd/config.go)); captured logs are scrubbed, the
    in-container stream is not.
11. **Index shows only 1-second bandwidth**; the steadier 15-second average is
    detail-page only.
12. **No history persistence** across a CLI restart (documented as by design, but it
    means long-window graphs stay impossible even after item 2 is fixed).
13. **`/healthz` is a liveness stub** — it returns `ok` even when every router is down
    ([server.go:106-115](../lib/monitor/web/server.go)). Correct for liveness,
    misleading if read as "the testnet is healthy".

---

## Documentation vs code

Claims that do not match the code as of `6f75e72`:

| # | Claim | Where | Reality |
|---|---|---|---|
| 1 | "see `docs/monitoring-verification.md` for the end-to-end results" | [monitoring-discovery.md:7](./monitoring-discovery.md) | The file did not exist in the state this audit began from; the reference was dangling until commit `3bf0502` added it mid-audit. |
| 2 | "An explicit `-insecure-tls-skip-verify` flag exists and prints a loud warning" | [monitoring-discovery.md:146](./monitoring-discovery.md) | The flag is `-insecure-tls` ([main.go:738](../main.go)); the README uses the correct name ([README.md:162](../README.md)). The documented flag does not exist and would abort flag parsing. |
| 3 | i2pd `GenerateRouterConfig` sets `address=0.0.0.0` | [monitoring-discovery.md:118](./monitoring-discovery.md) | i2pd binds the **container IP**, not `0.0.0.0`: `Address: ip` ([config.go:248-253](../lib/i2pd/config.go)). The `0.0.0.0` in the doc's own probe section describes a throwaway container, not the testnet config. |
| 4 | "The same flags are passed on the command line so they hold even if the config file isn't picked up" (go-i2p I2PControl) | [monitoring-discovery.md:126-127](./monitoring-discovery.md) | The container command passes only bootstrap and netdb flags — no `i2pcontrol` flags ([container.go:69-77](../lib/go-i2p/container.go)). The config file *is* read (go-i2p logs `validateI2PControlConfig` for `address=0.0.0.0:7650`), so the redundancy the doc promises does not exist. |
| 5 | i2pd is version **2.60.0** (stated as the image package and as the observed `i2p.router.version`) | [monitoring-discovery.md:4,44,74,84,92](./monitoring-discovery.md) | Live routers report **2.56.0**: `i2pd --version` → `i2pd version 2.56.0 (0.9.65)`, and the dashboard served `version: "2.56.0"` for every i2pd router. `monitoring-verification.md` says 2.56.0, so the two docs contradict each other. |
| 6 | Index shows "version, net status, uptime, **tunnels**, peers, bandwidth" | [README.md:145](../README.md) | Tunnel inbound/outbound are never populated for i2pd (live `null` → `—`); only `participating` is served. The README presents tunnels as available. |
| 7 | "every router now serves I2PControl (JSON-RPC on port 7650)" | [README.md:158](../README.md) | True as configuration intent, false as observed behaviour: the go-i2p router never binds 7650 below the 40-peer floor and was `down` for the auditor's entire run. `monitoring-verification.md` limitation 1 acknowledges this; the README does not. |
| 8 | "Red within one poll interval (≤ ~10 s …)" and the recovery claim | [monitoring-verification.md](./monitoring-verification.md) §5 | **Confirmed, not drift** — independently reproduced (§Live verification 5–6). Listed because it is the doc's strongest behavioural claim and it held. |
| 9 | "polling 53 routers with 5 s timeouts never serialized" | [monitoring-verification.md](./monitoring-verification.md) §4 | Plausible and consistent with the code, but **Unverified** — the auditor did not run a 40+ container testnet. |

---

## Live verification

### What the auditor verified live

Performed at `6f75e72` on a testnet the auditor started, disrupted and tore down:
`make build`; CLI `start`, `add i2pd_router` ×3, `add goi2p_router`, `sync`, `status`,
`peers`; direct `curl` against the dashboard; `docker stop` / `docker start` /
`docker exec netstat` / `docker inspect` / `docker events`; `ss -ltnp`; fixed-string
secret greps.

1. **All routers appear** — `/api/status` reported `totals.routers: 4`, and the index
   rendered `Routers: 4 / Up: 3 / Down: 1`, listing `router-i2pd-1/2/3` and
   `router-goi2p-4` with their real bridge IPs and per-implementation URLs
   (`https://…` pinned for i2pd, `http://…` for go-i2p).
2. **History fills in over time** — per-router history grew 4 → 25 → 31 → 33 → 60
   samples over ~5 minutes; the detail page rendered "Last 60 samples" with 61 table
   rows (header + 60). The 60 samples carried **60 distinct timestamps**
   (`15:07:33Z → 15:12:28Z`) and monotonic `uptime_ms`, proving real polling rather
   than a repeated value.
3. **No graphs exist** — 0 matches for chart/svg/canvas/img/script markup in the live
   index HTML.
4. **No topology** — `/topology`, `/api/topology`, `/graph`, `/topology.json` all
   returned 404 from the live server.
5. **Stop-one-router behaviour confirmed** — `docker stop router-i2pd-3` while `up`
   (39 samples): within 10 s the API reported `state: down` with
   `i2pcontrol: request timed out: router router-i2pd-3`, later
   `… connect: no route to host`; the index row rendered `class="down"` with
   `<td class="state">down</td>` plus the error line; the detail page showed `[down]`
   and a "Last error" entry; the header moved to `Up: 2 / Down: 2`. The other routers
   stayed `up` throughout — one router's failure did not affect the rest.
6. **Recovery confirmed** — `docker start router-i2pd-3` returned it to `up` with an
   empty `last_error` within ~30 s.
7. **Standalone discovery confirmed** — `./bin/go-i2p-testnet -dashboard
   -dashboard-addr 127.0.0.1:8766` discovered all 4 running containers from the live
   Docker topology (no in-memory router list) and served identical data.
8. **Security confirmed** — `ss -ltnp` showed the listener bound to `127.0.0.1:8765`
   only; `logs/.i2pcontrol-password` was `-rw-------` and gitignored; a fixed-string
   search for the live 48-hex password across `/`, `/api/status`, `/router/*` and every
   `logs/*.log` returned **0** matches, while `logs/router-i2pd-1.log` shows the
   scrubber working (`I2PControl: Authenticate API=1 [REDACTED]`).
9. **Root cause of the go-i2p gap confirmed** — no listener on 7650 inside
   `router-goi2p-4`; its `config.yaml` present on the volume and validated by go-i2p;
   its log showing `bootstrap completed successfully netdb_size=3` immediately followed
   by `initial reseed incomplete, retrying after backoff … threshold=40`.
10. **Repo baseline healthy** — `go build ./...`, `go vet ./...`, `gofmt -l .` (no
    output) and `go test -count=1 ./lib/monitor/...` all green, including
    `go test -race ./lib/monitor/web/...`. **No pre-existing failure was encountered,
    so the "stop and report" condition did not trigger**, and the dashboard is present,
    so the audit did not stop at the "dashboard absent" condition either.
11. **Edges are derivable but not published** — the CLI `peers` command produced real
    SSU2 edges between the three i2pd routers with byte counters, and correctly
    reported `no active transport sessions` for the go-i2p router.

Incidental finding worth recording for operators: the auditor's *first* stop-test was
invalid because the CLI's own `sync` command sequentially restarts every i2pd router so
they load the freshly synced netDb ([main.go:540-549](../main.go)), which resurrected
the stopped container within ~1 s. The test was re-run after the CLI went idle. `sync`
is behaving correctly, but it transiently marks the whole network `down` in the
dashboard — a fact an operator should know. `monitoring-verification.md` §4 notes the
same effect at scale.

### What the auditor did NOT verify — Unverified

- **Stale (amber) state in a live run.** The auditor observed `down` and `up`
  transitions but never caught a router sitting in `stale`; that path is covered only
  by unit tests ([poller_test.go:266-296](../lib/monitor/web/poller_test.go)).
  **Unverified live** — code and tests exist, so it is not "Missing".
- **The 53-router scale claims** in `monitoring-verification.md` §4 (`totals.routers:
  53`, "53 routers polled without serializing", go-i2p reaching `up` with
  `known_peers=47`). Reproducing them requires 40+ containers; not attempted.
  **Unverified.** Its implication — that the go-i2p blocker clears at scale — is
  plausible but unproven here.
- **Certificate pinning failure against a live router.** `pinning_test.go` covers
  mismatch, and the auditor saw pinned `https://` endpoints being polled successfully,
  but did not force a mismatched cert in the live stack. **Unverified live.**
- **`-insecure-tls` warning** and **default-password refusal** — established by reading
  the code ([main.go:94-96](../main.go)) and its tests
  ([dashboard_test.go:68-88](../lib/monitor/dashboard/dashboard_test.go)), not executed.
  **Unverified live.**
- **Auth-token leakage** — only the session password was grepped for across all
  surfaces; tokens are handled by `redact()`
  ([client.go:568-576](../lib/monitor/i2pcontrol/client.go)) and unit-tested, but no
  exhaustive live sweep for tokens was made.
- **`Target.key()` embedding the password** in an internal client fingerprint
  ([poller.go:46-48](../lib/monitor/web/poller.go)). The field is unexported and absent
  from every rendered/serialized shape (confirmed by the live greps), so this is judged
  safe — but no exhaustive audit of every future log statement that could print a
  `Target` was performed.
- **Graphs and topology** — nothing to verify; both are absent, proven by 404s and by
  the total absence of rendering code.

---

## Summary table

| Capability | Status |
|---|---|
| 1. Observe each router individually | **Partial** — solid for i2pd; go-i2p unobservable; tunnel counts blank for i2pd |
| 2. Observe the network as a whole | **Partial** — health counts only, no aggregate peers/tunnels/bandwidth |
| 3. Graph network state over time | **Missing** — table of samples only; data is real and ready |
| 4. See the edges (topology) | **Missing** — no route, no edges, no timestamps; `peers` proves edges are derivable |
| 5. Read it easily | **Implemented** — minor issues: name-only sort, hardcoded detail refresh |

| Supporting requirement | Status |
|---|---|
| Discovery from config/topology, not hardcoded | **Implemented** (verified live, both modes) |
| Concurrent polling, ~5 s per-router timeout | **Implemented** (verified live) |
| Routes `/`, `/router/{name}`, `/api/status`, `/healthz` | **Implemented** (verified live) |
| Topology route | **Missing** (verified 404 live) |
| Last 60 samples per router in memory | **Implemented** (verified live) |
| Bind 127.0.0.1 by default | **Implemented** (verified live) |
| Passwords from env/gitignored file, never logged | **Implemented** (verified live, incl. scrubbing) |
| Standard library only | **Partial** — true for `web`/`i2pcontrol`; Docker SDK + logrus elsewhere in monitoring |
| `docs/monitoring-discovery.md` | **Implemented** (with drift) |
| `docs/monitoring-verification.md` | **Implemented** — added mid-audit; was a dangling reference before that |
| README | **Implemented** (overstates tunnels and I2PControl coverage) |
