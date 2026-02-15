# Conduit - Psiphon inproxy node (Debian)
#
# Build with embedded config:
#   make docker PSIPHON_CONFIG=psiphon_config.json
#
# Or build image only (after binaries exist in dist/):
#   docker build -t conduit:latest -f Dockerfile .
#
# Run with persistent data volume:
#   docker run -d --name conduit \
#     -v conduit-data:/home/conduit/data \
#     --restart unless-stopped \
#     conduit:latest

FROM debian:bookworm-slim

ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY dist/conduit-linux-${TARGETARCH} /usr/local/bin/conduit
COPY dist/conduit-monitor-linux-${TARGETARCH} /usr/local/bin/conduit-monitor

RUN chmod +x /usr/local/bin/conduit /usr/local/bin/conduit-monitor

# Create non-root user with home directory
RUN useradd -r -u 1000 -m conduit
USER conduit

WORKDIR /home/conduit

# Create data directory owned by conduit user
RUN mkdir -p /home/conduit/data

ENTRYPOINT ["conduit"]
CMD ["start", "--data-dir", "/home/conduit/data"]
