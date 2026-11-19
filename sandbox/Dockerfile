# Live-hook sandbox: Go + Node + Claude Code, flow built from the mounted
# worktree at run time (see live-hooks.sh). Never mount the host's ~/.flow
# or ~/.claude into this container.
FROM golang:1.25
RUN apt-get update && apt-get install -y --no-install-recommends nodejs npm jq \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @anthropic-ai/claude-code \
 && useradd -m -s /bin/bash dev
ENV FLOW_NO_AUTO_UPGRADE=1
