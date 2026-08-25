# The browser's entry point: the built frontend, plus the one reverse proxy that
# makes it same-origin with the auth edge.
#
# This is a second image rather than a seventh binary in the Go one on purpose.
# The bundle asks for absolute paths — `/api/v1/...` — so whatever serves it must
# also answer those, and the auth edge's session cookie is `SameSite=Strict` with
# no Domain, which means a cross-origin frontend would have the cookie withheld
# on exactly the navigations that matter. A proxy in front reproduces the vite
# dev server's own `/api` proxy, so the app runs identically in both places and
# the auth edge stays what the plan scopes it to: session termination and path
# forwarding.

FROM node:22-alpine AS build
WORKDIR /src

# The lockfile alone first, so a source edit does not re-resolve the dependency
# tree. `npm ci` installs @playwright/test, which pulls in `playwright` and its
# browser-downloading postinstall — half a gigabyte this image never runs, on a
# libc it does not support. The build needs the package only because
# `tsconfig.json` type-checks `e2e/`.
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund

COPY web/ ./
# `npm run build` is `tsc --noEmit && vite build`, so a type error fails the
# image rather than shipping a bundle that only happens to run.
RUN npm run build

# -----------------------------------------------------------------------------
# Runtime: Caddy, with no configuration of its own.
#
# The Caddyfile is bind-mounted by compose rather than copied in, matching how
# the gateway receives its yaml — the routing table is deployment configuration,
# and an image rebuild is the wrong unit of change for it.
# -----------------------------------------------------------------------------
FROM caddy:2.10-alpine

# Caddy writes an autosaved config and its data directory on startup. The stack
# runs every container read-only with a tmpfs at /tmp, so both are pointed there;
# `persist_config off` in the Caddyfile makes the first one moot as well, which
# is belt and braces on purpose — the failure mode is a container that will not
# start, reported as a permission error on a path nobody configured.
ENV XDG_CONFIG_HOME=/tmp XDG_DATA_HOME=/tmp

# The upstream image sets cap_net_bind_service on the binary so it can bind :80.
# This container binds :8080 and runs under `no-new-privileges` with every
# capability dropped, and the kernel refuses to exec a file that carries file
# capabilities under that combination — `exec /usr/bin/caddy: operation not
# permitted`, before Caddy prints a line of its own. Dropping the capability the
# image cannot use is what makes the hardening anchor in compose.yaml apply here.
RUN setcap -r /usr/bin/caddy

COPY --from=build /src/dist /srv

EXPOSE 8080
