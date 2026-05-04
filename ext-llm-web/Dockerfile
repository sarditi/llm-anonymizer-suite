# syntax=docker/dockerfile:1.7
#
# Three-stage build:
#   1. flutter-build  → compiles the Flutter web bundle (HTML/JS/CSS) into /out
#   2. server-build   → compiles the tiny Go static-file server into /out/server
#   3. runtime        → minimal alpine + the binary + the bundle
#
# Build context is the suite root (so `ext-llm-web/...` paths work the same
# way the other services in this repo expect):
#
#   docker build -t ext-llm-web -f ext-llm-web/Dockerfile .

# --- Stage 1: build the Flutter web bundle -----------------------------------
FROM debian:bookworm-slim AS flutter-build
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git unzip xz-utils libglu1-mesa \
    && rm -rf /var/lib/apt/lists/*

# Pin the Flutter SDK so builds are reproducible. Bump intentionally.
ARG FLUTTER_VERSION=3.24.5
RUN git clone --depth 1 --branch ${FLUTTER_VERSION} \
        https://github.com/flutter/flutter.git /opt/flutter
ENV PATH="/opt/flutter/bin:/opt/flutter/bin/cache/dart-sdk/bin:${PATH}"

# Flutter refuses to run as root unless you opt in explicitly; in CI/Docker
# this is fine.
RUN git config --global --add safe.directory /opt/flutter \
    && flutter config --no-analytics \
    && flutter precache --web

WORKDIR /src
COPY ext-llm-web/pubspec.yaml ./pubspec.yaml
COPY ext-llm-web/analysis_options.yaml ./analysis_options.yaml
RUN flutter pub get

COPY ext-llm-web/lib ./lib
COPY ext-llm-web/web ./web
RUN flutter build web --release \
    && mkdir -p /out \
    && cp -r build/web/. /out/

# --- Stage 2: build the static-file server -----------------------------------
FROM golang:1.23-alpine AS server-build
WORKDIR /src
COPY ext-llm-web/server ./server
RUN cd server && CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -o /out/server .

# --- Stage 3: runtime --------------------------------------------------------
FROM alpine:3.20
RUN adduser -D -H -u 10001 webuser && apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=server-build /out/server /app/server
COPY --from=flutter-build /out /app/web

USER webuser

ENV PORT=8080 \
    STATIC_DIR=/app/web \
    WEBADAPTER_BASE_URL=http://localhost:9555 \
    APP_TITLE=ext-llm-web

EXPOSE 8080
ENTRYPOINT ["/app/server"]
