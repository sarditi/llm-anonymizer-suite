FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY ext-llm-common ./ext-llm-common
COPY ext-llm-gateway/go.sum ./ext-llm-gateway/
COPY ext-llm-gateway ./ext-llm-gateway
COPY ext-llm-gateway/go.mod.docker ./ext-llm-gateway/go.mod

WORKDIR /app/ext-llm-gateway
RUN cat go.mod
RUN go mod download


# Build binary
RUN go build -o server ./cmd/main.go

EXPOSE 7800

CMD ["./server"]

