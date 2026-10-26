# The tools CI uses, at the same versions: Go from go.mod, golangci-lint from ci.yml.
FROM golangci/golangci-lint:v2.15.0@sha256:fbfcfd12a6f8d7f1b97b3362ca80fc2a244d46863148c73cb88e7793af96c481 AS lint

FROM golang:1.27@sha256:f20d83a83e18dbd2a4714b8ffd4b30b287b6243962dd75608e86abcc63ba4efc
COPY --from=lint /usr/bin/golangci-lint /usr/local/bin/golangci-lint
