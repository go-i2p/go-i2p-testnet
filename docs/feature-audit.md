# Feature audit: is go-i2p-testnet feature-complete for a testnet operator?

Audited commit: `f4d8710` (working tree clean; no source modified by this audit).
This report replaces the earlier audit in the same file: every headline conclusion was
re-derived independently from code, tests and a fresh live run, and several new findings
were added.

## Verdict

**Not feature-complete** — the dashboard is a genuinely working per-router *poller and
table* (verified live, including concurrent polling, 60-sample history, down/recover
behaviour and loopback binding), but two of the five required operator capabilities —
**graphs over time** and the **topology/edges** view — are entirely absent, and the router
the testnet exists to debug (go-i2p) is never observable at all.

### Branch note

The task said "work on `main`, do not create branches". **This repository has no `main`
branch**: `git ls-remote --heads origin` returns exactly one ref,
`refs/heads/experimental`, and `origin/HEAD -> origin/experimental`. Creating `main` would
have violated "do not create branches", so the audit was performed on, and committed to,
`experimental` — the repo's default branch. Everything else in the brief was followed
literally: no source changes, no new branches, no force-push, this report is the only write.

### Stop conditions

Neither stop condition applied:

- The repo builds and its test suite passes **before** any change (verified fresh, not
  cached): `go build ./...`, `go vet ./...`, `gofmt -l .` and `go test -count=1 ./...` all
  clean — `monitor`, `monitor/dashboard`, `monitor/i2pcontrol`, `monitor/web` all `ok`.
- The dashboard is **present**, not absent: `lib/monitor/web`, `lib/monitor/dashboard`,
  `lib/monitor/i2pcontrol` plus embedded templates, wired into the CLI at
  [main.go:83-116](../main.go), and confirmed serving live.

---

## 1. Operator capabilities

### 1.1 Observe each router individually

| | |
|---|---|
| **Status** | **Partial** |
| **Evidence** | Fields requested: `RequestedProperties` — uptime, version, status, net status, tunnels in/out/participating, bandwidth 1s/15s, known/active/fast/high-capacity peers ([client.go:43-70](../lib/monitor/i2pcontrol/client.go)); decoded onto `Sample` ([client.go:103-121](../lib/monitor/i2pcontrol/client.go), [client.go:515-535](../lib/monitor/i2pcontrol/client.go)); rendered per router ([index.html:33-56](../lib/monitor/web/templates/index.html), [router.html:23-40](../lib/monitor/web/templates/router.html)). Down: assigned on poll failure ([poller.go:213-217](../lib/monitor/web/poller.go)) with the error shown ([index.html:53-55](../lib/monitor/web/templates/index.html)). Stale: `StaleIntervals = 3` and `sweepStale` ([poller.go:30](../lib/monitor/web/poller.go), [poller.go:233-249](../lib/monitor/web/poller.go)), colour classes at [format.go:60-71](../lib/monitor/web/format.go). History: last 60 samples ([poller.go:26](../lib/monitor/web/poller.go), [poller.go:222-225](../lib/monitor/web/poller.go)) rendered newest-first ([router.html:42-56](../lib/monitor/web/templates/router.html)). Behavioural tests: [poller_test.go:81-296](../lib/monitor/web/poller_test.go), [server_test.go:56-137](../lib/monitor/web/server_test.go). |
| **Note** | Three real defects, all confirmed live: (a) **go-i2p routers are unobservable**; (b) **i2pd serves no tunnel in/out and no fast/high-capacity peers**, so four of the required fields are permanently empty for every observable router; (c) **the amber `stale` state is effectively unreachable** in live operation. |

**(a) go-i2p: never observable.** On a live 4-router testnet (3x i2pd + 1x go-i2p) started
through the real CLI, `router-goi2p-4` stayed `down` for its entire life:

```text
router-goi2p-4  down  hist=0  err=i2pcontrol: request to router router-goi2p-4 failed:
                        Post "http://172.28.0.5:7650/": dial tcp 172.28.0.5:7650: connect: connection refused
```

Nothing ever listens on 7650 inside that container (`netstat -ltn` -> only the embedded DNS
resolver `127.0.0.11` and the NTCP2 port). Its own captured log shows the config is read and
validated, then the process never reaches serving:

```text
level=warning msg=I2PControl is bound to a non-localhost address over HTTP ... address=0.0.0.0:7650 at=validateI2PControlConfig
level=debug   msg=I2PControl configuration validated successfully at=validateI2PControlConfig
level=warning msg=initial reseed incomplete, retrying after backoff ... current_size=3 threshold=40 attempt=4
```

upstream go-i2p enforces a 40-RouterInfo bootstrap floor even though this repo sets
`low-peer-threshold: 1` ([config.go:101-105](../lib/go-i2p/config.go)), so with a small
testnet it loops in bootstrap and the I2PControl server is never started. The dashboard
reports this *faithfully* ("down", with the error) — but an operator gets no status,
uptime, version, tunnels, peers, bandwidth or history for the router under test, and there
is no fallback (log-derived or webconsole-derived) and no state distinguishing "control
endpoint not yet serving" from "router dead". For a tool whose stated purpose is debugging
go-i2p's netDb ([README.md:3-7](../README.md)) this is a capability-1 failure, not a
cosmetic one. `docs/monitoring-verification.md` limitation 1 acknowledges the upstream
floor; the missing piece is the dashboard's lack of any fallback.

**(b) i2pd: four fields permanently empty.** Live `/api/status` sample for i2pd 2.56.0:

```json
{"tunnels_inbound": null, "tunnels_outbound": null, "fast_peers": null,
 "high_capacity_peers": null, "tunnels_participating": 0, "known_peers": 1, "active_peers": 0}
```

i2pd does not serve those properties over I2PControl (documented at
[monitoring-discovery.md:97](./monitoring-discovery.md)) and no alternate source is wired.
So "tunnels" and two of four peer columns are always empty for every router that *can* be
polled. Note the inversion: the only implementation that serves tunnel counts (go-i2p) is
the one that cannot be polled.

**(c) `stale` is implemented but not reachable in practice.** `sweepStale` only promotes
`up -> stale`, and any failed poll unconditionally assigns `down`
([poller.go:213-217](../lib/monitor/web/poller.go)), overwriting a stale state before it can
be observed. A router can only go amber if its own poll goroutine is still in flight and
gets skipped ([poller.go:189-196](../lib/monitor/web/poller.go)). Live proof: `docker pause
router-i2pd-1` (socket accepts, never answers) produced `down | request timed out` within one
interval and `stale: 0` in totals for the whole run; `stale` never appeared in any live
`totals` across three runs. `TestSweepStaleMarksOldSamplesAmber`
([poller_test.go:266-286](../lib/monitor/web/poller_test.go)) calls `sweepStale` directly
rather than driving a real poll cycle, so the unit test passes without the state being
reachable end-to-end. **Down: verified live. Stale: Unverified in live operation.**

### 1.2 Observe the network as a whole

| | |
|---|---|
| **Status** | **Partial** |
| **Evidence** | `Totals()` returns routers/up/down/stale/pending ([poller.go:346-363](../lib/monitor/web/poller.go)); rendered as the summary header ([index.html:26-32](../lib/monitor/web/templates/index.html)) and serialized ([server.go:328-330](../lib/monitor/web/server.go)); test asserts the exact key set ([server_test.go:138-188](../lib/monitor/web/server_test.go)). Live: `{'routers': 4, 'up': 3, 'down': 1, 'stale': 0, 'pending': 0}`, correct against `docker ps`. |
| **Note** | Only *health counts* are aggregated. There are no network totals for peers, tunnels or bandwidth, and no network-level verdict — even though `Snapshot()` ([poller.go:316-325](../lib/monitor/web/poller.go)) already hands every caller the full per-router set. Live `/api/status` top-level keys are exactly `generated_at, refresh_seconds, totals, routers`; `totals` is exactly the five counts. An operator reading "is the network healthy overall?" must sum rows by hand. |

### 1.3 Graph network state over time

| | |
|---|---|
| **Status** | **Missing** |
| **Evidence** | No charting exists anywhere. Repo-wide search of `*.go` and `*.tmpl` (the template extension at the audited revision; those files are `*.html` now) for `topolog|chart|graph|sparkline|svg|polyline|canvas` returns exactly one hit, an unrelated comment ([dashboard.go:3](../lib/monitor/dashboard/dashboard.go)). Live proof against the running server: `curl /` and `curl /router/router-i2pd-1` piped through `grep -ci "svg|canvas|polyline|<img|chart|<script"` -> **0** and **0**; served index is 3216 bytes of pure table markup; templates contain no external assets and no JavaScript. Only two templates exist ([index.html](../lib/monitor/web/templates/index.html), [router.html](../lib/monitor/web/templates/router.html)). |
| **Note** | The *underlying data is real, not hardcoded or empty* — which is the good news — but a table is not a graph. Live history for one router: 23 samples with 23 distinct `collected_at` values at 5 s spacing and monotonically increasing `uptime_ms` (14000 -> 256000), rendered as rows (`13:53:36`, `13:53:31`, `13:53:26`, ...). Requirement names peer count, tunnel count and bandwidth trends; none are plotted. Secondary problem: 60 samples x 5 s = a **5-minute window only**, with no persistence ([monitoring-verification.md limitation 5](./monitoring-verification.md)), so even after charts are added no meaningful trend is visible beyond five minutes. |

### 1.4 See the edges (topology view)

| | |
|---|---|
| **Status** | **Missing** |
| **Evidence** | Only four routes are registered ([server.go:68-71](../lib/monitor/web/server.go)); live probe: `/` 200, `/healthz` 200, `/api/status` 200, `/router/router-i2pd-1` 200, and `/topology` **404**, `/api/topology` **404**, `/graph` **404**, `/topology.json` **404**, `/api/graph` **404**. No edge model exists: `Sample` ([client.go:103-121](../lib/monitor/i2pcontrol/client.go)) carries only scalar counters, and `RequestedProperties` never asks for a peer list or netDb contents. |
| **Note** | Both sub-requirements ("graph update time", "edge observed vs inferred") have nothing to evaluate. The requirement's escape hatch — "derived from I2PControl **or router data**" — is satisfiable and the repo already does it *outside* the dashboard: the CLI `peers` command ([main.go:563-643](../main.go)) parses i2pd's webconsole transport table via `i2pdSessionRe` ([main.go:558](../main.go)) plus `netstat` ESTABLISHED, mapping container IP -> router name. Verified live on a 2-router net with real edges: `SSU2 session -> router-i2pd-2 (172.28.0.3:4567) sent=1161 recv=175 bytes` and the reciprocal edge from `router-i2pd-2`. `verify_netdb` ([main.go:647-690](../main.go)) reports per-router RouterInfo counts. So an observed-edge topology is derivable from data the repo already reads; it simply is not surfaced in the dashboard, and there is no HTML topology view at all. |

### 1.5 Read it easily

| | |
|---|---|
| **Status** | **Partial** |
| **Evidence** | Good: no raw JSON required — human-readable HTML tables, with JSON only at `/api/status` and merely linked from the footer ([index.html:61](../lib/monitor/web/templates/index.html)); consistent units via `formatBytesPerSec` (B/s, KiB/MiB/s — [format.go:9-22](../lib/monitor/web/format.go)), `formatDuration` (`2m 10s` — [format.go:24-40](../lib/monitor/web/format.go)), `formatAgo` (`just now`, `5s ago` — [format.go:42-54](../lib/monitor/web/format.go)); missing values render `—` via `orDash`/`numI64` ([format.go:73-96](../lib/monitor/web/format.go)); sensible sorting by router name in both HTML and JSON ([poller.go:323](../lib/monitor/web/poller.go), [server.go:331](../lib/monitor/web/server.go)) — verified live; labelled columns and a colour-coded header; empty-roster guidance ([index.html:57](../lib/monitor/web/templates/index.html)); 5 s auto-refresh. |
| **Note** | Readability breaks exactly where data is missing. Live served HTML for the down go-i2p router: `<td>//</td>`, `<td>/</td>`, `<td> / </td>`, and `<dt>Version</dt><dd></dd>` — because `rowFrom`'s nil-sample branch sets **only** `LastSeen` and leaves every other field the empty string ([server.go:144-174](../lib/monitor/web/server.go)), while the templates concatenate the composites unconditionally. Also: the detail page hardcodes `content="5"` instead of using the configured interval ([router.html:5](../lib/monitor/web/templates/router.html) vs [server.go:235](../lib/monitor/web/server.go)); `indexView.Now` is computed and never used ([server.go:213](../lib/monitor/web/server.go), [server.go:236](../lib/monitor/web/server.go)), so the aggregate snapshot has no absolute "as of" stamp on the page; no sortable columns. |

---

## 2. Supporting requirements

| Requirement | Status | Evidence | Note |
|---|---|---|---|
| Router discovery from existing config/topology, not hardcoded | **Implemented** | In-memory list snapshot `cliRouters()` ([main.go:66-80](../main.go)); Docker discovery `RoutersFromDocker` ([dashboard.go:225-268](../lib/monitor/dashboard/dashboard.go)); per-implementation target building `Source.Targets` ([dashboard.go:133-156](../lib/monitor/dashboard/dashboard.go)); re-diffed every cycle by `syncTargets` ([poller.go:251-313](../lib/monitor/web/poller.go)). | Verified live in **both** modes: in-process (`start` + `add ...`) and standalone `./bin/go-i2p-testnet -dashboard -dashboard-addr 127.0.0.1:8899`, which discovered all 4 running containers by name prefix and IP with no router list in memory. No router name/IP/count appears as a literal in non-test monitor code (only in doc comments). Gap: `RoutersFromDocker` has **no unit test** (0 hits in `*_test.go`) — live-verified only. |
| Concurrent polling, ~5 s per-router timeout, one router cannot block others | **Implemented** | `web.NewPoller(5*time.Second, 5*time.Second, src.Targets)` ([main.go:98](../main.go)); per-router goroutine ([poller.go:200-231](../lib/monitor/web/poller.go)); skip-if-still-running ([poller.go:189-196](../lib/monitor/web/poller.go)); `DefaultTimeout = 5s` ([client.go:23](../lib/monitor/i2pcontrol/client.go)) applied to dial, TLS handshake, response header and client ([client.go:164-171](../lib/monitor/i2pcontrol/client.go)) and per-poll context ([client.go:424-433](../lib/monitor/i2pcontrol/client.go)); panic isolation ([client.go:425-430](../lib/monitor/i2pcontrol/client.go)). | **Strongest part of the implementation; verified live.** With `router-i2pd-1` paused, `router-i2pd-2` kept a clean 5 s cadence (`generated_at` advancing exactly 5 s six times in a row, history growing 31->36) and stayed `up`. Test: `TestPollerSlowRouterDoesNotBlockOthers` ([poller_test.go:132-161](../lib/monitor/web/poller_test.go)). |
| Routes `/`, `/router/{name}`, `/api/status`, `/healthz` | **Implemented** | Registered at [server.go:68-71](../lib/monitor/web/server.go); handlers at [server.go:106](../lib/monitor/web/server.go), [server.go:217](../lib/monitor/web/server.go), [server.go:243](../lib/monitor/web/server.go), [server.go:325](../lib/monitor/web/server.go). | All four returned 200 in the live run; unknown paths 404 ([server_test.go:189-198](../lib/monitor/web/server_test.go)); `/router/<unknown>` 404; detail page verified live. |
| **Plus a topology route** | **Missing** | No handler registered; `/topology`, `/api/topology`, `/graph`, `/topology.json`, `/api/graph` all 404 live. | Explicit requirement, unmet. See 1.4. |
| Last 60 samples per router in memory | **Implemented** | `HistorySamples = 60` ([poller.go:26](../lib/monitor/web/poller.go)); prepend + truncate ([poller.go:222-225](../lib/monitor/web/poller.go)); copied out for readers ([poller.go:336-344](../lib/monitor/web/poller.go)). | Verified live: three routers' histories grew monotonically and **stopped at exactly 60**. Test: `TestPollerHistoryCapped` ([poller_test.go:243-265](../lib/monitor/web/poller_test.go)). Caveat: in-memory only, so 60 x 5 s = 5 minutes of visible history; a restart clears it. |
| Bind to 127.0.0.1 by default | **Implemented** | Default `127.0.0.1:8765` in both the server ([server.go:41-44](../lib/monitor/web/server.go)) and the flag ([main.go:734-735](../main.go)). | Verified live: `ss -ltnp` -> `127.0.0.1:8765 users:(("go-i2p-testnet",pid=1602))`. No port published to the router containers either. |
| Passwords only from env / gitignored files, never logged | **Partial** | `ResolvePassword`: env -> `logs/.i2pcontrol-password` -> generated 0600 file ([dashboard.go:166-195](../lib/monitor/dashboard/dashboard.go)); well-known `itoopie` refused without opt-in ([dashboard.go:197-205](../lib/monitor/dashboard/dashboard.go)); file gitignored (`.gitignore:2,6`, confirmed by `git check-ignore -v`); excluded from all rendering and JSON ([server.go:264-320](../lib/monitor/web/server.go)); RPC errors redacted ([client.go:566-575](../lib/monitor/i2pcontrol/client.go)); captured router logs scrubbed ([logs.go:86-96](../lib/monitor/logs.go), applied at [logs.go:205](../lib/monitor/logs.go), wired at [main.go:787](../main.go)); tests [dashboard_test.go:13-88](../lib/monitor/dashboard/dashboard_test.go), [logs_scrub_test.go:8-37](../lib/monitor/logs_scrub_test.go), [client_test.go:279-294](../lib/monitor/i2pcontrol/client_test.go). | Everything the **tool itself** writes is clean — verified live: 0 matches for the session password in `/`, `/api/status`, `logs/*.log`, and `docker inspect` env/args. **But the requirement is not met at the container boundary**: i2pd runs at `loglevel=debug` ([lib/i2pd/config.go:147](../lib/i2pd/config.go)) and echoes the credential to its own stdout. Reproduced exactly with a probe password: `docker logs router-i2pd-1 | grep -c <PASSWORD>` -> **1**, while the captured file shows `Authenticate API=1 ******`. An operator who inspects `docker logs` sees the cleartext I2PControl password. Fix: set i2pd `loglevel` to `warn`/`info`, or document the exposure as operator-visible rather than claiming "never logged". |
| Standard library only | **Implemented (as scoped)** | The dashboard added **zero** dependencies: `git diff 4f0648e..HEAD -- go.mod` is empty. `lib/monitor/web` and `lib/monitor/i2pcontrol` import stdlib only (verified per-file: `net/http`, `html/template`, `embed`, `crypto/tls`, ...); no web framework, no database, no CDN, no client-side JS ([index.html](../lib/monitor/web/templates/index.html), [router.html](../lib/monitor/web/templates/router.html)). | Nuance, stated so it is not mistaken for a claim: `lib/monitor/dashboard` and `lib/monitor/logs.go` do import the Docker SDK and logrus, because router discovery and cert retrieval require the Docker API. The README's narrower claim — "stdlib only: no external web server, database, CDN, or frontend framework" ([README.md:139-140](../README.md)) — is accurate. |
| Docs in `docs/monitoring-discovery.md`, `docs/monitoring-verification.md`, README | **Implemented (existence) / inaccurate (content)** | All three exist and are substantive: 167, 184 and 190 lines. README "Status dashboard" section ([README.md:136-179](../README.md)) documents routes, interval, states, pinning, password order and standalone mode. | Existence is fine; **content carries at least ten claims contradicted by the code or by a live run** — see section 4. |

---

## 3. Prioritized gap list

### Blocking

1. **No topology/edges view (1.4).** No route, no edge model, no update timestamp, no
   observed-vs-inferred distinction. Explicitly required. The raw material already exists in
   this repo (`i2pdSessionRe` + webconsole transports + `netstat`, all live-verified to yield
   real named edges), so this is integration work, not research.
2. **No charts of peer count, tunnel count or bandwidth over time (1.3).** Explicitly
   required; zero chart markup in served HTML (verified live). History data is real, so the
   renderer is the missing piece.
3. **go-i2p routers cannot be observed at all (1.1a).** The dashboard has no fallback and no
   distinct "control endpoint not yet serving" state, so the router the testnet exists to
   debug shows nothing but `down` — and the README asserts the opposite.

### Important

4. **i2pd serves no tunnel in/out and no fast/high-capacity peers (1.1b)** — four required
   fields permanently empty for every observable router; no alternate source
   (webconsole/netDb) is wired in.
5. **Cleartext I2PControl password in `docker logs` (section 2)** — reproduced live; the tool's
   own outputs are clean, but the operator-facing raw log path is not.
6. **No aggregate network totals beyond health counts (1.2)** — no total peers/tunnels/
   bandwidth, no network verdict.
7. **5-minute history window, no persistence (1.3, section 2)** — 60 x 5 s caps any trend view
   even after charts exist; a restart clears everything.
8. **`RoutersFromDocker` is untested** — the standalone discovery path is exercised only by
   manual/live runs.

### Minor

9. **Down routers render `//`, `/`, ` / ` and empty `<dd>` instead of `—` (1.5)** —
   `rowFrom`'s nil branch sets only `LastSeen` ([server.go:171-173](../lib/monitor/web/server.go)).
10. **Detail page hardcodes `refresh content="5"`** instead of `PollInterval`
    ([router.html:5](../lib/monitor/web/templates/router.html)).
11. **`stale` amber is unreachable in live operation** (1.1c) — a failed poll overwrites it;
    the unit test bypasses the real cycle.
12. **Dead code**: the eight-function template `FuncMap`
    ([server.go:47-56](../lib/monitor/web/server.go)) is used 0 times by the templates;
    `indexView.Now` is set and never rendered ([server.go:213](../lib/monitor/web/server.go),
    [server.go:236](../lib/monitor/web/server.go));
    `TestI2PDConfigRedaction` exercises a test-local `mapLines`
    ([dashboard_test.go:130-145](../lib/monitor/dashboard/dashboard_test.go)) that duplicates the
    production `redactConfigPassword` ([lib/i2pd/config.go:292](../lib/i2pd/config.go)) instead of
    testing it.
13. **No sortable columns and no absolute "as of" timestamp rendered on the index page.**
14. **`sync` restarts every i2pd router** ([main.go:533-549](../main.go)), so the dashboard
    shows transient red flips during a sync. By design and documented, but worth labelling in
    the UI.

---

## 4. Documentation claims that do not match the code

| # | Claim | Where | Reality (verified) |
|---|---|---|---|
| 1 | "every router now serves I2PControl (JSON-RPC on port 7650)" | [README.md:158](../README.md) | False for go-i2p: nothing listens on 7650 in `router-goi2p-4` (checked with `netstat -ltn` inside the container); it stays in bootstrap retry (`threshold=40`) and the dashboard shows `connection refused` forever. |
| 2 | "An explicit `-insecure-tls-skip-verify` flag exists" | [monitoring-discovery.md:146](./monitoring-discovery.md) | The flag is `-insecure-tls` ([main.go:738](../main.go)); the README uses the correct name ([README.md:162](../README.md)). The documented name does not exist and would abort flag parsing. |
| 3 | i2pd is "currently **2.60.0**" from Alpine 3.22, and the property table was probed against 2.60.0 | [monitoring-discovery.md:4-5,44,74,84,92](./monitoring-discovery.md) | The image in this environment resolves to **2.56.0** (`docker exec router-i2pd-1 i2pd --version` -> `i2pd version 2.56.0`). [README.md:107](../README.md) and [monitoring-verification.md:59](./monitoring-verification.md) correctly say 2.56 — the discovery doc contradicts both, and its compatibility table is for a version that is not what runs here. |
| 4 | "The same flags are passed on the command line so they hold even if the config file isn't picked up" (about go-i2p I2PControl) | [monitoring-discovery.md:126](./monitoring-discovery.md) | The container command passes only `--bootstrap.type local`, `--bootstrap.low-peer-threshold 1` and `--netdb.strict-routerinfo-network-validation=false` ([lib/go-i2p/container.go:69-79](../lib/go-i2p/container.go)) — no i2pcontrol flags. (Good: a password in argv would be visible in `ps`/`docker inspect`. But the described fallback does not exist.) |
| 5 | "Never logged or displayed" / "Passwords and auth tokens are never logged or rendered" | [monitoring-discovery.md:137](./monitoring-discovery.md), [README.md:174](../README.md) | True for everything the tool writes/renders (verified: 0 matches in dashboard HTML, `/api/status`, `logs/*.log`, `docker inspect` env/args). False at the container boundary: `docker logs router-i2pd-1` contained the cleartext probe password (1 match) because i2pd logs `I2PControl: Authenticate API=1 <password>` at debug level. [monitoring-verification.md limitation 4](./monitoring-verification.md) admits this. |
| 6 | "After `sync`: all routers `up`; peer counts became non-zero" | [monitoring-verification.md:88-90](./monitoring-verification.md) | Not reproducible at that scale: on a live 4-router run the go-i2p router remained `down` after `sync` (bootstrap floor). The same document's limitation 1 contradicts this section 3 claim. `known_peers` did become non-zero (1) for i2pd routers. |
| 7 | Index page "shows ... tunnels, peers, bandwidth" for each router | [README.md:145](../README.md) | Incomplete: for i2pd, `tunnels_inbound`/`tunnels_outbound`/`fast_peers`/`high_capacity_peers` are always `null` (live JSON), rendered `—/—/0`; not caveated. |
| 8 | Test comment: "guards the config generators used at `add`: the I2PControl section must exist and the redaction helper must work on their output" | [dashboard_test.go:116-127](../lib/monitor/dashboard/dashboard_test.go) | The test never calls a config generator or `redactConfigPassword`; it re-implements the logic locally as `mapLines` and asserts on that. The production redaction is untested. |
| 9 | "53-router testnet ... totals: {'routers': 53, 'up': 53 ...}" and "Polling 53 routers ... never serialized" | [monitoring-verification.md section 4](./monitoring-verification.md) | **Unverified here** — reproducing it needs about an hour of container churn. The concurrency *mechanism* and the cadence claim were independently verified at 4-router scale (and with a paused router), so this is a scale number taken on trust, not a doubted claim. |
| 10 | "history is in memory ... last 60 samples" implies a usable trend view | [README.md:138-149](../README.md) | Accurate as written, but 60 x 5 s = 5 minutes; no doc states the effective window, which matters for the graphs requirement. |

---

## 5. What was verified live, and what was not

**Environment.** Linux, Docker 24.0.7, Go 1.26.8. Images `i2pd-node` (alpine 3.22, i2pd
2.56.0) and `go-i2p-node` already present. The testnet was driven through the real CLI binary
(`./bin/go-i2p-testnet`) with commands fed over stdin; a fresh random
`GO_I2P_TESTNET_I2PCONTROL_PASSWORD` was used per run. All containers, network and dashboard
processes were torn down afterwards (`docker ps -a` router count: 0; network count: 0; no
stray processes). No source file was modified; the only write is this report.

**Verified live**

- Build, vet, gofmt and `go test -count=1 ./lib/monitor/...` all green (fresh, not cached).
- Dashboard auto-starts with `start`; prints its URL; binds `127.0.0.1:8765` (confirmed by `ss -ltnp`).
- All four routers appeared in the roster with correct names, implementations and URLs
  (`https://172.28.0.{2,3,4}:7650/` pinned for i2pd, `http://172.28.0.5:7650/` for go-i2p).
- `/`, `/router/<name>`, `/api/status`, `/healthz` -> 200; `/topology`, `/api/topology`,
  `/graph`, `/topology.json`, `/api/graph` -> 404.
- **Zero chart/graph/JS markup** in the served HTML of both pages; index is 3216 bytes of table.
- History fills from real samples over time (23 samples, 23 distinct 5 s-spaced timestamps,
  `uptime_ms` 14000 -> 256000) and **caps at exactly 60**.
- `docker stop router-i2pd-3` -> `down` with `i2pcontrol: request timed out` within about 10 s
  while the other routers stayed `up`; `docker start` -> `up` with `ver 2.56.0` within about
  5 s. Totals tracked correctly throughout (`up:2 down:2` -> `up:3 down:1`). (An earlier
  attempt appeared to do nothing only because a pending `sync` was restarting every i2pd
  router — see gap 14.)
- **Concurrency**: with `router-i2pd-1` paused, `router-i2pd-2` kept an exact 5 s cadence
  (`generated_at` +5 s over six consecutive polls) and its history kept growing — one blocked
  router did not delay the others.
- `docker pause` produced `down` (timeout), never `stale`; `stale: 0` in every live `totals`.
- go-i2p never observable: `down` + `connection refused`, 0 samples, 0 history, all fields
  blank, for its entire life; nothing listening on 7650 inside the container.
- i2pd null fields (`tunnels_inbound`, `tunnels_outbound`, `fast_peers`, `high_capacity_peers`).
- Standalone discovery mode (`-dashboard -dashboard-addr 127.0.0.1:8899`) found all 4 running
  containers from the Docker topology with no in-memory router list, and served identical data.
- Net status rendered as a name (`FIREWALLED`), units rendered (`0 B/s`, `2m 10s`, `just now`),
  rows sorted by name, down rows red (`class="down"`), error line shown under the row.
- Password absent from dashboard HTML, `/api/status`, `logs/*.log` and `docker inspect`
  env/args; captured log line shows `******`; **present in cleartext in `docker logs`** (probe).
- CLI `peers` produced real named edges (`SSU2 session -> router-i2pd-2 (172.28.0.3:4567)` and
  the reciprocal), proving topology data is derivable from router data; `verify_netdb` reported
  per-router RouterInfo counts matching the roster.

**Not verified (treat as Unverified)**

- The 53-router scale run described in `monitoring-verification.md` section 4.
- The `stale`/amber state in live operation (implemented and unit-tested, but not reachable
  end-to-end — see 1.1c).
- TLS certificate-pinning **mismatch** and the `-insecure-tls` warning path at runtime: pinned
  HTTPS polling of live i2pd routers succeeded (which exercises the match path), but mismatch
  and opt-out rest on unit tests ([pinning_test.go:71-138](../lib/monitor/i2pcontrol/pinning_test.go)) and code reading.
- Live `logs all` streaming and its scrubbing on the terminal (scrubbing verified via the
  captured files and [logs_scrub_test.go](../lib/monitor/logs_scrub_test.go)).
- go-i2p ever becoming observable (would require 40 or more RouterInfos in the shared netDb).
- Graphs and topology: nothing to verify — both absent, proven by 404s and by zero chart
  markup in live HTML.

---

## 6. Scorecard

| Requirement | Status |
|---|---|
| 1. Observe each router individually | **Partial** — i2pd works; go-i2p unobservable; 4 fields permanently empty; `stale` not reachable live |
| 2. Observe the network as a whole | **Partial** — health counts only, no aggregate totals |
| 3. Graph network state over time | **Missing** — table of real samples only, zero chart markup (verified live) |
| 4. See the edges (topology) | **Missing** — no route, no edge model; edges provably derivable from router data |
| 5. Read it easily | **Partial** — good units/labels/sorting, no raw JSON; broken empty-value rendering and a hardcoded refresh interval |
| Discovery from config/topology | **Implemented** (verified live in both modes; Docker path untested) |
| Concurrent polling, ~5 s timeout | **Implemented** (verified live with a paused router) |
| Routes `/`, `/router/{name}`, `/api/status`, `/healthz` | **Implemented** (verified live) |
| Topology route | **Missing** (verified 404 live) |
| Last 60 samples in memory | **Implemented** (verified live: capped at exactly 60) |
| Bind 127.0.0.1 by default | **Implemented** (verified live via `ss`) |
| Passwords from env/gitignored file, never logged | **Partial** — tool outputs clean (verified); cleartext in `docker logs` (reproduced) |
| Standard library only | **Implemented** as scoped — dashboard added no dependencies; Docker SDK used for discovery |
| Docs (discovery, verification, README) | **Implemented** in existence; **10 claims contradicted by code or live runs** (section 4) |

**Bottom line for an operator today:** you can watch each reachable router's current state,
scroll a 5-minute table of its samples, see red/last-error when one dies, and watch it come
back — all from a loopback-only page that adds no dependencies and leaks no credentials in
its own output. You cannot see trends as graphs, you cannot see who is connected to whom, you
cannot read network-wide totals, and you cannot see the go-i2p router at all. Two of the five
operator capabilities are missing outright, so the repo is **not feature-complete**.

---

## 7. Status after implementation (this section supersedes nothing above; it records what changed)

The verdict and every table above describe commit `f4d8710` and still stand as a
description of that revision. The gaps in section 3 were subsequently implemented;
this section states what is now true, what was verified live, and what the audit
found **cannot** be fixed in this repo.

Verified on two fresh testnets built from the current tree: 4 routers (3 i2pd + 1 go-i2p,
2026-11-10 19:24-19:35 EST) and 5 routers (4 i2pd + 1 go-i2p, 23:33-23:44 EST). Edge and
peer counts vary with the network, so the notes below give one representative observation
per claim.

| Gap (section 3) | Status now | Evidence | Verified live |
|---|---|---|---|
| 1. No topology/edges view | **Implemented** | `lib/monitor/topology/topology.go` (model, `Build`, observed/inferred kinds, match=hash/ip/netdb/external); `lib/monitor/dashboard/collector.go` (sessions from i2pd's transports page, peer hashes from each netDb, own hash from `router.info`); `lib/monitor/dashboard/topology_service.go` (background refresh, `TopologyRefresh = 10s`, `collectionTimeout = 8s`); routes at [server.go:175](../lib/monitor/web/server.go) `/topology` and `/api/topology`; SVG + edge table in `lib/monitor/web/topology_view.go` + `templates/topology.html` | Yes, at both scales. 4-router run: 4 nodes, 9 edges — 6 observed (all `match=hash`, transports NTCP2/SSU2, with per-edge byte counters) and 3 inferred (netDb). 5-router run: 12 observed / 4 inferred. Page prints `Graph generated 2026-11-10 23:37:00 EST` and a summary line counting observed, inferred and hash-matched edges. Nothing about adjacency is hardcoded: every edge comes from a router's own transport table or its own netDb. |
| 2. No charts | **Implemented** | `lib/monitor/web/chart.go` (stdlib SVG renderer, `niceScale` integer axes, gaps drawn as dots, explicit escaping), `lib/monitor/web/charts.go` (`routerCharts`, `networkCharts` built from real history), rendered in `templates/index.html` + `templates/router.html` | Yes: the index shows 3 network charts (peers / tunnels / bandwidth), each with its own series polylines and legend (`known`, `active`, ...). Peer and tunnel axes are integral (`0,2,4,6` and `0,50,100,150` observed), the bandwidth axis is in `KiB/s` (`0 B/s, 9.8 KiB/s, 19.5 KiB/s`), and x labels are wall-clock (`19:24:28 / 19:25:58 / 19:26:48`). Each router page shows its own 3 charts. |
| 3. go-i2p unobservable | **Partially fixed — root cause is upstream** | `(go-i2p) router is bootstrapping: it refuses to start its control interface until it has the minimum number of peers (the log names that floor)` — `LogManager.Diagnose` ([logs.go:355](../lib/monitor/logs.go)) reads the router's own log tail and classifies, never echoing log content; failure class `not-serving` from `classifyFailure` ([poller.go:229](../lib/monitor/web/poller.go)) | Yes: `router-goi2p-4` returns `state=down`, `failure_reason=not-serving` and the router page says why instead of showing a bare red cell. **It also now appears in the topology** with netDb-derived edges to every router in the roster (3 at 4-router scale, 4 at 5-router scale) — derived from its own netDb listing, not from a name list. The control interface itself still does not listen: `--bootstrap.low-peer-threshold 1` and `--bootstrap.minimum-reseed-peers 1` were both probed and neither changes `threshold=40`, so this is not configurable from here. |
| 4. i2pd serves no tunnels in/out, fast/high-capacity peers | **Cannot be implemented — now labelled honestly** | `notServed` map ([charts.go:19](../lib/monitor/web/charts.go)) + `notServedText`; API field `not_served_by_implementation` | Yes: i2pd 2.56 answers those four properties with `{"result":{}}` (probed directly, so no polling cadence and no alternate I2PControl source can produce a value). The detail page now reads `not served by i2pd` in place of four dashes, the compact table reads `n/a`, and the page explains that this means the property is not exposed at all — not a missing measurement and not a zero. |
| 5. Cleartext password in `docker logs` | **Fixed at the source** | routers default to `loglevel = info` because i2pd prints `I2PControl: Authenticate API=1 <password>` at debug ([config.go:139-164](../lib/i2pd/config.go)); opt-in `TESTNET_I2PD_LOGLEVEL=debug` prints a warning at startup ([main.go](../main.go)); `RedactConfigPassword` exported and now actually tested ([config_test.go](../lib/i2pd/config_test.go)); `./logs/` scrubbing retained as defence in depth | Yes: `grep -Fc "$password"` over `docker logs` for all 3 i2pd containers, all 4 `logs/*.log` files, `/`, `/topology`, `/api/status`, `/api/topology`, and `docker inspect` env+args → **0 matches everywhere** (was 1 in `docker logs` at the audited revision). |
| 6. No aggregate network totals | **Implemented** | `NetworkTotals` + `recordNetworkHistory` / `NetworkHistory` / `aggregateLocked` ([poller.go](../lib/monitor/web/poller.go)); aggregation ignores samples older than `StaleIntervals`, so a stale router stops contributing | Yes: at 4 routers `network {routers 4, up 3, known_peers 9, active_peers 6, tunnels_participating 378}`. Pausing a router dropped the sum (`known_peers 6 / active_peers 4 / tunnels_participating 245`) and unpausing restored it. At 5 routers the same behaviour held: `known_peers 10 / active_peers 6 / tunnels_participating 310` with the router up, falling while it was stale. |
| 7. 5-minute window, no persistence | **Unchanged** | History is still capped at 60 in-memory samples; the effective window is now stated on the page (`window()` in [charts.go](../lib/monitor/web/charts.go)) and as `history_max_samples` in the API | Not attempted: persistence would need storage outside the stdlib-only scope. The charts say `5 minute window` rather than letting the x axis be the only clue. |
| 8. `RoutersFromDocker` untested | **Implemented** | `SelectRouters` + `RouterImpl` extracted for testability; `discovery_test.go` | Yes (unit): name→implementation mapping, container-id/IP propagation, and the standalone path are covered without a daemon. |
| 9. Down routers render `//`, `/`, ` / `, empty `<dd>` | **Fixed** | `rowFrom(st, compact)` ([server.go](../lib/monitor/web/server.go)) fills every cell in the nil branch | Yes: `grep` for `<td>\s*[/ ]+\s*</td>` in the served index and router pages → 0 matches; a down router renders `—` (or `n/a`). |
| 10. Hardcoded `refresh content="5"` | **Fixed** | `templates/base.html` uses `{{.RefreshSecs}}` from `PollInterval` | Yes: `content="5"` appears because the configured interval *is* 5s; it now tracks the config. |
| 11. `stale` unreachable | **Fixed** | `applyUnhealthy` keeps amber while the last sample is within `StaleIntervals × interval` ([poller.go](../lib/monitor/web/poller.go)) | Yes, at both scales. 5-router run: `docker pause router-i2pd-2` → at 15s `totals {routers 5, up 3, down 1, stale 1}` with `state=stale failure_reason=timeout consecutive_failures=2`; at 35s `stale 0, down 2` with `consecutive_failures=4`; `docker unpause` → back to `up`. |
| 12. Dead code | **Removed** | `orDash`, `numF64`, `numPtr`, `Adjacency`, `Chart.Subtitle`, the entire unused template `FuncMap`, and the fake `mapLines` redaction test | Yes: `gofmt`/`go vet` clean; the redaction test now calls the production `RedactConfigPassword`. |
| 13. No sortable columns, no absolute "as of" | **Implemented** | server-side sort by `?sort=&dir=` gated by `durationShape`/`parseDurationDisplay` so `2m 10s` cannot sort as 2; nav snapshot stamp + footer `snapshot 2026-11-10 19:26:49 EST` ([server.go:633](../lib/monitor/web/server.go)) | Yes: 9 sortable columns (name, impl, state, version, uptime, tunnels, peers, bw, seen). Text such as `not served by i2pd` sorts as missing, never as a fake 0. |
| 14. `sync` restarts every i2pd router | **Unchanged, now labelled** | transient red flips during `sync` are expected; the state machine distinguishes `not-serving`/`unreachable`/`timeout` so a restart is visibly different from a crash | Yes: observed directly in a run log — `Restarted router-i2pd-1 to load synced netDb` (and -2, -4, -5). Those restarts land in the `not-serving`/`timeout` buckets rather than looking like a crash, which is the point of the classification. |

### Also fixed while implementing (found by testing, not by the audit)

- A series whose only value sits between gaps drew nothing at all; gaps are now
  rendered as dots so a single reported value is still visible.
- A chart with exactly one sample said `no router reported this value`, which is
  false when a router did report one. The placeholder now distinguishes
  `only one sample collected so far — a trend needs two` from
  `no router reported this value`.
- `edgeSummary` was defined as `Summary.text()`; templates cannot call unexported
  methods, so the totals line silently rendered empty. Renamed to `Text()`.
- `html/template` escapes `&` inside an interpolated query string, so sort links
  came out as `?sort=x%26dir%3dasc`. The `&amp;` is now literal in the template.

### What is still true, stated plainly

1. **go-i2p remains unobservable over I2PControl.** Its peer floor is upstream and
   not configurable; the dashboard can now explain that and show the router's
   netDb-derived edges, but it cannot make the control endpoint listen.
2. **Four required fields are permanently absent for i2pd.** They are labelled, not
   filled. Anyone claiming to have "fixed" this by polling harder would be wrong.
3. **No persistence.** Restarting the dashboard clears history; the window is
   60 samples by design.
4. **Scale beyond 5 routers is unverified.** The 53-router figures in
   `docs/monitoring-verification.md` were taken on trust here, as section 4 item 9
   said; everything in this section was verified at 4- and 5-router scale.
5. **Topology edges stop at what routers actually report.** Sessions come from
   i2pd's own transport table and peer hashes from each router's own netDb, so an
   edge that no router records is not drawn — and a stopped router's edges are
   demoted to `inferred` with the warning `router-i2pd-4 is not running, so its
   sessions are not in this graph` (verified live after `docker stop`) rather than
   silently disappearing.

### Test and doc state after implementation

`go build ./...`, `go vet ./...`, `gofmt -l .` and `go test -count=1 ./...` are clean
(including `-race` for `./lib/monitor/...`). Coverage: `topology` 90.3%, `web` 83.9%.
New test files: `topology_test.go`, `chart_test.go`, `routes_test.go`, `sort_test.go`,
`collector_test.go`, `discovery_test.go`, `topology_service_test.go`, `config_test.go`.
`go.mod`/`go.sum` are untouched, so the dashboard remains stdlib-only; the Docker SDK
is used only for discovery and container-side collection, as before.

The 10 documentation contradictions in section 4 were corrected in
`README.md`, `docs/monitoring-discovery.md` and `docs/monitoring-verification.md`,
and template files were renamed `*.tmpl` → `*.html` (the extension in the links above
refers to the audited revision).
