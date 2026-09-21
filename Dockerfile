# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Frontend: build the Svelte UI that gets embedded into the Go binary.
#
# Pinned to BUILDPLATFORM because the output is architecture-independent
# static assets; running node under QEMU for a foreign target would be pure
# waste.
# ---------------------------------------------------------------------------
# Node 24 is the current LTS. Dependabot proposed 26, which is still
# "Current" -- a line that stops getting fixes the moment 28 ships. A build
# toolchain is the last place to be on a release train that ends early.
FROM --platform=$BUILDPLATFORM node:24.21-alpine AS frontend
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---------------------------------------------------------------------------
# Backend: compile the daemon.
#
# Canarium uses a pure-Go SQLite driver, so CGO is not needed and the Go
# toolchain can cross-compile natively on the build host. This avoids
# emulating the entire toolchain under QEMU for arm/arm64 targets.
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27.0-alpine AS backend

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev

# Use the toolchain in the image rather than downloading one mid-build.
# go.mod pins a toolchain version; if it ever exceeds what this base image
# provides, the build should fail loudly here rather than reaching out to the
# network and producing a binary nobody chose.
ENV GOTOOLCHAIN=local

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
COPY --from=frontend /app/web/dist ./web/dist

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    GOARM=$(echo "${TARGETVARIANT}" | tr -d 'v') \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/canarium ./cmd/canarium

# ---------------------------------------------------------------------------
# Runtime
# ---------------------------------------------------------------------------
# Pinned to the series, not a patch: a rebuild should pick up security
# fixes without waiting for someone to notice and bump a third digit.
FROM alpine:3.24

# ca-certificates: transports talk to HTTPS APIs (Proxmox, TrueNAS, OPNsense)
# and must be able to verify their certificates.
RUN apk add --no-cache ca-certificates tzdata

# Run as an unprivileged user. Canarium holds credentials to an entire fleet;
# it has no reason to run as root. UID/GID are fixed so bind-mounted data
# directories have predictable ownership.
RUN addgroup -g 65532 -S canarium \
 && adduser -u 65532 -S -G canarium -H -s /sbin/nologin canarium \
 && mkdir -p /var/lib/canarium /etc/canarium \
 && chown -R canarium:canarium /var/lib/canarium \
 && chmod 0700 /var/lib/canarium

COPY --from=backend /out/canarium /usr/local/bin/canarium

USER canarium:canarium

EXPOSE 8420
VOLUME ["/var/lib/canarium"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8420/api/health >/dev/null || exit 1

ENTRYPOINT ["canarium"]
CMD ["run", "--config", "/etc/canarium/config.yaml"]
