# Changelog

## [0.2.0](https://github.com/klconsultancy/capmox-pve-proxy/compare/v0.1.1...v0.2.0) (2026-11-07)


### Features

* ship a Helm chart, published as an OCI artifact with the image ([#46](https://github.com/klconsultancy/capmox-pve-proxy/issues/46)) ([2383957](https://github.com/klconsultancy/capmox-pve-proxy/commit/2383957e0cf73207bfc296a688d965b83b684163))


### Bug Fixes

* answer kubelet probes on /healthz and log server errors through slog ([#44](https://github.com/klconsultancy/capmox-pve-proxy/issues/44)) ([20625e8](https://github.com/klconsultancy/capmox-pve-proxy/commit/20625e8b9c33a80df4d340b01452831ebad86dff))

## [0.1.1](https://github.com/klconsultancy/capmox-pve-proxy/compare/v0.1.0...v0.1.1) (2026-11-04)


### Bug Fixes

* **deps:** update image ([#34](https://github.com/klconsultancy/capmox-pve-proxy/issues/34)) ([256b817](https://github.com/klconsultancy/capmox-pve-proxy/commit/256b81712383f91e54fefb851271811fbd918171))
* forward GET /nodes/{node}/tasks, which CAPMOX v0.9.2 calls ([#37](https://github.com/klconsultancy/capmox-pve-proxy/issues/37)) ([6df7186](https://github.com/klconsultancy/capmox-pve-proxy/commit/6df718633e70f77125b3a8c8bffe05cbfbf2affe))

## 0.1.0 (2026-11-02)


### Bug Fixes

* keep a started clone taken until its node lists it ([#3](https://github.com/klconsultancy/capmox-pve-proxy/issues/3)) ([c8cdb17](https://github.com/klconsultancy/capmox-pve-proxy/commit/c8cdb1783ac022ba474ebe6fbbea7e8d317a209e))
* log a node the VMID refresh cannot list ([#25](https://github.com/klconsultancy/capmox-pve-proxy/issues/25)) ([98d1b20](https://github.com/klconsultancy/capmox-pve-proxy/commit/98d1b2041e604c73ca95bdb244a16e8fcf590e2f))


### Continuous Integration

* release with release-please, as python-thermia-genesis does ([#11](https://github.com/klconsultancy/capmox-pve-proxy/issues/11)) ([2bf7fb7](https://github.com/klconsultancy/capmox-pve-proxy/commit/2bf7fb720071bc6fcf37d2781033a7c9e80230c3))
