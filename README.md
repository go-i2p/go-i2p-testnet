# go-i2p-testnet

Orchestrates an isolated I2P testnet of Docker containers so go-i2p's netDb
behavior can be debugged against other router implementations (i2pd) without
live-network noise. Routers run on an internal-only bridge network
(172.28.0.0/16) with netid 5, so nothing reaches or reseeds from the real I2P
network.

## Prerequisites

- Docker (daemon running)
- Go >= 1.23 (for the testnet tool itself; the go-i2p node image compiles
  go-i2p from upstream master inside Docker with its own Go toolchain)

## Quick start

```shell
make build
./bin/go-i2p-testnet
```

Then, inside the CLI:

```
build                # build the go-i2p-node and i2pd-node docker images (one-time)
start                # create the isolated network and shared volume
add i2pd_router      # repeat for as many i2pd nodes as you want (majority)
add i2pd_router
add i2pd_router
add goi2p_router     # the go-i2p node under test
logs router-i2pd-1   # confirm i2pd started cleanly (Enter stops streaming)
sync                 # wait ~10-15s after add so routers have written router.info
verify_netdb         # RouterInfo counts per router + shared volume
peers                # established sessions between routers (proof they talk)
logs all             # correlate all routers' logs side-by-side
stop                 # tear everything down
```

## Commands

| Command | Description |
|---|---|
| `start` / `stop` | Create/destroy the testnet (network, shared volume, all routers); `start` also boots the status dashboard |
| `dashboard` | Show the dashboard URL / start it if it was disabled |
| `status` | List router containers |
| `usage` | Memory/CPU usage per router |
| `build` / `rebuild` / `remove_images` | Manage the node docker images |
| `add goi2p_router\|i2pd_router` | Add a router node (config generated per node) |
| `sync` | Full netDb sync: publish every router's netDb to the shared volume, then distribute it to every router |
| `sync_shared` | Publish-only half of `sync` |
| `sync_netdb` | Distribute-only half of `sync` |
| `verify_netdb` | Show RouterInfo counts per router and in the shared volume |
| `peers` | Show established transport sessions between routers and per-router traffic counters |
| `logs all\|<router-name>` | Stream router logs live with colored per-router prefixes |
| `exit` | Cleanup and quit |

## Log monitoring

Every router's docker logs are captured automatically from the moment the node
is added:

- **Files**: written continuously to `./logs/<router-name>.log` (with docker
  timestamps), truncated at the start of each session. These survive
  `stop`/`exit`, so `grep`, `lnav`, or `diff` work for offline correlation.
- **Live**: `logs all` interleaves every router's output with a colored
  `[router-name]` prefix for side-by-side correlation; `logs router-i2pd-2`
  follows a single router. Press Enter to stop streaming.

## Verifying that routers talk to each other

`peers` is the ground truth. For each router it shows i2pd's own transport
session table (NTCP2 and SSU2, with per-session sent/received bytes), any
established inter-router TCP connections, and the container's interface byte
counters:

```
router-i2pd-1 (172.28.0.2) rx=708250 bytes, tx=673830 bytes
  SSU2 session -> router-i2pd-2 (172.28.0.3:4567) sent=411894 recv=465166 bytes
  SSU2 session -> router-i2pd-3 (172.28.0.4:4567) sent=273642 recv=248266 bytes
```

Notes on interpreting it:
- i2pd tends to prefer SSU2 (UDP) between peers, so an empty TCP view with
  active SSU2 sessions is normal and healthy.
- go-i2p has no webconsole, so its sessions only appear in the TCP view; its
  connection *attempts* show up in the other routers' logs (`logs all`).
- The i2pd webconsole is also available inside each container:
  `docker exec router-i2pd-1 wget -qO- "http://127.0.0.1:7070/?page=transports"`.

## netDb synchronization

Each router container mounts the shared volume at `/shared`. `sync` publishes
each router's RouterInfo/netDb entries into `/shared/netDb` (i2pd layout,
`r<X>/routerInfo-<hash>.dat`), then copies the merged netDb into every
router's own netDb directory (`/var/lib/i2pd/netDb` for i2pd,
`~/.go-i2p/config/netDb` for go-i2p).

`sync` finishes by restarting the i2pd routers: i2pd only reads its netDb from
disk at startup, so without the restart the synced RouterInfos would sit on
disk unloaded (`Routers: 1` in the webconsole). Router identity lives on the
per-router volume, so restarts don't invalidate published RouterInfos.

Notes:
- The go-i2p router currently only *consumes* RouterInfos — it does not
  publish its own router.info yet, so `verify_netdb`'s expected count equals
  the number of i2pd routers.
- i2pd >= 2.51 is required (the image uses alpine 3.22 / i2pd 2.56): older
  i2pd hardcodes reserved-IP-range rejection when parsing RouterInfos and
  deletes every peer on a private-subnet testnet regardless of
  `reservedrange = false`.

## go-i2p node behavior

The go-i2p node runs with `--bootstrap.type local`, which imports RouterInfos
from known netDb paths instead of clearnet reseed servers; the shared netDb is
bind-mounted at `/root/.i2pd` (one of those search paths) to make that work.
go-i2p exits when bootstrap finds no RouterInfos, so the container runs it in
a retry loop — **a go-i2p node crash-looping with "no valid netDb directory
found" is expected until the first `sync` publishes the i2pd RouterInfos**,
after which the next retry logs `bootstrap completed successfully`.

Known upstream go-i2p gaps this testnet currently surfaces (the point of the
exercise):
- go-i2p publishes a caps-only RouterInfo with `host=::` inside Docker (no
  UPnP/NAT-PMP available, and no config to set the host explicitly), so peers
  cannot dial it inbound.
- go-i2p's outbound NTCP2/SSU2 connection attempts toward i2pd are visible in
  the i2pd logs — watch for handshake rejections there.

## Verbosity

Logging of the testnet tool itself is controlled with the `DEBUG_TESTNET`
environment variable (`debug`, `warn`, or `error`; unset disables logging).
This is separate from router logs, which are always captured to `./logs/`.

## Status dashboard

`start` also boots a self-contained monitoring dashboard in the same
process (stdlib only: no external web server, database, CDN, or frontend
framework, no JavaScript; history is in memory). It polls every router's
I2PControl interface concurrently (5 s interval, ~5 s per-router timeout —
one down router never delays the others) and shows:

- `http://127.0.0.1:8765/` — network totals (peers, tunnels and bandwidth
  summed across routers), charts of those totals over the last 60 samples,
  and a per-router table (version, net status, uptime, tunnels, peers,
  bandwidth) refreshing every 5 seconds. Column headers are links that sort
  the table; sorting by **State** puts trouble on top.
- `http://127.0.0.1:8765/router/<name>` — one router's peer, tunnel and
  bandwidth charts plus its latest sample and the last 60 samples.
- `http://127.0.0.1:8765/topology` — which router is connected to which.
- `http://127.0.0.1:8765/api/topology` — the same graph as JSON.
- `http://127.0.0.1:8765/api/status` — JSON snapshot of everything above.
- `http://127.0.0.1:8765/healthz` — 200 while the dashboard runs.

**Router states.** `up` is green. A router whose poll fails goes **amber
(`stale`)** while its last good sample is still recent — the numbers on
screen are real, just ageing — and **red (`down`)** once that data ages past
three poll intervals, or if it never answered at all. Failures are also
classified (`not-serving`, `timeout`, `auth`, `unreachable`, `protocol`) so
"the control endpoint is not listening yet" is visibly different from "the
router is wedged".

**Charts.** Graphs are server-rendered SVG drawn from the samples the poller
actually collected; nothing is plotted that no router reported. A value an
implementation never exposes is labelled `not served by i2pd` rather than
shown as a dash or a zero — i2pd 2.56 does not serve tunnel in/out or
fast/high-capacity peer counts over I2PControl, and it renders those
aggregates on its own webconsole only via client-side JavaScript. History
covers 60 samples (5 minutes at the default interval) and is in memory, so
restarting the CLI clears it.

**Topology.** The graph is built from what the routers themselves report:
each i2pd router's transport session table (read inside the container via
the Docker API), each router's netDb contents, and each router's own
`router.info` identity, which yields the base64 hash the netDb file names
and session tables use. An edge is **observed** when a live transport
session exists and **inferred** when a router merely stores that peer's
RouterInfo. The page shows the graph's generation time. There is no
hardcoded peer list: peers that match no router in the roster are shown as
external rather than dropped, and a router that cannot be read produces a
warning instead of silently vanishing.

To disable it: `./bin/go-i2p-testnet -no-dashboard`. To run it standalone
against an already-running testnet (routers discovered from the live Docker
topology, nothing hardcoded): `make dashboard` or
`./bin/go-i2p-testnet -dashboard`.

**How routers are polled:** i2pd serves I2PControl (JSON-RPC on port 7650)
over HTTPS with a self-signed certificate it generates into its datadir; the
dashboard reads that certificate through the Docker API and trusts *only* it
(certificate pinning) — verification is never disabled silently. Use
`-insecure-tls` to opt out of pinning explicitly (it prints a loud warning).
go-i2p is configured for plaintext HTTP on the internal-only bridge, with its
own `allow_plaintext_non_loopback` policy opt-in set in the generated config.

**Known limitation — go-i2p is only observable once bootstrapped.** Upstream
go-i2p will not start its I2PControl server until its netDb holds 40
RouterInfos, and that floor is not configurable (`--bootstrap.low-peer-threshold`
and `--bootstrap.minimum-reseed-peers` do not change it). On a small testnet
the router therefore stays `down` with reason `not-serving` and never
publishes `router.info`. The dashboard reports this honestly and, because
I2PControl is not the only channel available, adds a plain-language
explanation derived from the router's own captured log telling you to run
`sync` or add routers. Fixing the floor itself belongs to go-i2p, not here.

**Password:** all routers share one I2PControl password. The CLI resolves it
in this order: the `GO_I2P_TESTNET_I2PCONTROL_PASSWORD` environment
variable, then the gitignored file `logs/.i2pcontrol-password`, then a
freshly generated random password written to that file (mode 0600). The
dashboard (including standalone mode) reads the same sources. The
well-known default `itoopie` is refused unless you are doing local testing
and set `TESTNET_ALLOW_DEFAULT_I2PCONTROL_PASSWORD=1`. Passwords and auth
tokens are never logged or rendered by this tool: they appear in no page, no
JSON, no CLI output and no captured log line (captured lines are scrubbed).
Routers run at i2pd's `info` log level by default because i2pd prints the
password it authenticates with at `debug`, and raw `docker logs` output is
outside this tool's scrubbing. Set `TESTNET_I2PD_LOGLEVEL=debug` when you
need that detail; the CLI then warns you that the credential will appear in
container logs.

```shell
export GO_I2P_TESTNET_I2PCONTROL_PASSWORD='pick-a-strong-one'
make build && ./bin/go-i2p-testnet   # then: start; add i2pd_router; ...
# open http://127.0.0.1:8765/
```

## TO DO
 - [ ] Port forwarding
 - [ ] Reseed via file
 - [ ] Reseed via node (i2pd)
 - [ ] Reseed via node (i2p java)
 - [ ] i2p java router node (startup + config)
 - [ ] go-i2p RouterInfo publishing (blocked on go-i2p writing router.info)
 - Metrics
   - [ ] TCP connection with daemon to relay router information
