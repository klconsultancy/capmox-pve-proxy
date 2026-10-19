# Security policy

## Supported versions

No release has been published yet. Once there are releases, only the latest one receives security fixes.

## Reporting a vulnerability

Report vulnerabilities privately through [GitHub's private vulnerability reporting](https://github.com/klconsultancy/capmox-pve-proxy/security/advisories/new), not in a public issue.

Include what an attacker can do, the steps to reproduce it, and the affected version or commit. You will get an answer within a week. Once a fix is released, the advisory is published with credit to you unless you prefer otherwise.

The proxy holds API tokens for every Proxmox node it fronts, so issues that let a client reach a node without the proxy's own authentication, or read a node's token, are treated as the most severe.
