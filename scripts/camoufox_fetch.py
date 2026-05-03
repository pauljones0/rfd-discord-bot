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

from camoufox.sync_api import Camoufox


def truthy(value: str | None) -> bool:
    return value is not None and value.strip().lower() in {"1", "true", "yes", "y"}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("url", nargs="?", default=os.getenv("SCRAPELAB_TARGET_URL", ""))
    parser.add_argument("--headless", action="store_true", default=truthy(os.getenv("CAMOUFOX_HEADLESS")))
    parser.add_argument("--wait-ms", type=float, default=float(os.getenv("CAMOUFOX_WAIT_MS", "5000")))
    parser.add_argument("--timeout-ms", type=float, default=float(os.getenv("CAMOUFOX_TIMEOUT_MS", "60000")))
    args = parser.parse_args()

    if not args.url:
        print("target URL is required", file=sys.stderr)
        return 2

    with Camoufox(
        headless=args.headless,
        os="windows",
        locale="en-CA",
        humanize=True,
        block_webrtc=True,
        window=(1600, 1000),
    ) as browser:
        page = browser.new_page()
        page.goto(args.url, wait_until="domcontentloaded", timeout=args.timeout_ms)
        page.wait_for_timeout(args.wait_ms)
        html = page.content()

    sys.stdout.buffer.write(html.encode("utf-8", errors="replace"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
