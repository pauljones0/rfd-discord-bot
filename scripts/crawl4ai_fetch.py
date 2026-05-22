#!/usr/bin/env python3
"""Fetch one page with Crawl4AI Magic Mode and print raw HTML to stdout."""

from __future__ import annotations

import argparse
import asyncio
import glob
import importlib.metadata
import inspect
import os
from pathlib import Path
import subprocess
import sys
import time

from crawl4ai import AsyncWebCrawler, BrowserConfig, CacheMode, CrawlerRunConfig


def truthy_env(name: str, default: bool = True) -> bool:
    value = os.getenv(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "y", "on"}


def playwright_cache_root() -> Path:
    return Path(os.getenv("PLAYWRIGHT_BROWSERS_PATH", "/ms-playwright"))


def playwright_browser_patterns(browser: str) -> list[str]:
    match browser:
        case "firefox":
            return ["firefox-*/firefox/firefox"]
        case "webkit":
            return ["webkit-*/pw_run.sh", "webkit-*/minibrowser-gtk/MiniBrowser"]
        case _:
            return ["chromium-*/chrome-linux64/chrome", "chromium-*/chrome-linux/chrome"]


def playwright_browser_exists(browser: str) -> bool:
    root = playwright_cache_root()
    return any(glob.glob(str(root / pattern)) for pattern in playwright_browser_patterns(browser))


def playwright_package_version() -> str:
    try:
        return importlib.metadata.version("playwright")
    except importlib.metadata.PackageNotFoundError:
        return "unknown"


def ensure_playwright_browser(browser: str) -> None:
    if not truthy_env("SCRAPELAB_AUTO_INSTALL_BROWSERS", True):
        return
    root = playwright_cache_root()
    version = playwright_package_version()
    marker = root / f".rfd-playwright-{browser}.version"
    if playwright_browser_exists(browser) and marker.exists() and marker.read_text().strip() == version:
        return

    root.mkdir(parents=True, exist_ok=True)
    print(f"ensuring playwright {browser} browser cache at {root} for package {version}", file=sys.stderr)
    subprocess.run([sys.executable, "-m", "playwright", "install", browser], check=True)
    try:
        marker.write_text(version, encoding="utf-8")
    except OSError as exc:
        print(f"could not write playwright cache marker: {exc}", file=sys.stderr)


def supported_kwargs(cls, values: dict) -> dict:
    params = inspect.signature(cls.__init__).parameters
    return {key: value for key, value in values.items() if key in params and value is not None}


def make_browser_config(args: argparse.Namespace) -> BrowserConfig:
    values = {
        "browser_type": args.browser,
        "headless": args.headless,
        "viewport_width": args.width,
        "viewport_height": args.height,
        "user_agent": args.user_agent or None,
        "locale": args.locale,
        "enable_stealth": True,
    }
    params = inspect.signature(BrowserConfig.__init__).parameters
    if "locale" not in params and args.locale and "extra_args" in params:
        values["extra_args"] = [f"--lang={args.locale}"]
    return BrowserConfig(**supported_kwargs(BrowserConfig, values))


def make_run_config(args: argparse.Namespace) -> CrawlerRunConfig:
    values = {
        "cache_mode": CacheMode.BYPASS,
        "magic": True,
        "simulate_user": True,
        "override_navigator": True,
        "delay_before_return_html": args.wait_ms / 1000.0,
        "page_timeout": args.timeout_ms,
    }
    return CrawlerRunConfig(**supported_kwargs(CrawlerRunConfig, values))


async def fetch(args: argparse.Namespace) -> str:
    browser_cfg = make_browser_config(args)
    run_cfg = make_run_config(args)
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
        ensure_playwright_browser(args.browser)
        html = asyncio.run(fetch(args))
    except Exception as exc:
        print(f"crawl4ai_fetch failed: {exc}", file=sys.stderr)
        return 1
    print(f"crawl4ai_fetch ok duration_ms={int((time.monotonic() - start) * 1000)}", file=sys.stderr)
    sys.stdout.write(html)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
