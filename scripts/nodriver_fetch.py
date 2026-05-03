#!/usr/bin/env python
"""Fetch one page with nodriver and print final HTML to stdout.

This is intended for scrape-lab's external-stealth backend. Runtime metadata is
written to stderr so stdout remains parseable page content.
"""

from __future__ import annotations

import argparse
import asyncio
import glob
import os
import sys
from pathlib import Path

import nodriver as uc


def truthy(value: str | None) -> bool:
    return value is not None and value.strip().lower() in {"1", "true", "yes", "y"}


def default_browser_path(configured: str) -> str:
    if configured:
        return configured

    candidates = [
        "/ms-playwright/chromium-*/chrome-linux64/chrome",
        "/ms-playwright/chromium-*/chrome-linux/chrome",
        "/ms-playwright/chromium-*/chrome-linux/chrome-wrapper",
        "/usr/bin/google-chrome",
        "/usr/bin/google-chrome-stable",
        "/usr/bin/chromium",
        "/usr/bin/chromium-browser",
    ]
    for pattern in candidates:
        for match in sorted(glob.glob(pattern)):
            if os.path.exists(match):
                return match
        if "*" not in pattern and os.path.exists(pattern):
            return pattern
    return ""


def remove_stale_profile_locks(profile_dir: Path) -> None:
    for name in ("SingletonLock", "SingletonSocket", "SingletonCookie", "lockfile"):
        try:
            (profile_dir / name).unlink()
        except FileNotFoundError:
            pass


async def fetch(args: argparse.Namespace) -> str:
    profile_dir = Path(args.profile_dir).resolve()
    profile_dir.mkdir(parents=True, exist_ok=True)
    remove_stale_profile_locks(profile_dir)

    browser_args = [
        "--no-sandbox",
        "--no-first-run",
        "--no-default-browser-check",
        "--disable-background-timer-throttling",
        "--disable-backgrounding-occluded-windows",
        "--disable-renderer-backgrounding",
        "--window-size=1600,1000",
    ]
    if args.user_agent:
        browser_args.append(f"--user-agent={args.user_agent}")

    browser = await uc.start(
        user_data_dir=str(profile_dir),
        headless=args.headless,
        browser_executable_path=default_browser_path(args.browser_path) or None,
        browser_args=browser_args,
        lang=args.lang,
        sandbox=False,
        no_sandbox=True,
    )
    try:
        page = await browser.get(args.url)
        await page.sleep(args.wait_seconds)
        return await page.get_content()
    finally:
        browser.stop()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("url", nargs="?", default=os.getenv("SCRAPELAB_TARGET_URL", ""))
    parser.add_argument("--profile-dir", default=os.getenv("NODRIVER_PROFILE_DIR", ".codex-remote/nodriver-profile"))
    parser.add_argument("--browser-path", default=os.getenv("NODRIVER_BROWSER_PATH", ""))
    parser.add_argument("--lang", default=os.getenv("NODRIVER_LANG", "en-CA"))
    parser.add_argument("--user-agent", default=os.getenv("NODRIVER_USER_AGENT", ""))
    parser.add_argument("--wait-seconds", type=float, default=float(os.getenv("NODRIVER_WAIT_SECONDS", "5")))
    parser.add_argument("--headless", action="store_true", default=truthy(os.getenv("NODRIVER_HEADLESS")))
    args = parser.parse_args()

    if not args.url:
        print("target URL is required", file=sys.stderr)
        return 2

    html = asyncio.run(fetch(args))
    sys.stdout.buffer.write(html.encode("utf-8", errors="replace"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
