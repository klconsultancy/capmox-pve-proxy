# capmox-pve-proxy

[![CI](https://github.com/klconsultancy/capmox-pve-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/klconsultancy/capmox-pve-proxy/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/klconsultancy/capmox-pve-proxy)](https://github.com/klconsultancy/capmox-pve-proxy/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/klconsultancy/capmox-pve-proxy)](go.mod)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/klconsultancy/capmox-pve-proxy/badge)](https://scorecard.dev/viewer/?uri=github.com/klconsultancy/capmox-pve-proxy)
[![License](https://img.shields.io/github/license/klconsultancy/capmox-pve-proxy)](LICENSE)
[![golangci-lint](https://img.shields.io/badge/linted_by-golangci--lint-blue)](https://golangci-lint.run)
[![Conventional Commits](https://img.shields.io/badge/Conventional%20Commits-1.0.0-yellow.svg)](https://conventionalcommits.org)

Presents several standalone Proxmox VE nodes to the [Cluster API Proxmox provider (CAPMOX)](https://github.com/ionos-cloud/cluster-api-provider-proxmox) as one Proxmox cluster, so a single Kubernetes cluster can have machines on all of them.

> **Status: first release.** The proxy is tested against go-proxmox, the client CAPMOX uses, and with CAPMOX v0.9.2 against two real nodes, one of them down, as described in [docs/testing.md](docs/testing.md). See [Releases](#releases).

## Why

CAPMOX talks to one Proxmox API per `ProxmoxCluster`. It spreads machines over nodes only inside one Proxmox cluster, and even there it cannot clone a template to another node's local storage ([#451](https://github.com/ionos-cloud/cluster-api-provider-proxmox/issues/451)). Support for several Proxmox endpoints is a proposal ([#370](https://github.com/ionos-cloud/cluster-api-provider-proxmox/issues/370), [#231](https://github.com/ionos-cloud/cluster-api-provider-proxmox/issues/231)). Joining standalone nodes into a Proxmox cluster only for this couples their failure domains.

## How it works

The proxy sits between CAPMOX and the nodes and answers as if they were one cluster:

- `/version` comes from the first node that answers; `/cluster/status` describes a quorate cluster of the configured nodes, each online when it answers.
- `/cluster/resources` merges every node's resources. A template that exists on several nodes under the same VMID is reported once, on the first of them in the configuration, so CAPMOX's template lookup by tags finds exactly one.
- `/cluster/nextid` reports a VMID free only when every node says it is free; for a node that is down, see below.
- `/nodes/{node}/…` requests go to that node, and task status to the node in the task's UPID.
- A clone with a target node (`POST /nodes/{src}/qemu/{tpl}/clone`, `target` ≠ `src`) becomes a local clone on the target node of the template with the same VMID. Each node therefore carries the template under the same VMID; without it on the target, the clone is refused.
- CAPMOX authenticates to the proxy with its own token; the proxy uses a separate token per node.
- Any endpoint the proxy does not implement is refused (HTTP 501) and logged, so a CAPMOX release that needs a new call fails visibly.

The implemented endpoints are the ones CAPMOX v0.9.2 calls through go-proxmox v0.8.2; see [`internal/proxy/proxy.go`](internal/proxy/proxy.go).

### When a node is down

Machines keep being created on the nodes that are up; machines on the down node wait for it.

- **Scheduling.** CAPMOX's scheduler reads every allowed node's status and guests, and gives up on all of them when one fails. The proxy answers for an unreachable node as a node without free memory or guests, which the scheduler never picks.
- **VMIDs.** Every 30 seconds (`-refresh`) the proxy lists each node's guests, and adds a clone to the list as soon as it starts it. For a node that is down, a VMID counts as taken if that node held it when it last answered. It must not count as free merely because the node cannot be asked: CAPMOX's `DeleteVM` treats a free VMID as an already deleted VM, so it would drop a machine on the down node while its VM still exists, and hand the VMID out again. The list is kept in memory only: a proxy started while a node is down has none for it, and every VMID counts as taken on it until it answers, so no machines are created in that time. CAPMOX gives up for good on a machine it could find no VMID for (`VMProvisionFailed`, "No free vmid found in vmIDRange"), even once the node is back; use a [MachineHealthCheck](#machinehealthcheck) to replace it.
- **Templates.** `/cluster/resources` leaves out an unreachable node, so a template is found on the nodes that are up. Select templates by tags: a fixed `sourceNode` that is down blocks every clone.
- **Waiting.** A node counts as down when it cannot be reached, or when it does not answer within 5 seconds one of the scheduler's reads or a request that asks every node, as when its API hangs. A request that asks every node gives up after 20 seconds in all, and a VMID search asks a node that turned out to be down only once. The proxy logs why it treated a node as down, such as a TLS error after a certificate change.
- **Machines on the down node.** Everything else about that node fails, as it would on Proxmox: deleting a machine there (for example when a MachineHealthCheck remediates it) is retried until the node is back.

#### MachineHealthCheck

Give every cluster's workers a MachineHealthCheck with a `nodeStartupTimeout`, such as 20 minutes, selecting only machines that register a Kubernetes node: the check deletes any machine that has none after that time. A k0s controller that is not also a worker never registers one, so with k0smotron select the workers' MachineDeployment (`cluster.x-k8s.io/deployment-name`), not the control plane. CAPMOX v0.9.1 does not try a machine again once it failed it, and when creating a machine's VM fails and later works, it leaves that VM unconfigured and stopped ([#953](https://github.com/ionos-cloud/cluster-api-provider-proxmox/issues/953)). Both are CAPMOX's, but a node that is down makes them more likely through the proxy: the health check deletes such a machine, and CAPI creates a new one.

### Errors

go-proxmox treats only HTTP 400, 401, 403, 500 and 501 as errors: anything else, such as a 404 or Proxmox's own 595 for an unreachable node, reaches CAPMOX as success with empty data. The proxy therefore answers every other error status, its own or a node's, as 500.

## Configuration

```yaml
listen: ":8006"
clusterName: homelab
tls:                              # the certificate the proxy serves
  certFile: /tls/tls.crt
  keyFile: /tls/tls.key
client:                           # the token CAPMOX sends
  tokenID: capmox@pve!capmox
  secretFile: /secrets/client
nodes:                            # in order of preference
  - name: pve01                   # the node's own name
    url: https://10.0.0.2:8006
    tokenID: capmox@pve!proxy
    secretFile: /secrets/pve01
    caFile: /ca/pve01.pem         # the node's /etc/pve/pve-root-ca.pem
  - name: pve02
    url: https://10.0.0.3:8006
    tokenID: capmox@pve!proxy
    secretFile: /secrets/pve02
    caFile: /ca/pve02.pem
```

Secrets are read from files, such as mounted Kubernetes Secrets, never from the configuration itself. `insecureHTTP: true` in place of `tls` serves plain HTTP, for tests only.

Each node's token needs only what CAPMOX does: VM rights on the pool its machines go into, the right to clone the template, rights on the storage the machines use, the right to upload and delete ISOs on the first storage with `iso` content it can see (for their cloud-init ISO, best a storage of its own), `SDN.Use` on the bridge they connect to (`/sdn/zones/localnetwork/<bridge>`), and `Sys.Audit` on the node itself, for the node's status that CAPMOX's scheduler reads. The template needs the QEMU guest agent enabled, and no cloud-init drive of its own. See [docs/testing.md](docs/testing.md#tokens) for an example.

A node's `url` must match its certificate: with a certificate for its DNS name, such as one from ACME, use that name and leave out `caFile`. Proxmox's own self-signed certificate also covers the node's IP address; `caFile` is that node's `pve-root-ca.pem`.

Run it with `capmox-pve-proxy -config /etc/capmox-pve-proxy/config.yaml` and point CAPMOX's `url` at it, with the client token.

For Kubernetes probes, `GET /healthz` answers `ok` without a token. It says only that the proxy serves, and asks no node, so a node that is down does not take the proxy out of its Service. The [Helm chart](#helm-chart) uses it for its readiness and liveness probes, over HTTPS (HTTP under `insecureHTTP: true`).

## Helm chart

The chart runs the proxy in the cluster CAPMOX runs in: two replicas, spread over Kubernetes nodes where possible, behind a ClusterIP Service on port 8006. It is published with each release at `oci://ghcr.io/klconsultancy/charts/capmox-pve-proxy`, with the same version as the image, and runs that release's image by its digest.

It renders `config.yaml` from `config` in its values, and sets `listen` and every file path itself. It never creates Secrets; create them first, in the release's namespace:

- `tokens.existingSecret`: the client token's secret under the key `client`, and each node's token secret under the node's name.
- `tls.existingSecret`: the certificate the proxy serves, under `tls.crt` and `tls.key`, for the Service's name as CAPMOX calls it (`capmox-pve-proxy.capmox-system.svc` for the release below; the install's notes give the URL). It may be the same Secret as the tokens'. Not needed under `config.insecureHTTP: true`.
- A node's `pve-root-ca.pem`, from a ConfigMap or a Secret, if the node needs one (see above).

```sh
kubectl -n capmox-system create secret generic capmox-pve-proxy \
  --from-file=client=client.secret --from-file=pve01=pve01.secret --from-file=pve02=pve02.secret \
  --from-file=tls.crt=tls.crt --from-file=tls.key=tls.key
kubectl -n capmox-system create configmap pve-root-ca \
  --from-file=pve01.pem=pve01-root-ca.pem --from-file=pve02.pem=pve02-root-ca.pem
```

```yaml
# values.yaml
config:
  clusterName: homelab
  client:
    tokenID: capmox@pve!capmox
  nodes:                          # in order of preference
    - name: pve01
      url: https://10.0.0.2:8006
      tokenID: capmox@pve!proxy
      ca:
        configMap: pve-root-ca    # or secret: <name>
        key: pve01.pem            # default pve-root-ca.pem
    - name: pve02
      url: https://10.0.0.3:8006
      tokenID: capmox@pve!proxy
      ca:
        configMap: pve-root-ca
        key: pve02.pem
tokens:
  existingSecret: capmox-pve-proxy
tls:
  existingSecret: capmox-pve-proxy
```

```sh
helm install capmox-pve-proxy oci://ghcr.io/klconsultancy/charts/capmox-pve-proxy \
  --version <version> --namespace capmox-system -f values.yaml
```

The values schema refuses unknown keys and incomplete configurations, so a typo fails the install instead of being ignored. See [`values.yaml`](charts/capmox-pve-proxy/values.yaml) for the rest, such as `networkPolicy.enabled`, which allows ingress only from CAPMOX's namespace (`capmox-system` by default).

## Releases

[release-please](https://github.com/googleapis/release-please) keeps a pull request open with the next version and its changelog; merging it makes the release. [Renovate](renovate.json) brings updates on the 1st and the 15th of each month (security fixes at once) and merges them once the checks pass, except a new major version, so updates of what the image contains collect in that pull request until it is merged. Each release is a multi-arch image (`linux/amd64`, `linux/arm64`) at `ghcr.io/klconsultancy/capmox-pve-proxy`, running as a non-root user on a distroless base. It is tagged with its version, such as `1.2.3` and `1.2` (a pre-release such as `1.2.3-rc.1` only with its full version); there is no `latest`. Pin it by the digest given in the [release notes](https://github.com/klconsultancy/capmox-pve-proxy/releases). The [Helm chart](#helm-chart) is released with it, at the same version, and a change to the chart is a release of the proxy.

The image carries each platform's build provenance and SBOM. A signed provenance attestation names the image by that digest, so it covers them too. Check it, and read the SBOMs, with:

```sh
gh attestation verify oci://ghcr.io/klconsultancy/capmox-pve-proxy@sha256:<digest> --repo klconsultancy/capmox-pve-proxy
docker buildx imagetools inspect ghcr.io/klconsultancy/capmox-pve-proxy@sha256:<digest> --format '{{json .SBOM}}'
```

The chart has a signed provenance attestation too. Its digest is in the release notes, and `helm pull` prints it:

```sh
helm pull oci://ghcr.io/klconsultancy/charts/capmox-pve-proxy --version <version>
gh attestation verify oci://ghcr.io/klconsultancy/charts/capmox-pve-proxy@sha256:<digest> --repo klconsultancy/capmox-pve-proxy
```

## Scope

Only the part of the Proxmox API that CAPMOX calls. It is not a general way to manage several Proxmox nodes through one API, and requests to make it one will be declined. It is meant to be retired once CAPMOX supports several Proxmox endpoints itself.

## Development

`go test -race ./...` runs the unit tests and the contract tests, which drive the proxy with go-proxmox in CAPMOX's own call sequences against fake nodes. `scripts/chart-check.sh` lints and validates the chart and checks that its schema refuses each file in [`ci/invalid`](charts/capmox-pve-proxy/ci/invalid); `scripts/chart-kind-test.sh` installs it in a kind cluster, as CI does. Keep go-proxmox in `go.mod` at the version CAPMOX pins. Its own dependencies may be raised for security fixes, as `gorilla/websocket` is: the tests do not use them, and they never reach the binary.

Before a release, test against real nodes as described in [docs/testing.md](docs/testing.md), with a release candidate: an admin tags main as the next version with `-rc.1` (such as `v1.2.3-rc.1`), which publishes its image as a pre-release. Merge the release pull request once the tests pass.

## License

[Apache-2.0](LICENSE)
