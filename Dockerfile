# Stage 1: Build the Go binary
FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server

# Stage 2: Runtime with Playwright + Firefox
FROM mcr.microsoft.com/playwright:v1.52.0-noble

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /root/
COPY --from=builder /app/server .
COPY --from=builder /app/internal/scraper/selectors.json ./internal/scraper/selectors.json

# Pre-install Firefox browser for Playwright during build to avoid startup latency.
RUN npx playwright install firefox

EXPOSE 8080

CMD ["./server"]
