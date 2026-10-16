# syntax=docker/dockerfile:1

# BuildKit (docker, buildx) and buildah (podman) set BUILDPLATFORM, TARGETOS and TARGETARCH. A
# plain build sets them to the host, so it builds for the host.

# The UI is platform independent: build it once on the build platform.
FROM --platform=$BUILDPLATFORM oven/bun:1.4.3-alpine AS web
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

# Go cross-compiles, so this stage runs on the build platform and never under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine3.24 AS build
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
# The server embeds the six CLI archives of the same source (internal/clidist), which it serves
# at /cli/ and its install scripts download. With a release VERSION, "go tool task cli" first
# writes it into each "dev" entry of changes.toml, so the CLI and the server embed the stamped
# change log.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go tool task cli "${VERSION}" \
 && CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-$(go env GOARCH)}" go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/telesphorepo-server ./cmd/telesphorepo-server \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/telesphorepo-server /telesphorepo-server
COPY --from=build --chown=65532:65532 /out/data /data
ENV TR_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/telesphorepo-server"]
CMD ["serve"]
