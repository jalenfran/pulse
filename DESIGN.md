# Pulse — design

A private status and alerting backend for a small Kubernetes homelab, plus an
iOS app that talks to it over Tailscale. The backend and the app meet at
`api/openapi.yaml`.

## Why

Small homelabs often run without a monitoring stack, and their failures are
the quiet kind: a certificate stops renewing, a health check fails into the
journal, an unattended upgrade breaks one node, a pod crashloops for two days.
Pulse turns those into a timeline and notifications, and gives a phone view of
the cluster.

## Architecture

```
 phone (Tailscale) ──https──▶ reverse proxy (TLS; allow LAN + tailnet, deny all)
                                         │
                                         ▼
                              ┌──────────────────────────────┐
 hosts (systemd hooks) ──────▶│ pulse  (ns: pulse, 1 replica) │──▶ k8s API (read-only SA,
   POST /v1/ingest            │  SQLite on a node-local PVC   │     restart via resourceNames)
   per-host token             │  notifier                     │──▶ ntfy now / APNs later
                              └──────────────────────────────┘
```

One service with a single replica, because SQLite is the store. It **pulls**
from Kubernetes and **receives pushes** from the hosts. It runs no agent on
the hosts.

## Data sources

| Source | How | What |
|---|---|---|
| Kubernetes | pull + watch (in-cluster SA) | nodes (Ready, pressure, version), workloads (ready/desired), pods (restarts, CrashLoopBackOff), Warning events, pod logs, metrics-server CPU/mem |
| Hosts | push: `curl POST /v1/ingest` from systemd | OS upgrade results, health-check pass/fail, boots (kernel, reason), accepted SSH logins |
| Probes | pull, from the pod (phase 3) | public endpoints up, cert days remaining over the network |

The host side isn't part of this repo, because it belongs with whatever
configures the hosts. It's a small script wired as `OnFailure=` /
`ExecStartPost=` on existing systemd units that POSTs JSON to `/v1/ingest`.
Its token is installed out of band at `/var/lib/pulse/token`.

Host journals are push-only. Tailing an arbitrary host journal on demand would
need an agent with journal access on every node. That's deliberately out of
scope until it's clearly needed.

## Event model

Everything ends up as one timeline:

```json
{
  "id": "01J…",                       // ULID built from ts: sort order and pagination cursor
  "ts": "2026-10-16T04:03:11Z",
  "source": "host:node2",             // or "k8s", "probe"
  "kind": "nixos-upgrade",            // node-health, boot, ssh-login, pod-crashloop, node-notready, …
  "severity": "info",                 // info | warning | critical
  "title": "node2 upgraded to generation 412",
  "body": "…optional detail, e.g. the failing check's output…",
  "key": "nixos-upgrade:node2"        // dedupe/cooldown key for notifications
}
```

## API (v1)

JSON over HTTPS. Every call except `/healthz` and `/readyz` needs a bearer
token.

| Method | Path | Notes |
|---|---|---|
| GET | `/healthz`, `/readyz` | liveness; readiness (caches synced), no auth |
| GET | `/v1/overview` | everything the home screen and widget need in one call: node summary, unhealthy workload count, last upgrade per host, open problems, latest events |
| GET | `/v1/nodes` | list, with metrics if metrics-server answers |
| GET | `/v1/nodes/{name}` | conditions, pods on node, recent host events |
| GET | `/v1/workloads?namespace=` | deployments, statefulsets, daemonsets (ready/desired, restarts) and cronjobs (last run) |
| GET | `/v1/pods?namespace=&node=` | pod list |
| GET | `/v1/pods/{ns}/{pod}` | containers, status, restarts, last termination reason |
| GET | `/v1/pods/{ns}/{pod}/logs?container=&tail=200&previous=` | plain text; `previous=true` for the crashed instance |
| GET | `/v1/pods/{ns}/{pod}/logs/stream?container=&tail=200` | follow as Server-Sent Events |
| GET | `/v1/events?before=&minSeverity=&source=&kind=&limit=` | unified timeline, newest first, cursor = ULID |
| GET | `/v1/events/{id}` | one event; ids replaced by a recurrence still resolve |
| GET | `/v1/actions` | the allowlisted actions, so the app can render buttons (phase 3) |
| POST | `/v1/actions/{id}` | run one allowlisted action (phase 3) |
| POST | `/v1/devices` | register a push target (APNs token, phase 4) |
| POST | `/v1/ingest` | **host token only**: accept one event from a host |

The spec in `api/openapi.yaml` changes before any handler does. The Swift
client is generated from it with Apple's `swift-openapi-generator`, so the app
never hand-writes models.

### Deep links

The app registers `pulse://`. The server emits these links and the app opens
them (the table is also in `api/openapi.yaml`):

| Link | Emitted by | Opens |
|---|---|---|
| `pulse://enroll?url=…&token=…` | `pulse token create` QR | enrollment |
| `pulse://events/<id>` | ntfy click action, single-event notification | that event |
| `pulse://events` | ntfy click action, quiet-hours digest | the timeline |

Mirrored events (crashloops, repeating Kubernetes warnings) get a new id each
time they recur, which is what moves them back to the top of the timeline. A
SQLite trigger keeps each replaced id in `event_aliases`, so a link from an
earlier notification resolves to the current version of the event.

## Auth and exposure

The pieces are layered. Each one assumes the one before it has failed.

1. **Network.** The reverse proxy only admits the LAN and the tailnet
   (`100.64.0.0/10`). The hostname can be public so it gets a normal
   certificate, but the API isn't reachable from the internet. On the LAN,
   the name must resolve to the proxy's local address (split DNS), or LAN and
   tailnet clients get hairpinned through the WAN address and turned away.
2. **User tokens.** Each device gets a random 32-byte token, stored hashed
   (SHA-256) in SQLite, with a name and last-used time. The app keeps it in
   the Keychain. Tokens are minted with a CLI inside the pod
   (`kubectl exec … pulse token create iphone`), which prints a QR code the
   app scans. They can be revoked the same way.
3. **Host tokens.** These are separate from user tokens and accepted **only**
   by `/v1/ingest`. Each token maps to one host, and the server sets `source`
   from the token, never from the body, so a compromised node can't post as
   another node.
4. **Kubernetes RBAC.**
   - The ClusterRole is `get/list/watch` on nodes, pods, `pods/log`, events,
     the apps/v1 and batch workloads and `metrics.k8s.io`. It has **no access
     to Secrets** at all.
   - Restart actions get a namespaced Role with `patch` on `deployments`
     restricted by `resourceNames`. Even a fully compromised pulse can only
     restart the deployments named there.
5. **Actions.** These are a fixed list in config (id → deployment to
   rollout-restart). There is no endpoint that takes a command, a namespace
   or a resource name from the request.

Note that pod logs can contain whatever an app prints, including secrets.
That's the reason exposure stays limited to LAN and tailnet.

## Notifications

Rules run when an event arrives:

- warning/critical events notify, and info doesn't, apart from upgrade
  results, which are worth seeing every time
- a cooldown applies per `key` (default 6h), so a failing hourly check pings
  once, not 24 times
- `critical` gets through quiet hours (23:00–08:00) and everything else waits
  for the morning

**Phase 2: ntfy.** Pulse publishes to a self-hosted ntfy server, and the
phone uses the ntfy iOS app. For instant delivery on iOS, the self-hosted
server sets `upstream-base-url: https://ntfy.sh`. Only a poll request
carrying a hash of the message ID goes upstream, and the content stays on the
server, which the phone fetches over the tailnet.

**Phase 4: APNs directly** from pulse (token-based `.p8` key). This needs the
paid Apple Developer account. Phase 4 also brings widgets and Live Activities.

## Storage

SQLite in WAL mode on a node-local PVC (`local-path`), never NFS, because
SQLite locking over NFS is unreliable. The tables are `events` (30-day
retention), `tokens`, `notify_sent` (last notification per key, for the
cooldown), `notify_queue` (held during quiet hours), and later `devices`.
Losing the database costs the event history and means re-minting tokens.
Nothing else is stored there.

## Repo layout

```
pulse/
  DESIGN.md
  api/openapi.yaml      contract between backend and app
  backend/              service + Dockerfile
  deploy/               kustomize base: namespace, rbac, pvc, deployment, service
  ios/                  Xcode project
```

`deploy/` is deliberately generic. Site specifics (node pinning, how the
Service is exposed, the public URL) go in a kustomize overlay kept with the
rest of your cluster config.

## Phases

1. **Read-only MVP.** Covers `openapi.yaml`, user tokens, overview, nodes,
   workloads, pods, logs and k8s-sourced events. On the iOS side: Overview,
   Nodes, Workloads and Logs screens.
2. **Host events + alerts.** Done: the notifier (rules, cooldown, quiet-hours
   digest) publishing to ntfy, plus a host reporter for upgrades, health
   checks, boots and SSH logins.
3. **Actions + probes.** Allowlisted restarts, plus endpoint and cert probes.
4. **Native polish.** APNs, widgets, and Live Activities (for example while
   an upgrade is running).

## Open decisions

- ~~Backend language~~: Go (decided 2026-10-15; resource use measured at ~38 MB RSS).
- Whether to buy the Apple Developer account (needed for phase 4 and
  TestFlight; free signing expires every 7 days).
- Kubernetes watch vs polling for k8s events. The plan is watch for pods and
  events, with a 60s poll for metrics.
