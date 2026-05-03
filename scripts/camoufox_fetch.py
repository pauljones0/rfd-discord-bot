#!/usr/bin/env python
"""Fetch one page with Camoufox and print final HTML to stdout.

This is a scrape-lab external-stealth prototype. Install with:

    python -m pip install camoufox
    python -m camoufox fetch
"""

from __future__ import annotations

import argparse
import os
import sys
import time

from camoufox.sync_api import Camoufox


def truthy(value: str | None) -> bool:
    return value is not None and value.strip().lower() in {"1", "true", "yes", "y"}


def env_float(name: str, default: float) -> float:
    value = os.getenv(name)
    if not value:
        return default
    try:
        return float(value)
    except ValueError:
        print(f"invalid {name}={value!r}; using {default}", file=sys.stderr)
        return default


def parse_window(value: str) -> tuple[int, int]:
    parts = value.lower().replace("x", ",").split(",", 1)
    if len(parts) != 2:
        raise argparse.ArgumentTypeError("window must be WIDTHxHEIGHT")
    try:
        width = int(parts[0].strip())
        height = int(parts[1].strip())
    except ValueError as exc:
        raise argparse.ArgumentTypeError("window must be WIDTHxHEIGHT") from exc
    if width <= 0 or height <= 0:
        raise argparse.ArgumentTypeError("window dimensions must be positive")
    return width, height


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("url", nargs="?", default=os.getenv("SCRAPELAB_TARGET_URL", ""))
    parser.add_argument("--headless", action="store_true", default=truthy(os.getenv("CAMOUFOX_HEADLESS")))
    parser.add_argument("--wait-ms", type=float, default=env_float("CAMOUFOX_WAIT_MS", 5000))
    parser.add_argument("--timeout-ms", type=float, default=env_float("CAMOUFOX_TIMEOUT_MS", 60000))
    parser.add_argument("--locale", default=os.getenv("CAMOUFOX_LOCALE", "en-CA"))
    parser.add_argument("--os", default=os.getenv("CAMOUFOX_OS", "windows"))
    parser.add_argument("--window", type=parse_window, default=parse_window(os.getenv("CAMOUFOX_WINDOW", "1600x1000")))
    args = parser.parse_args()

    if not args.url:
        print("target URL is required", file=sys.stderr)
        return 2

    start = time.monotonic()
    print(
        f"camoufox_fetch url={args.url!r} headless={args.headless} "
        f"locale={args.locale!r} os={args.os!r} wait_ms={args.wait_ms}",
        file=sys.stderr,
    )
    try:
        with Camoufox(
            headless=args.headless,
            os=args.os,
            locale=args.locale,
            humanize=True,
            block_webrtc=True,
            window=args.window,
        ) as browser:
            page = browser.new_page()
            page.goto(args.url, wait_until="domcontentloaded", timeout=args.timeout_ms)
            page.wait_for_timeout(args.wait_ms)
            html = page.content()
    except Exception as exc:
        print(f"camoufox_fetch failed: {exc}", file=sys.stderr)
        return 1

    sys.stdout.buffer.write(html.encode("utf-8", errors="replace"))
    print(f"camoufox_fetch ok duration_ms={int((time.monotonic() - start) * 1000)}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
