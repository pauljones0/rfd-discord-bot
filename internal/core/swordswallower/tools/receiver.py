#!/usr/bin/env python3
import argparse
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


class Receiver(BaseHTTPRequestHandler):
    server_version = "SwordswallowerReceiver/0.1"

    def do_POST(self):
        if self.path != self.server.events_path:
            self.send_error(404, "not found")
            return

        expected_secret = self.server.secret
        if expected_secret:
            actual_secret = self.headers.get("X-Swordswallower-Secret", "")
            if actual_secret != expected_secret:
                self.send_error(401, "invalid secret")
                return

        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        try:
            event = json.loads(body.decode("utf-8"))
        except json.JSONDecodeError:
            self.send_error(400, "invalid json")
            return

        line = json.dumps(event, sort_keys=True, separators=(",", ":"))
        print(line, flush=True)
        if self.server.output_path:
            with self.server.output_path.open("a", encoding="utf-8") as out:
                out.write(line + "\n")

        forward_result = forward_event(
            self.server.forward_url,
            self.server.forward_headers,
            body,
            self.server.forward_timeout,
        )
        if forward_result:
            print(json.dumps(forward_result, sort_keys=True, separators=(",", ":")), flush=True)
            if forward_result.get("type") == "forward_error":
                self.send_error(502, "upstream forward failed")
                return

        self.send_response(204)
        self.end_headers()

    def log_message(self, fmt, *args):
        print("%s - %s" % (self.address_string(), fmt % args), flush=True)


def parse_args():
    parser = argparse.ArgumentParser(description="Receive Swordswallower notification events.")
    parser.add_argument("--host", default=os.environ.get("HOST", "0.0.0.0"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("PORT", "8787")))
    parser.add_argument("--path", default=os.environ.get("EVENTS_PATH", "/events"))
    parser.add_argument("--secret", default=os.environ.get("SWORDSWALLOWER_SECRET", ""))
    parser.add_argument(
        "--forward-url",
        default=os.environ.get("FORWARD_URL", ""),
        help="Optional upstream URL that receives each event as JSON.",
    )
    parser.add_argument(
        "--forward-bearer-token",
        default=os.environ.get("FORWARD_BEARER_TOKEN", ""),
        help="Optional bearer token for the upstream receiver.",
    )
    parser.add_argument(
        "--forward-header",
        action="append",
        default=[],
        help="Extra upstream header in 'Name: value' form. Can be repeated.",
    )
    parser.add_argument(
        "--forward-timeout",
        type=float,
        default=float(os.environ.get("FORWARD_TIMEOUT", "5")),
        help="Upstream forwarding timeout in seconds.",
    )
    parser.add_argument(
        "--output",
        default=os.environ.get("EVENTS_OUTPUT", "events.jsonl"),
        help="JSONL output path; use an empty value to disable file output.",
    )
    return parser.parse_args()


def parse_forward_headers(args):
    headers = {
        "Content-Type": "application/json; charset=utf-8",
        "Accept": "application/json",
    }
    if args.forward_bearer_token:
        headers["Authorization"] = f"Bearer {args.forward_bearer_token}"

    env_headers = os.environ.get("FORWARD_HEADERS", "")
    raw_headers = list(args.forward_header)
    if env_headers:
        raw_headers.extend(part.strip() for part in env_headers.split("\n") if part.strip())

    for raw in raw_headers:
        name, sep, value = raw.partition(":")
        if not sep or not name.strip():
            raise SystemExit(f"invalid --forward-header value: {raw!r}")
        headers[name.strip()] = value.strip()
    return headers


def forward_event(forward_url, headers, body, timeout):
    if not forward_url:
        return None

    request = Request(forward_url, data=body, headers=headers, method="POST")
    try:
        with urlopen(request, timeout=timeout) as response:
            return {
                "type": "forward",
                "url": forward_url,
                "status": response.status,
            }
    except HTTPError as exc:
        return {
            "type": "forward_error",
            "url": forward_url,
            "status": exc.code,
            "error": exc.reason,
        }
    except URLError as exc:
        return {
            "type": "forward_error",
            "url": forward_url,
            "error": str(exc.reason),
        }


def main():
    args = parse_args()
    output_path = Path(args.output) if args.output else None
    server = ThreadingHTTPServer((args.host, args.port), Receiver)
    server.events_path = args.path
    server.secret = args.secret
    server.output_path = output_path
    server.forward_url = args.forward_url
    server.forward_headers = parse_forward_headers(args)
    server.forward_timeout = args.forward_timeout
    print(f"listening on http://{args.host}:{args.port}{args.path}", flush=True)
    if args.forward_url:
        print(f"forwarding events to {args.forward_url}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("stopped", flush=True)


if __name__ == "__main__":
    main()
