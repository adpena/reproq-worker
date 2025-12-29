FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o worker ./cmd/worker
RUN go build -o loadgen ./cmd/loadgen
RUN go build -o verify ./cmd/verify

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/worker .
COPY --from=builder /app/loadgen .
COPY --from=builder /app/verify .
COPY --from=builder /app/migrations ./migrations

CMD ["./worker"]
