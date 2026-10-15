# Pulse

A private status API for a Kubernetes homelab, with an iOS app on top. It's
meant to be reachable only from the LAN and a tailnet. See
[DESIGN.md](DESIGN.md) for the why, and [api/openapi.yaml](api/openapi.yaml)
for the contract.

```
api/openapi.yaml   the contract; the iOS client is generated from it
backend/           Go service (pulse serve, pulse token …)
deploy/            kustomize base: namespace, RBAC, PVC, deployment, service
ios/               Xcode project
```

## Backend development

Run against a real cluster with your kubeconfig and a throwaway database:

```sh
cd backend
export PULSE_DB=/tmp/pulse-dev.db PULSE_ADDR=127.0.0.1:18080
go run ./cmd/pulse token create dev     # prints the token
go run ./cmd/pulse serve
curl -H "Authorization: Bearer pls_…" localhost:18080/v1/overview
```

If you change an endpoint, update `api/openapi.yaml` in the same commit.

| Env | Default | |
|---|---|---|
| `PULSE_DB` | `/data/pulse.db` | SQLite path |
| `PULSE_ADDR` | `:8080` | listen address |
| `PULSE_PUBLIC_URL` | (unset) | URL the app should use; goes in the enrollment QR. Without it, `token create` prints the token but no QR |
| `PULSE_RETENTION_DAYS` | `30` | event retention |
| `PULSE_NTFY_URL` | (unset) | ntfy server base URL. Unset disables notifications |
| `PULSE_NTFY_TOPIC` | `pulse` | ntfy topic |
| `PULSE_NTFY_TOKEN` | (unset) | ntfy access token with write access to the topic |
| `PULSE_NOTIFY_MIN_SEVERITY` | `warning` | lowest severity that notifies |
| `PULSE_NOTIFY_ALWAYS` | `nixos-upgrade,node-ready,workload-recovered` | kinds that notify at any severity |
| `PULSE_NOTIFY_IGNORE` | `k8s-event` | kinds that never notify (raw Kubernetes warnings are noisy; crashloops and node outages have their own kinds) |
| `PULSE_NOTIFY_COOLDOWN` | `6h` | at most one notification per event key in this window |
| `PULSE_QUIET_HOURS` | `23-8` | local hours when only `critical` goes out; the rest arrives as one digest afterwards. `off` disables |
| `TZ` | UTC | time zone for quiet hours |

## Notifications

Pulse pushes through [ntfy](https://ntfy.sh). Point `PULSE_NTFY_URL` at your
own server (or ntfy.sh with an unguessable topic) and subscribe to the topic
in the ntfy app. With a self-hosted server on iOS, set
`upstream-base-url: https://ntfy.sh` in ntfy's config for instant delivery.
Only a hash of the message ID goes to ntfy.sh, and the app fetches the
content from your server.

What Pulse itself raises from the cluster:

| Kind | When | Severity |
|---|---|---|
| `node-notready` | a node goes NotReady | warning |
| `node-down` | still NotReady 5 minutes later | critical |
| `node-ready` | it's back | info (notifies by default) |
| `pod-crashloop` | a container enters CrashLoopBackOff | warning |
| `job-failed` | a Job fails (named after its CronJob, so a nightly backup has one cooldown) | warning |
| `workload-unhealthy` | a Deployment/StatefulSet/DaemonSet has been short of ready replicas for 10 minutes, and it isn't a crashloop. The body says why: image pull, unschedulable, readiness | warning |
| `workload-recovered` | a workload reported unhealthy is healthy again | info (notifies by default) |

Only `node-down` is critical, so routine reboots don't wake anyone. The grace
periods (5 and 10 minutes) cover a node rebooting and a rollout.

```sh
kubectl -n pulse exec deploy/pulse -- /pulse notify test
```

## Host events

Anything that can run `curl` can report. POST JSON to `/v1/ingest` with a
host token:

```sh
curl -H "Authorization: Bearer $TOKEN" https://pulse.example.com/v1/ingest \
  -d '{"kind":"node-health","severity":"warning","title":"node2 health check failed","body":"/ is 91% full"}'
```

Hook it to systemd with `OnFailure=` / `ExecStartPost=`. Kinds Pulse knows
about: `nixos-upgrade`, `node-health` and `boot` show on the overview's host
cards, and `ssh-login` and anything else go to the timeline. To remove a
retired host's history:

```sh
kubectl -n pulse exec deploy/pulse -- /pulse events purge --source host:old-node
```

## Deploy

`deploy/` is a generic kustomize base. Put your specifics in an overlay:
pinning to the node that has the image and volume, exposing the Service to
your reverse proxy, and `PULSE_PUBLIC_URL`. For example:

```yaml
# my-cluster/pulse/kustomization.yaml
resources:
  - ../../pulse/deploy
patches:
  - target: { kind: Deployment, name: pulse }
    patch: |
      - op: add
        path: /spec/template/spec/nodeSelector
        value: { kubernetes.io/hostname: node1 }
      - op: add
        path: /spec/template/spec/containers/0/env/-
        value: { name: PULSE_PUBLIC_URL, value: https://pulse.example.com }
```

Without a registry, build on the node and import the image into k3s directly:

```sh
docker build -t pulse:0.2.2 backend/
docker save pulse:0.2.2 | sudo k3s ctr images import -
kubectl apply -k my-cluster/pulse
```

For a new version, bump the tag in both places. A reused tag won't be
re-imported over the one containerd already has with `IfNotPresent`.

Put a TLS reverse proxy in front that admits only your LAN and tailnet
(`100.64.0.0/10`). The log stream is Server-Sent Events. Pulse sends
`X-Accel-Buffering: no` and a ping every 15s, so nginx needs no special
buffering config, only a `proxy_read_timeout` above 15s.

## Tokens

```sh
# A device (prints a QR the app scans: pulse://enroll?url=…&token=…)
kubectl -n pulse exec -it deploy/pulse -- /pulse token create iphone

# A host, for /v1/ingest only
kubectl -n pulse exec deploy/pulse -- /pulse token create --host node2

kubectl -n pulse exec deploy/pulse -- /pulse token list
kubectl -n pulse exec deploy/pulse -- /pulse token revoke iphone
kubectl -n pulse exec deploy/pulse -- /pulse token revoke --host node2
```

Only a hash of each token is stored, so a lost token gets revoked and
re-minted.

## iOS development

Open `ios/Pulse.xcodeproj` (Xcode 26+, iOS 26+). The first build asks you to
trust the OpenAPI generator plugin. The Mac must be on the LAN or tailnet to
reach the API.

```
ios/Pulse/      SwiftUI app: Overview, Nodes, Workloads, Events, pod logs
ios/PulseKit/   Swift package: client generated from api/openapi.yaml
                (symlinked in), Keychain, enrollment links, log parsing
```

The client is regenerated on every build, so an endpoint change in
`api/openapi.yaml` shows up as a compile error in the app, not a runtime one.
PulseKit's tests run on the Mac:

```sh
cd ios/PulseKit && swift test
```

Debug builds read two environment variables, which make simulator runs
scriptable without tapping. `PULSE_OPEN` takes any link the app handles
(`pulse://enroll?…`, `pulse://events`, `pulse://events/<id>`):

```sh
SIMCTL_CHILD_PULSE_OPEN='pulse://enroll?url=…&token=…' \
SIMCTL_CHILD_PULSE_TAB=nodes \
  xcrun simctl launch booted dev.homelab.pulse
```
