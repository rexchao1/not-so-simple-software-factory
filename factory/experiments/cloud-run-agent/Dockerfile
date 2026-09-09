FROM node:22.19.0-bookworm-slim

ARG PI_VERSION=0.80.10

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        jq \
    && rm -rf /var/lib/apt/lists/*

RUN npm install --global "@earendil-works/pi-coding-agent@${PI_VERSION}"

RUN mkdir -p /workspace \
    && chown node:node /workspace

COPY --chown=node:node run-agent.sh /usr/local/bin/run-agent
RUN chmod 0755 /usr/local/bin/run-agent

ENV PI_SKIP_VERSION_CHECK=1 \
    PI_TELEMETRY=0

USER node
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/run-agent"]
