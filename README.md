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
docker build -t pulse:0.1.0 backend/
docker save pulse:0.1.0 | sudo k3s ctr images import -
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

Work in `ios/`. Generate the API client from `../api/openapi.yaml` with
[swift-openapi-generator](https://github.com/apple/swift-openapi-generator).
The Mac must be on the tailnet to reach the API.
