# Build stage
FROM golang:1.25-alpine AS build
WORKDIR /src

COPY ext-llm-common ./ext-llm-common

COPY ext-llm-webadapter ./ext-llm-webadapter
COPY ext-llm-webadapter/go.mod.docker ./ext-llm-webadapter/go.mod

WORKDIR /src/ext-llm-webadapter
RUN go mod download || true
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w -X main.version=$(date +%Y%m%d)" -o /out/ext-llm-webadapter ./cmd
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -o /out/hashpw ./cmd/hashpw

# Runtime stage
FROM alpine:3.20
RUN adduser -D -H webadapter && apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/ext-llm-webadapter /app/ext-llm-webadapter
COPY --from=build /out/hashpw /app/hashpw
COPY ext-llm-webadapter/conf/config.json /app/conf/config.json
USER webadapter
EXPOSE 9555
ENV WEBADAPTER_CONFIG=/app/conf/config.json
ENTRYPOINT ["/app/ext-llm-webadapter"]
