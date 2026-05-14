# Stage 1: Build the Go binary
FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o scrape-lab ./cmd/scrape-lab
RUN CGO_ENABLED=0 GOOS=linux go build -o register-commands ./cmd/register-commands
RUN CGO_ENABLED=0 GOOS=linux go build -o cleanup-legacy-subscriptions ./cmd/cleanup-legacy-subscriptions

# Stage 2: Runtime with Playwright browsers
FROM mcr.microsoft.com/playwright:v1.52.0-noble

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        libasound2t64 \
        libgtk-3-0 \
        libx11-xcb1 \
        python3 \
        python3-pip \
        python3-venv \
        xauth \
        xvfb \
    && rm -rf /var/lib/apt/lists/*

RUN python3 -m venv /opt/scrape-venv \
    && /opt/scrape-venv/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/scrape-venv/bin/pip install --no-cache-dir camoufox nodriver crawl4ai fastembed \
    && /opt/scrape-venv/bin/python -m camoufox fetch

ENV PATH="/opt/scrape-venv/bin:${PATH}"

# Pre-install Chromium and Firefox during build to avoid startup latency.
RUN npx playwright install chromium firefox

WORKDIR /root/
COPY --from=builder /app/server .
COPY --from=builder /app/scrape-lab .
COPY --from=builder /app/register-commands .
COPY --from=builder /app/cleanup-legacy-subscriptions .
COPY --from=builder /app/internal/scraper/selectors.json ./internal/scraper/selectors.json
COPY --from=builder /app/scripts ./scripts

EXPOSE 8080

CMD ["./server"]
