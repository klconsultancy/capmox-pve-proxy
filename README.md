# capmox-pve-proxy

Presents several standalone Proxmox VE nodes to the [Cluster API Proxmox provider (CAPMOX)](https://github.com/ionos-cloud/cluster-api-provider-proxmox) as one Proxmox cluster, so a single Kubernetes cluster can have machines on all of them.

> **Status: not usable yet.** The repository holds the project skeleton; the proxy itself is being written.

## Why

CAPMOX talks to one Proxmox API per `ProxmoxCluster`. It spreads machines over nodes only inside one Proxmox cluster, and even there it cannot clone a template to another node's local storage ([#451](https://github.com/ionos-cloud/cluster-api-provider-proxmox/issues/451)). Support for several Proxmox endpoints is a proposal ([#370](https://github.com/ionos-cloud/cluster-api-provider-proxmox/issues/370), [#231](https://github.com/ionos-cloud/cluster-api-provider-proxmox/issues/231)). Joining standalone nodes into a Proxmox cluster only for this couples their failure domains.

## How it works

The proxy sits between CAPMOX and the nodes and answers as if they were one cluster:

- `/version` and `/cluster/status` describe a cluster made of the configured nodes.
- `/cluster/resources` merges every node's resources; `/cluster/nextid` reports a VMID free only when it is free on every node.
- `/nodes/{node}/…` requests, and tasks by the node in their UPID, go to that node.
- A clone with a target node (`POST /nodes/{src}/qemu/{tpl}/clone`, `target` ≠ `src`) becomes a local clone on the target node of the template with the same VMID. Each node therefore carries the template under the same VMID.
- CAPMOX authenticates to the proxy with its own token; the proxy uses a separate token per node.
- Any endpoint the proxy does not implement is refused and logged, so a CAPMOX release that needs a new call fails visibly.

## Scope

Only the part of the Proxmox API that CAPMOX calls. It is not a general way to manage several Proxmox nodes through one API, and requests to make it one will be declined. It is meant to be retired once CAPMOX supports several Proxmox endpoints itself.

## License

[Apache-2.0](LICENSE)
