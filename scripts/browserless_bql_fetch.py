#!/usr/bin/env python
"""Fetch one page through Browserless BrowserQL stealth and print HTML.

This is intentionally a tiny paid-trial adapter for scrape-lab/eBay coupon
experiments. It does no retry loop; keep the target set small and controlled.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


DEFAULT_ENDPOINT = "https://production-sfo.browserless.io/stealth/bql"


def env_float(name: str, default: float) -> float:
    value = os.getenv(name)
    if not value:
        return default
    try:
        return float(value)
    except ValueError:
        print(f"invalid {name}={value!r}; using {default}", file=sys.stderr)
        return default


def endpoint_with_token(endpoint: str, token: str) -> str:
    parsed = urllib.parse.urlparse(endpoint)
    query = urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)
    if not any(key == "token" for key, _ in query):
        query.append(("token", token))
    return urllib.parse.urlunparse(parsed._replace(query=urllib.parse.urlencode(query)))


PRICE_DETAILS_SCRIPT = r"""
(() => {
  const rx = /price\s*details/i;
  const nodes = Array.from(document.querySelectorAll('button,a,[role="button"]'));
  const el = nodes.find((node) => {
    const text = [
      node.innerText,
      node.textContent,
      node.getAttribute('aria-label'),
      node.getAttribute('title')
    ].filter(Boolean).join(' ');
    return rx.test(text);
  });
  if (!el) return 'not-found';
  el.scrollIntoView({block: 'center', inline: 'center'});
  el.click();
  return 'clicked';
})()
"""


def build_query(url: str, wait_ms: float, timeout_ms: float, ebay_price_details: bool, solve_captcha: bool) -> str:
    url_json = json.dumps(url)
    solve_block = ""
    if solve_captcha:
        solve_block = """
  captcha: solve {
    found
    solved
    time
  }
  waitAfterCaptcha: waitForTimeout(time: 1500) {
    time
  }
"""
    price_details_block = ""
    if ebay_price_details:
        script_json = json.dumps(PRICE_DETAILS_SCRIPT)
        price_details_block = f"""
  priceDetails: evaluate(content: {script_json}, timeout: 5000) {{
    value
  }}
  waitAfterPriceDetails: waitForTimeout(time: 1500) {{
    time
  }}
"""
    return f"""
mutation EbayCouponPageTrial {{
  goto(url: {url_json}, waitUntil: firstMeaningfulPaint, timeout: {timeout_ms}) {{
    status
  }}
  waitForTimeout(time: {wait_ms}) {{
    time
  }}
{solve_block}
{price_details_block}
  html {{
    html
  }}
}}
"""


def extract_html(payload: bytes) -> str:
    try:
        response = json.loads(payload.decode("utf-8"))
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"Browserless returned non-JSON response: {exc}") from exc

    if response.get("errors"):
        raise RuntimeError(f"Browserless BQL errors: {response['errors']}")

    html = response.get("data", {}).get("html", {}).get("html")
    if not isinstance(html, str) or not html:
        raise RuntimeError("Browserless response did not include data.html.html")
    return html


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("url", nargs="?", default=os.getenv("SCRAPELAB_TARGET_URL", ""))
    parser.add_argument("--endpoint", default=os.getenv("BROWSERLESS_BQL_ENDPOINT", DEFAULT_ENDPOINT))
    parser.add_argument("--wait-ms", type=float, default=env_float("BROWSERLESS_WAIT_MS", 5000))
    parser.add_argument("--timeout-ms", type=float, default=env_float("BROWSERLESS_TIMEOUT_MS", 60000))
    parser.add_argument("--ebay-price-details", action="store_true", help="click eBay's Price details control before returning HTML")
    parser.add_argument("--solve-captcha", action="store_true", help="ask BrowserQL to solve a CAPTCHA before returning HTML")
    args = parser.parse_args()

    token = os.getenv("BROWSERLESS_TOKEN", "").strip()
    if not token:
        print("BROWSERLESS_TOKEN is required for paid-trial", file=sys.stderr)
        return 2
    if not args.url:
        print("target URL is required", file=sys.stderr)
        return 2

    url = endpoint_with_token(args.endpoint, token)
    body = json.dumps({"query": build_query(args.url, args.wait_ms, args.timeout_ms, args.ebay_price_details, args.solve_captcha)}).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )

    print(
        f"browserless_bql_fetch url={args.url!r} endpoint={args.endpoint!r} wait_ms={args.wait_ms} solve_captcha={args.solve_captcha}",
        file=sys.stderr,
    )
    start = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=(args.timeout_ms / 1000) + 15) as resp:
            html = extract_html(resp.read())
    except urllib.error.HTTPError as exc:
        details = exc.read().decode("utf-8", errors="replace")
        print(f"Browserless HTTP {exc.code}: {details}", file=sys.stderr)
        return 1
    except Exception as exc:
        print(f"browserless_bql_fetch failed: {exc}", file=sys.stderr)
        return 1

    sys.stdout.buffer.write(html.encode("utf-8", errors="replace"))
    print(f"browserless_bql_fetch ok duration_ms={int((time.monotonic() - start) * 1000)}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
