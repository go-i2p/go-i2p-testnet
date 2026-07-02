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
| `start` / `stop` | Create/destroy the testnet (network, shared volume, all routers) |
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

## TO DO
 - [ ] Port forwarding
 - [ ] Reseed via file
 - [ ] Reseed via node (i2pd)
 - [ ] Reseed via node (i2p java)
 - [ ] i2p java router node (startup + config)
 - [ ] go-i2p RouterInfo publishing (blocked on go-i2p writing router.info)
 - Metrics
   - [ ] TCP connection with daemon to relay router information
