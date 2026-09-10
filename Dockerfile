FROM golang:1.26.0-alpine3.23@sha256:d4c4845f5d60c6a974c6000ce58ae079328d03ab7f721a0734277e69905473e5 AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://proxy.golang.org,direct

# One image carries every process in the stack. The alternative — an image per
# service — would rebuild the same module graph five times and let the five
# copies drift apart in Go version and dependency set, which is precisely the
# skew that makes a "works in gateway, fails in trading" bug expensive. Compose
# selects the process by overriding the entrypoint; the image stays one artifact.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    for target in \
      exchange-gateway \
      exchange-gateway-docker-support \
      gateway-migrate \
      gateway-market \
      gateway-monitor \
      trading \
      trading-migrate \
      authedge \
      authedge-migrate \
    ; do \
      CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" GOPROXY="${GOPROXY}" \
      go build -trimpath -ldflags="-s -w -buildid=" \
        -o "/out/${target}" "./cmd/${target}"; \
    done

FROM gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

COPY --from=build --chown=nonroot:nonroot /out/ /usr/local/bin/

USER nonroot:nonroot
EXPOSE 18080 18091 18092

# The default process stays the Gateway so the image behaves exactly as it did
# when it held only that binary; the other services override both of these.
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=6 \
  CMD ["/usr/local/bin/exchange-gateway-docker-support", "health", "-config", "/etc/boltertrader/gateway.yaml", "-kind", "ready"]

ENTRYPOINT ["/usr/local/bin/exchange-gateway-docker-support"]
CMD ["run", "-config", "/etc/boltertrader/gateway.yaml"]
