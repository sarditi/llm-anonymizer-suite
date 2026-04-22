FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY ext-llm-gateway/logger ./ext-llm-gateway/logger
COPY ext-llm-gateway/models ./ext-llm-gateway/models
COPY ext-llm-gateway/utils ./ext-llm-gateway/utils
COPY ext-llm-gateway/go.mod ./ext-llm-gateway/go.mod


COPY llm-gemini-mediator/go.sum ./
COPY llm-gemini-mediator .
COPY llm-gemini-mediator/go.mod.docker ./go.mod

RUN cat go.mod
RUN go mod download

RUN go mod tidy

# Build binary
RUN go build -o server ./cmd/main.go

EXPOSE 9111

CMD ["./server"]

