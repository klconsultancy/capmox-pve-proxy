# Testing against real nodes

The unit and contract tests drive the proxy against fake nodes. This is how to check it against real Proxmox VE nodes before a release, in three steps: the proxy on its own, CAPMOX through it, and the proxy while a node is down. Each step assumes the one before it passed.

Record what you tested in the pull request or release that follows: the CAPMOX, Proxmox VE and proxy versions, and which checks passed.

## Setup

You need two or more standalone Proxmox VE nodes, not joined into a Proxmox cluster. The examples call them `pve01` and `pve02`; use each node's own name (`hostname`).

### A template on every node

Every node carries the template under the same VMID, with the same tags, because a clone onto another node becomes a local clone there. Build it once and copy it:

```sh
# On pve01, with the template built as VMID 9000:
vzdump 9000 --dumpdir /var/tmp --compress zstd
scp /var/tmp/vzdump-qemu-9000-*.vma.zst pve02:/var/tmp/

# On pve02; a template's backup restores as a template:
qmrestore /var/tmp/vzdump-qemu-9000-*.vma.zst 9000 --storage local-lvm
qm config 9000 | grep -E '^(template|tags)'
```

A template that uses a snippet, such as `cicustom: vendor=local:snippets/…`, needs that file on every node too, in a storage with `snippets` content.

### Tokens

On each node, a token for the proxy with only what CAPMOX needs (see [Configuration](../README.md#configuration)). For example, with machines in the pool `capmox` on the storage `local-lvm`:

```sh
pveum user add capmox@pve
pveum pool add capmox
pveum acl modify /pool/capmox --users capmox@pve --roles PVEVMAdmin
pveum acl modify /vms/9000 --users capmox@pve --roles PVETemplateUser
pveum acl modify /storage/local-lvm --users capmox@pve --roles PVEDatastoreUser
pveum acl modify /nodes/pve01 --users capmox@pve --roles PVEAuditor   # the node's status
pveum user token add capmox@pve proxy --privsep 0   # prints the secret once
```

Check the rights rather than widen them up front: a check below that fails with HTTP 403 shows what is missing, and the README's list of rights should then be corrected.

Put each node's secret in its own file, and choose a client token and secret for CAPMOX to send to the proxy. The client token does not exist on any node.

### The proxy

Give each node the `url` its certificate is for. A node with an ACME certificate, such as one from Let's Encrypt, is reached by its DNS name, without `caFile`. A node with Proxmox's own certificate can be reached by its IP address, with a copy of its `/etc/pve/pve-root-ca.pem` as `caFile`. The proxy log says when it does not trust a node. Give the proxy a certificate that CAPMOX will trust, and write the [configuration](../README.md#configuration) with both nodes, `pve01` first.

Run the release candidate's image, or `go run ./cmd/capmox-pve-proxy -config config.yaml` from a checkout:

```sh
docker run --rm -p 8006:8006 -v "$PWD/proxy:/etc/capmox-pve-proxy:ro" \
  ghcr.io/klconsultancy/capmox-pve-proxy:0.1.0-rc.1
```

with the configuration, certificate, CA files and secrets in `proxy/`, and their paths in the configuration under `/etc/capmox-pve-proxy`. Keep its log in view during every step: it says which node it treated as down and why, and refuses any call it does not implement.

## Step 1: the proxy on its own

[`scripts/smoke-test.sh`](../scripts/smoke-test.sh) calls the proxy the way CAPMOX does and checks each answer. It needs `curl` and `jq`.

```sh
export PROXY_URL=https://10.0.0.10:8006 PROXY_CA=proxy/ca.pem \
  PROXY_TOKEN_ID='capmox@pve!capmox' PROXY_SECRET_FILE=proxy/client TEMPLATE_VMID=9000
scripts/smoke-test.sh            # reads only
CLONE=1 CLONE_POOL=capmox scripts/smoke-test.sh    # also clones the template from pve01 onto pve02, then deletes it
```

It checks that:

- a wrong client token is refused (401), and an endpoint the proxy does not implement too (501);
- the nodes appear as one quorate cluster, every node online;
- the template is reported once, on the first node, and every node carries it under the same VMID;
- every node's status, guests and storage can be read with the node's token;
- `/cluster/nextid` hands out a VMID free on every node, and counts the template's VMID as taken;
- with `CLONE=1`: a clone from `pve01` with `target=pve02` runs on `pve02`, its VMID counts as taken until it is deleted, and free again after.

Fix whatever fails here before going on: TLS, tokens and rights are far easier to find without CAPMOX in between.

## Step 2: CAPMOX through the proxy

Create a management cluster, for example with kind, and install CAPMOX v0.9.1 with `clusterctl init --infrastructure proxmox`, pointing it at the proxy rather than a node:

```sh
export PROXMOX_URL=https://10.0.0.10:8006 PROXMOX_TOKEN='capmox@pve!capmox' PROXMOX_SECRET=<client secret>
```

The management cluster must reach the proxy: with kind, use the host's address, not `localhost`.

Create a workload cluster whose machines may go on both nodes (`allowedNodes: [pve01, pve02]`), with the template selected by its tags rather than a fixed `sourceNode` and `templateID`. Then check that:

- [ ] every machine gets running, and the machines are spread over both nodes;
- [ ] no VMID is used twice, on one node or across them;
- [ ] scaling up and down works, and scaling down leaves no VM behind;
- [ ] each machine's cloud-init ISO is uploaded to its own node, and removed with it;
- [ ] deleting the workload cluster leaves no VMs or ISOs on either node;
- [ ] the proxy log shows no refused (501) calls.

## Step 3: a node down

The proxy matters most when a node is down; see [When a node is down](../README.md#when-a-node-is-down). Run each scenario on a running workload cluster, and bring the node back before the next.

With two nodes, a node going down takes the control-plane machines on it with it: with three, etcd keeps quorum only if the down node had one of them. Scale workers in these scenarios, or use one control-plane machine on the node that stays up.

| Scenario | How | Expected |
|---|---|---|
| Node unreachable | On `pve02`: `systemctl stop pveproxy` | Within seconds, new machines go on `pve01` only. The proxy logs `pve02` as unreachable. |
| Node API hangs | On `pve02`: `systemctl kill -s STOP pveproxy`; afterwards `systemctl kill -s CONT pveproxy` | As above, after 5 seconds per request, never 20 or more. |
| Proxy restarted while a node is down | Stop `pveproxy` on `pve02`, restart the proxy | No new machines on any node until `pve02` answers, as every VMID counts as taken on it. |
| Machine deleted on the down node | Stop `pveproxy` on `pve02`, `kubectl delete machine <one on pve02>` | The machine stays deleting, its VM stays on `pve02`, and its VMID is not handed out again. Once `pve02` is back, the VM is deleted and the machine goes. |
| Node certificate not trusted | Set `pve02`'s `caFile` to a CA that did not sign its certificate, restart the proxy | `pve02` is treated as down, and the log names the TLS error. |
| Node back | Start `pveproxy` on `pve02` again | Machines go on both nodes again, without restarting the proxy or CAPMOX. |

In each scenario, also check that the proxy log explains every node it treated as down.
