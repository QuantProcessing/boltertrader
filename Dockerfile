FROM golang:1.26.0-alpine3.23@sha256:d4c4845f5d60c6a974c6000ce58ae079328d03ab7f721a0734277e69905473e5 AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG GOPROXY=https://proxy.golang.org,direct
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" GOPROXY="${GOPROXY}" \
    go build -trimpath -ldflags="-s -w -buildid=" \
    -o /out/exchange-gateway ./cmd/exchange-gateway && \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" GOPROXY="${GOPROXY}" \
    go build -trimpath -ldflags="-s -w -buildid=" \
    -o /out/exchange-gateway-docker-support ./cmd/exchange-gateway-docker-support

FROM gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

COPY --from=build --chown=nonroot:nonroot /out/exchange-gateway /usr/local/bin/exchange-gateway
COPY --from=build --chown=nonroot:nonroot /out/exchange-gateway-docker-support /usr/local/bin/exchange-gateway-docker-support

USER nonroot:nonroot
EXPOSE 18080

HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=6 \
  CMD ["/usr/local/bin/exchange-gateway-docker-support", "health", "-config", "/etc/boltertrader/gateway.yaml", "-kind", "ready"]

ENTRYPOINT ["/usr/local/bin/exchange-gateway-docker-support"]
CMD ["run", "-config", "/etc/boltertrader/gateway.yaml"]
