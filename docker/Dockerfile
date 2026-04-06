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

# Run onboard as picoclaw user to create initial directories and config
USER picoclaw
RUN /usr/local/bin/picoclaw onboard
USER root

# Copy custom config (uses env:// references for secrets, no cache)
COPY --from=builder --chown=picoclaw:picoclaw /src/config/config.json /home/picoclaw/.picoclaw/config.json

# Copy workspace (AGENT.md, SOUL.md, skills, etc.)
COPY --from=builder --chown=picoclaw:picoclaw /src/workspace/ /home/picoclaw/.picoclaw/workspace/

# Railway injects PORT; map it to picoclaw env vars if not already set.
# Start as root to fix /data volume permissions, then drop to picoclaw.
ENTRYPOINT ["/bin/sh", "-c", "\
  if [ -d /data ]; then chown -R picoclaw:picoclaw /data; fi && \
  export PICOCLAW_GATEWAY_HOST=${PICOCLAW_GATEWAY_HOST:-0.0.0.0} \
         PICOCLAW_GATEWAY_PORT=${PICOCLAW_GATEWAY_PORT:-${PORT:-18790}} && \
  exec su-exec picoclaw picoclaw gateway --allow-empty 2>&1\
"]
