# Contributing

Thanks for your interest. Before you start, read the [scope](README.md#scope): the proxy covers only the part of the Proxmox API that CAPMOX calls, and changes outside that will be declined.

## Before you write code

Open an issue first for anything beyond a small fix, so we can agree on the approach before you spend time on it. Report security issues privately as described in [SECURITY.md](SECURITY.md).

## Development

You need the Go version from [go.mod](go.mod) and [golangci-lint](https://golangci-lint.run) v2. The [dev container](.devcontainer/devcontainer.json) has both, at the versions CI uses: open the repository in it from your editor, or start it with the [Dev Container CLI](https://github.com/devcontainers/cli) (`devcontainer up --workspace-folder .`) and prefix the commands below with `devcontainer exec --workspace-folder .`.

```sh
go build ./...
go test -race ./...
golangci-lint run
```

## Pull requests

- Keep a pull request to one change, with tests.
- CI (lint, tests, dependency review and CodeQL) must pass before merging.
- Pull requests are squash-merged, so the pull request title becomes the commit message on `main`. Write it as a short imperative sentence.

By contributing you agree that your contribution is licensed under the [Apache-2.0 license](LICENSE), and you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
