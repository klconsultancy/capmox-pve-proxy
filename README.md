# capmox-pve-proxy

[![CI](https://github.com/klconsultancy/capmox-pve-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/klconsultancy/capmox-pve-proxy/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/klconsultancy/capmox-pve-proxy/badge)](https://scorecard.dev/viewer/?uri=github.com/klconsultancy/capmox-pve-proxy)

Presents several standalone Proxmox VE nodes to the [Cluster API Proxmox provider (CAPMOX)](https://github.com/ionos-cloud/cluster-api-provider-proxmox) as one Proxmox cluster, so a single Kubernetes cluster can have machines on all of them.

> **Status: not released yet.** The proxy is implemented and tested against go-proxmox, the client CAPMOX uses, but has not yet run against real nodes. There is no image yet.

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

The implemented endpoints are the ones CAPMOX v0.9.1 calls through go-proxmox v0.8.1; see [`internal/proxy/proxy.go`](internal/proxy/proxy.go).

### When a node is down

Machines keep being created on the nodes that are up; machines on the down node wait for it.

- **Scheduling.** CAPMOX's scheduler reads every allowed node's status and guests, and gives up on all of them when one fails. The proxy answers for an unreachable node as a node without free memory or guests, which the scheduler never picks.
- **VMIDs.** Every 30 seconds (`-refresh`) the proxy lists each node's guests, and adds a clone to the list as soon as it starts it. For a node that is down, a VMID counts as taken if that node held it when it last answered. It must not count as free merely because the node cannot be asked: CAPMOX's `DeleteVM` treats a free VMID as an already deleted VM, so it would drop a machine on the down node while its VM still exists, and hand the VMID out again. The list is kept in memory only: a proxy started while a node is down has none for it, and every VMID counts as taken on it until it answers, so no machines are created in that time.
- **Templates.** `/cluster/resources` leaves out an unreachable node, so a template is found on the nodes that are up. Select templates by tags: a fixed `sourceNode` that is down blocks every clone.
- **Waiting.** Connecting to a node gives up after 3 seconds, and a request that asks every node after 20. A VMID search asks a node that turned out to be down only once.
- **Machines on the down node.** Everything else about that node fails, as it would on Proxmox: deleting a machine there (for example when a MachineHealthCheck remediates it) is retried until the node is back.

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

Each node's token needs only what CAPMOX does: VM rights on the pool its machines go into, and on the storage they and their cloud-init ISO use.

Run it with `capmox-pve-proxy -config /etc/capmox-pve-proxy/config.yaml` and point CAPMOX's `url` at it, with the client token.

## Scope

Only the part of the Proxmox API that CAPMOX calls. It is not a general way to manage several Proxmox nodes through one API, and requests to make it one will be declined. It is meant to be retired once CAPMOX supports several Proxmox endpoints itself.

## Development

`go test -race ./...` runs the unit tests and the contract tests, which drive the proxy with go-proxmox in CAPMOX's own call sequences against fake nodes. Keep go-proxmox in `go.mod` at the version CAPMOX pins.

## License

[Apache-2.0](LICENSE)
