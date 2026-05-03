#!/usr/bin/env python3
"""Fetch one page with Crawl4AI Magic Mode and print raw HTML to stdout."""

from __future__ import annotations

import argparse
import asyncio
import sys
import time

from crawl4ai import AsyncWebCrawler, BrowserConfig, CacheMode, CrawlerRunConfig


async def fetch(args: argparse.Namespace) -> str:
    browser_cfg = BrowserConfig(
        browser_type=args.browser,
        headless=args.headless,
        viewport_width=args.width,
        viewport_height=args.height,
        user_agent=args.user_agent or None,
        locale=args.locale,
        enable_stealth=True,
    )
    run_cfg = CrawlerRunConfig(
        cache_mode=CacheMode.BYPASS,
        magic=True,
        simulate_user=True,
        override_navigator=True,
        delay_before_return_html=args.wait_ms / 1000.0,
        page_timeout=args.timeout_ms,
    )
    async with AsyncWebCrawler(config=browser_cfg) as crawler:
        result = await crawler.arun(url=args.url, config=run_cfg)
        if not result.success:
            raise RuntimeError(result.error_message or "crawl failed")
        return result.html or result.cleaned_html or ""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("url")
    parser.add_argument("--wait-ms", type=int, default=5000)
    parser.add_argument("--timeout-ms", type=int, default=30000)
    parser.add_argument("--headless", action="store_true")
    parser.add_argument("--browser", default="chromium", choices=["chromium", "firefox", "webkit"])
    parser.add_argument("--locale", default="en-CA")
    parser.add_argument("--width", type=int, default=1600)
    parser.add_argument("--height", type=int, default=1000)
    parser.add_argument("--user-agent", default="")
    args = parser.parse_args()

    start = time.monotonic()
    try:
        html = asyncio.run(fetch(args))
    except Exception as exc:
        print(f"crawl4ai_fetch failed: {exc}", file=sys.stderr)
        return 1
    print(f"crawl4ai_fetch ok duration_ms={int((time.monotonic() - start) * 1000)}", file=sys.stderr)
    sys.stdout.write(html)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
