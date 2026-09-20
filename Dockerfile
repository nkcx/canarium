# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Frontend: build the Svelte UI that gets embedded into the Go binary.
#
# Pinned to BUILDPLATFORM because the output is architecture-independent
# static assets; running node under QEMU for a foreign target would be pure
# waste.
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend
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
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS backend

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev

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
FROM alpine:3.21

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
 && chmod 0750 /var/lib/canarium

COPY --from=backend /out/canarium /usr/local/bin/canarium

USER canarium:canarium

EXPOSE 8420
VOLUME ["/var/lib/canarium"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8420/api/health >/dev/null || exit 1

ENTRYPOINT ["canarium"]
CMD ["run", "--config", "/etc/canarium/config.yaml"]
