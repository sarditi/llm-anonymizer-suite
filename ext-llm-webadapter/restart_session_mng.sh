#!/usr/bin/env bash
# Local rebuild + restart for ext-llm-webadapter against an existing
# my-redis2 container and an ext-llm-gateway already listening on host port 7700.
#
# Usage: ./restart_session_mng.sh
set -euo pipefail

NAME=ext-llm-webadapter
PORT=9555

cd "$(dirname "$0")/.."

docker stop "$NAME" 2>/dev/null || true
docker rm   "$NAME" 2>/dev/null || true

docker build -t "$NAME" -f ext-llm-webadapter/Dockerfile .

docker run -d \
    --name "$NAME" \
    --link my-redis2:redis \
    -p "$PORT:$PORT" \
    "$NAME"

echo "Started $NAME on port $PORT"
docker logs --tail 20 "$NAME"
