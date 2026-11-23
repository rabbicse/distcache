# DistCache container image.
#
#   docker build -f deploy/docker/Dockerfile -t distcache:dev .       (make docker)
#   docker run --rm -p 6379:6379 -v distcache-data:/data distcache:dev
#
# The build stage runs on the build machine's own architecture and
# cross-compiles, so a multi-architecture build (make docker-multiarch) needs
# no emulation. The result is one static binary on a distroless base, running
# as an unprivileged user with /data as its only writable path.
FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build
ARG TARGETOS
ARG TARGETARCH
# Optional: --build-arg VERSION=1.2.0 stamps the version the server reports.
ARG VERSION=
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w ${VERSION:+-X github.com/rabbicse/distcache/internal/server.Version=$VERSION}" \
      -o /out/distcache-server ./cmd/distcache-server \
    && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="DistCache" \
      org.opencontainers.image.description="Redis-compatible multi-core in-memory server written in Go" \
      org.opencontainers.image.source="https://github.com/rabbicse/distcache" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.authors="Md Mehedi Hasan <rabbi.se@gmail.com>"
COPY --from=build /out/distcache-server /distcache-server
# The data directory belongs to the unprivileged user (uid 65532, "nonroot").
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
EXPOSE 6379
VOLUME /data
ENTRYPOINT ["/distcache-server"]
# Inside a container every client arrives from a non-loopback address, so the
# published port is the access control: protected mode is off here. Set
# --requirepass for anything beyond local use.
CMD ["--dir","/data","--protected-mode","no"]
