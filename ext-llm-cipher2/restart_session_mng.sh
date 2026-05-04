#!/usr/bin/env bash
# Stop, remove, rebuild and re-run the ext-llm-cipher2 container.
# Mirrors the pattern used by the other services in this suite.

set -euo pipefail

NAME="${NAME:-ext-llm-cipherer2}"
IMAGE="${IMAGE:-ext-llm-cipher2}"
HOST_PORT="${HOST_PORT:-8211}"
REDIS_LINK="${REDIS_LINK:-my-redis2:redis}"
CIPHER_TAGGERS="${CIPHER_TAGGERS:-credit_card,address,person}"
LOG_LEVEL="${LOG_LEVEL:-INFO}"

docker stop  "${NAME}" 2>/dev/null || true
docker rm    "${NAME}" 2>/dev/null || true

docker build -t "${IMAGE}" .

docker run -d \
    --name "${NAME}" \
    --link "${REDIS_LINK}" \
    -p "${HOST_PORT}:8000" \
    -e "CIPHER_TAGGERS=${CIPHER_TAGGERS}" \
    -e "LOG_LEVEL=${LOG_LEVEL}" \
    "${IMAGE}"

echo "ext-llm-cipher2 listening on http://localhost:${HOST_PORT}"
