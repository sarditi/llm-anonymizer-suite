FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY ext-llm-gateway/go.sum ./
COPY ext-llm-gateway .
COPY ext-llm-gateway/go.mod.docker ./go.mod

RUN cat go.mod
RUN go mod download


# Build binary
RUN go build -o server ./cmd/main.go

EXPOSE 7800

CMD ["./server"]

