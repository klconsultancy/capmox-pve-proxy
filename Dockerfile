# The Go image is the one in .devcontainer/Dockerfile; Dependabot keeps both current.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:820153096fda4de6ac1b7194d12e416b7dd8f4e9a33d17e0345b111b945162d8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /capmox-pve-proxy ./cmd/capmox-pve-proxy

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /capmox-pve-proxy /capmox-pve-proxy
EXPOSE 8006
ENTRYPOINT ["/capmox-pve-proxy"]
