# Builder stage
FROM golang:1.20 as builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /snapshot-service ./cmd/main.go

# Runtime stage
FROM alpine:latest

WORKDIR /

COPY --from=builder /snapshot-service  /snapshot-service 

CMD ["/snapshot-service", "--config=", "config.json"]
