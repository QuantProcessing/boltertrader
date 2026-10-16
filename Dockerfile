# One image, two processes: gateway and trader (which also serves the web app).

FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    for cmd in gateway trader; do \
      CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/$cmd ./cmd/$cmd; \
    done
# A named volume mounted at /data copies this directory's ownership, so the
# nonroot user (65532) can write state without running as root.
RUN mkdir -p /data/gateway /data/trader && chown -R 65532:65532 /data && chmod -R 0700 /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /out/ /usr/local/bin/
COPY --from=web /web/dist /srv/web
COPY --from=go --chown=65532:65532 /data /data
ENV TRADER_WEB_DIR=/srv/web
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/gateway"]
