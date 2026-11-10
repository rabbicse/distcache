FROM golang:1.27 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/distcache-server ./cmd/distcache-server

FROM gcr.io/distroless/static-debian12
LABEL org.opencontainers.image.title="DistCache" \
      org.opencontainers.image.description="Redis-compatible multi-core in-memory server written in Go" \
      org.opencontainers.image.source="https://github.com/rabbicse/distcache" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.authors="Md Mehedi Hasan <rabbi.se@gmail.com>"
COPY --from=build /out/distcache-server /distcache-server
EXPOSE 6379
VOLUME /data
ENTRYPOINT ["/distcache-server"]
CMD ["--dir","/data"]
