#!/usr/bin/env bash
# Stop, remove, rebuild and re-run the ext-llm-web container.
# Mirrors the pattern used by the other services in this suite.
#
# Run from the suite root (the build context must contain ext-llm-web/).

set -euo pipefail

NAME="ext-llm-web"
IMAGE="ext-llm-web"
HOST_PORT="${HOST_PORT:-8766}"
# IMPORTANT: this URL is consumed by the *browser*, not by the container, so
# it must be resolvable from your laptop. host.docker.internal is only valid
# from inside a container — using it here breaks the SPA with
# ERR_NAME_NOT_RESOLVED. Default to localhost (works as long as the
# webadapter is published on host port 9555).
WEBADAPTER_BASE_URL="${WEBADAPTER_BASE_URL:-http://localhost:9555}"
APP_TITLE="${APP_TITLE:-ext-llm-web}"

docker stop "${NAME}"   2>/dev/null || true
docker rm   "${NAME}"   2>/dev/null || true
docker rmi  "${IMAGE}"  2>/dev/null || true

docker build -t "${IMAGE}" -f ext-llm-web/Dockerfile .

docker run -d \
    --name  "${NAME}" \
    -p "${HOST_PORT}:8080" \
    --add-host=host.docker.internal:host-gateway \
    -e "WEBADAPTER_BASE_URL=${WEBADAPTER_BASE_URL}" \
    -e "APP_TITLE=${APP_TITLE}" \
    "${IMAGE}"

echo "Open http://localhost:${HOST_PORT}"
