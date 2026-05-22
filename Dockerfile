# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:alpine
ARG PLAYWRIGHT_IMAGE=mcr.microsoft.com/playwright:latest

# Stage 1: Build the Go binaries.
FROM ${GO_IMAGE} AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server \
    && CGO_ENABLED=0 GOOS=linux go build -o /out/scrape-lab ./cmd/scrape-lab \
    && CGO_ENABLED=0 GOOS=linux go build -o /out/register-commands ./cmd/register-commands \
    && CGO_ENABLED=0 GOOS=linux go build -o /out/cleanup-legacy-subscriptions ./cmd/cleanup-legacy-subscriptions

# Stage 2: Runtime with the Playwright browser image. The image already carries
# matching Playwright browsers; extra browser downloads are opt-in build args.
FROM ${PLAYWRIGHT_IMAGE}

ARG PY_BROWSER_PACKAGES="camoufox nodriver crawl4ai fastembed"
ARG PY_BROWSER_PACKAGE_REFRESH=latest
ARG INSTALL_CAMOUFOX_BROWSER=false
ARG INSTALL_PLAYWRIGHT_BROWSERS=false

ENV PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    PATH="/opt/scrape-venv/bin:${PATH}"

RUN apt-get update \
    && asound_package=libasound2 \
    && if apt-cache show libasound2t64 >/dev/null 2>&1; then asound_package=libasound2t64; fi \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        "${asound_package}" \
        libgtk-3-0 \
        libx11-xcb1 \
        python3 \
        python3-pip \
        python3-venv \
        xauth \
        xvfb \
    && rm -rf /var/lib/apt/lists/*

RUN --mount=type=cache,target=/root/.cache/pip \
    python3 -m venv /opt/scrape-venv \
    && /opt/scrape-venv/bin/pip install --upgrade pip \
    && echo "Refreshing Python browser packages: ${PY_BROWSER_PACKAGE_REFRESH}" \
    && /opt/scrape-venv/bin/pip install --upgrade ${PY_BROWSER_PACKAGES} \
    && if [ "${INSTALL_CAMOUFOX_BROWSER}" = "true" ]; then /opt/scrape-venv/bin/python -m camoufox fetch; fi \
    && if [ "${INSTALL_PLAYWRIGHT_BROWSERS}" = "true" ]; then /opt/scrape-venv/bin/python -m playwright install chromium firefox; fi

WORKDIR /root/
COPY --from=builder /out/server .
COPY --from=builder /out/scrape-lab .
COPY --from=builder /out/register-commands .
COPY --from=builder /out/cleanup-legacy-subscriptions .
COPY --from=builder /app/internal/scraper/selectors.json ./internal/scraper/selectors.json
COPY --from=builder /app/scripts ./scripts

EXPOSE 8080

CMD ["./server"]
