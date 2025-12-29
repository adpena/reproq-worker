FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o reproq ./cmd/reproq

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/reproq .
COPY --from=builder /app/migrations ./migrations

CMD ["./reproq", "worker"]
