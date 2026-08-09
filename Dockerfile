# Image for a hosted instance: `vkstack serve`, read-only, refreshing on a schedule.
#
# The binary is already static and cgo-free, so cross-compiling for the target arch is
# honest and needs no emulator: the build stage always runs natively on BUILDPLATFORM
# and only GOARCH changes. That is why this is a plain `go build` rather than a QEMU
# multi-arch build.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build

WORKDIR /src

# Dependencies first, so source edits do not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
# VERSION lands in `vkstack --version` and in the `tool` field of every JSON envelope,
# so an answer can be traced back to the build that produced it. CI passes the tag.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/vkstack ./cmd/vkstack

# The cache directory has to exist owned by the runtime user: distroless has no shell to
# create it with, and /var is not writable by nonroot.
RUN install -d -o 65532 -g 65532 /out/cache

# distroless/static carries ca-certificates, which `refresh` needs to reach upstream over
# HTTPS, and nothing else — no shell, no package manager.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/vkstack /usr/local/bin/vkstack
COPY --from=build --chown=65532:65532 /out/cache /var/cache/vkstack

# The SQLite cache lives here. Mount a volume over it to keep data across restarts;
# without one it is refetched on start, which takes about a minute.
ENV XDG_CACHE_HOME=/var/cache
VOLUME ["/var/cache/vkstack"]

EXPOSE 8080
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/vkstack"]
# Hosted defaults: reachable from outside the container, clients cannot trigger an
# upstream refresh, and the server keeps itself current. Override to run any other
# subcommand, e.g. `docker run --rm IMAGE stack vcenter 8.0U3k`.
CMD ["serve", "--bind", "0.0.0.0", "--port", "8080", "--read-only", "--refresh-interval", "6h"]
