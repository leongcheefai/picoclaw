# ============================================================
# Stage 1: Build the picoclaw binary
# ============================================================
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN make build

# ============================================================
# Stage 2: Minimal runtime image
# ============================================================
FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata curl su-exec

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q --spider http://localhost:${PICOCLAW_GATEWAY_PORT:-18790}/health || exit 1

# Copy binary
COPY --from=builder /src/build/picoclaw /usr/local/bin/picoclaw

# Create non-root user and group
RUN addgroup -g 1000 picoclaw && \
    adduser -D -u 1000 -G picoclaw picoclaw && \
    mkdir -p /data && chown picoclaw:picoclaw /data

# Set PICOCLAW_HOME so config, workspace, and skills all resolve to /data
# regardless of what the user's home directory is. This is the canonical
# location for Railway (persistent volume) and docker-compose (bind mount).
ENV PICOCLAW_HOME=/data

# Run onboard to create initial directories, config, and workspace templates
USER picoclaw
RUN /usr/local/bin/picoclaw onboard
USER root

# Stage the workspace and config in a known image-only path so they survive
# persistent volume mounts. The entrypoint syncs them to /data on every boot.
COPY --from=builder --chown=picoclaw:picoclaw /src/config/config.json /opt/picoclaw/config.json
COPY --from=builder --chown=picoclaw:picoclaw /src/workspace/ /opt/picoclaw/workspace/

# Railway injects PORT; map it to picoclaw env vars if not already set.
# Start as root to fix /data volume permissions, then drop to picoclaw.
# Sync workspace and config from the image to /data on every boot so that
# new skills, AGENT.md changes, etc. take effect even with a persistent volume.
ENTRYPOINT ["/bin/sh", "-c", "\
  if [ -d /data ]; then chown -R picoclaw:picoclaw /data; fi && \
  cp -f /opt/picoclaw/config.json /data/config.json && \
  mkdir -p /data/workspace && \
  cp -rf /opt/picoclaw/workspace/* /data/workspace/ && \
  chown -R picoclaw:picoclaw /data && \
  export PICOCLAW_GATEWAY_HOST=${PICOCLAW_GATEWAY_HOST:-0.0.0.0} \
         PICOCLAW_GATEWAY_PORT=${PICOCLAW_GATEWAY_PORT:-${PORT:-18790}} && \
  exec su-exec picoclaw picoclaw gateway --allow-empty 2>&1\
"]
