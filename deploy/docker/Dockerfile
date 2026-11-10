FROM golang:1.27 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/distcache-server ./cmd/distcache-server

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/distcache-server /distcache-server
EXPOSE 6379
VOLUME /data
ENTRYPOINT ["/distcache-server"]
CMD ["--dir","/data"]
