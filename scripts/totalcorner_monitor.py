#!/usr/bin/env python
"""Monitor TotalCorner live updates and emit OnEveryCorner JSON events."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any

from bs4 import BeautifulSoup


HEARTBEAT_SECONDS = 15.0
PROBLEM_ALERT_SECONDS = 30.0
FIX_FAILED_SECONDS = 75.0


@dataclass
class MatchState:
    home_corners: int | None = None
    away_corners: int | None = None
    home_score: int | None = None
    away_score: int | None = None
    last_seen: float = 0


def parse_csv(value: str) -> set[str]:
    return {part.strip() for part in value.split(",") if part.strip()}


def to_int(value: Any) -> int | None:
    if value is None or value == "":
        return None
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    text = str(value).strip()
    if text.lower() in {"half", "ht", "ft"}:
        return None
    try:
        return int(text)
    except ValueError:
        try:
            return int(float(text))
        except ValueError:
            return None


def first_int(*values: Any) -> int | None:
    for value in values:
        parsed = to_int(value)
        if parsed is not None:
            return parsed
    return None


def parse_scoreline(value: Any) -> tuple[int | None, int | None]:
    if not value:
        return None, None
    text = re.sub(r"\s+", " ", str(value).strip().replace(":", "-"))
    match = re.search(r"(\d+)\s*-\s*(\d+)", text)
    if not match:
        return None, None
    return to_int(match.group(1)), to_int(match.group(2))


def int_or_zero(value: Any) -> int:
    parsed = to_int(value)
    if parsed is None:
        return 0
    return parsed


DEFAULT_TOTALCORNER_API_URL = "https://api.totalcorner.com/v1/match/today"
DEFAULT_TOTALCORNER_API_TOKEN_ENV = "ONEVERYCORNER_TOTALCORNER_API_TOKEN"
DEFAULT_TOTALCORNER_API_MIN_POLL_MS = 3500


def clean_text(value: Any) -> str:
    if value is None:
        return ""
    return re.sub(r"\s+", " ", str(value)).strip()


def element_text(parent: Any, selector: str) -> str:
    element = parent.select_one(selector)
    if not element:
        return ""
    return clean_text(element.get_text(" ", strip=True))


def extract_metadata(html: str) -> dict[str, dict[str, Any]]:
    soup = BeautifulSoup(html, "html.parser")
    metadata: dict[str, dict[str, Any]] = {}
    for row in soup.select("tr[data-match_id]"):
        match_id = clean_text(row.get("data-match_id", ""))
        if not match_id:
            continue
        home_team = element_text(row, ".match_home a") or element_text(row, ".match_home")
        away_team = element_text(row, ".match_away a") or element_text(row, ".match_away")
        home_score, away_score = parse_scoreline(element_text(row, ".match_goal"))
        home_corners, away_corners = parse_scoreline(element_text(row, ".span_match_corner"))
        metadata[match_id] = {
            "match_id": match_id,
            "league_id": clean_text(row.get("data_league_id", "") or row.get("data-league_id", "")),
            "league_name": element_text(row, ".td_league"),
            "home_team": home_team,
            "away_team": away_team,
            "match_name": f"{home_team} v {away_team}" if home_team and away_team else home_team or away_team,
            "status": element_text(row, ".match_status"),
            "home_corners": home_corners,
            "away_corners": away_corners,
            "home_score": home_score,
            "away_score": away_score,
        }
    return metadata


def match_snapshot(row: dict[str, Any], metadata: dict[str, dict[str, Any]]) -> dict[str, Any] | None:
    match_id = clean_text(str(row.get("id") or ""))
    if not match_id:
        return None
    meta = metadata.get(match_id, {})
    home_team = clean_text(meta.get("home_team") or row.get("h") or row.get("home") or row.get("home_team") or "")
    away_team = clean_text(meta.get("away_team") or row.get("a") or row.get("away") or row.get("away_team") or "")
    return {
        "match_id": match_id,
        "match_name": clean_text(str(meta.get("match_name") or "")) or f"{home_team} v {away_team}".strip(" v") or "Unknown match",
        "league_id": clean_text(meta.get("league_id") or row.get("l_id") or row.get("league_id") or ""),
        "league_name": clean_text(meta.get("league_name") or row.get("l") or row.get("league") or row.get("league_name") or ""),
        "home_team": home_team,
        "away_team": away_team,
        "status": clean_text(row.get("sta") or row.get("status") or meta.get("status") or ""),
        "home_corners": first_int(row.get("hc"), meta.get("home_corners")),
        "away_corners": first_int(row.get("ac"), meta.get("away_corners")),
        "home_score": first_int(row.get("hg"), meta.get("home_score")),
        "away_score": first_int(row.get("ag"), meta.get("away_score")),
    }


def snapshot_with_known_state(snapshot: dict[str, Any], prev: MatchState) -> dict[str, Any]:
    merged = dict(snapshot)
    if merged["home_corners"] is None:
        merged["home_corners"] = prev.home_corners
    if merged["away_corners"] is None:
        merged["away_corners"] = prev.away_corners
    if merged["home_score"] is None:
        merged["home_score"] = prev.home_score
    if merged["away_score"] is None:
        merged["away_score"] = prev.away_score
    return merged


def event_payload(event_type: str, snapshot: dict[str, Any], team: str, sequence: int, at_ms: int) -> dict[str, Any]:
    return {
        "type": event_type,
        "match_id": snapshot["match_id"],
        "match_name": snapshot["match_name"],
        "league_id": snapshot["league_id"],
        "league_name": snapshot["league_name"],
        "home_team": snapshot["home_team"],
        "away_team": snapshot["away_team"],
        "team": team,
        "home_corners": int_or_zero(snapshot["home_corners"]),
        "away_corners": int_or_zero(snapshot["away_corners"]),
        "home_score": int_or_zero(snapshot["home_score"]),
        "away_score": int_or_zero(snapshot["away_score"]),
        "sequence": sequence,
        "status": snapshot["status"],
        "at_unix_ms": at_ms,
    }


def emit(payload: dict[str, Any]) -> None:
    print(json.dumps(payload, separators=(",", ":")), flush=True)
    print(
        "totalcorner emit type={type} match_id={match_id} match={match_name!r} team={team} "
        "corners={home_corners}-{away_corners} score={home_score}-{away_score}".format(**payload),
        file=sys.stderr,
        flush=True,
    )


def emit_system_event(
    event_type: str,
    severity: str,
    message: str,
    *,
    stage: str = "",
    status: str = "",
    attempt: str = "",
    detail: str = "",
    stale_seconds: int = 0,
    suppressed_events: int = 0,
) -> None:
    payload = {
        "type": event_type,
        "severity": severity,
        "message": message,
        "stage": stage,
        "status": status,
        "attempt": attempt,
        "detail": detail,
        "stale_seconds": stale_seconds,
        "suppressed_events": suppressed_events,
        "at_unix_ms": int(time.time() * 1000),
    }
    print(json.dumps(payload, separators=(",", ":")), flush=True)
    print(
        "totalcorner system type={type} severity={severity} stage={stage!r} status={status!r} "
        "attempt={attempt!r} stale_seconds={stale_seconds} suppressed_events={suppressed_events} "
        "message={message!r}".format(**payload),
        file=sys.stderr,
        flush=True,
    )


def log_system_event(
    event_type: str,
    severity: str,
    message: str,
    *,
    stage: str = "",
    status: str = "",
    attempt: str = "",
    detail: str = "",
    stale_seconds: int = 0,
    suppressed_events: int = 0,
) -> None:
    print(
        "totalcorner system log_only=true "
        f"type={event_type} severity={severity} stage={stage!r} status={status!r} "
        f"attempt={attempt!r} stale_seconds={stale_seconds} suppressed_events={suppressed_events} "
        f"detail={detail!r} message={message!r}",
        file=sys.stderr,
        flush=True,
    )


def report_system_event(emit_to_stdout: bool, *args: Any, **kwargs: Any) -> None:
    if emit_to_stdout:
        emit_system_event(*args, **kwargs)
        return
    log_system_event(*args, **kwargs)


def count_delta_events(prev: MatchState, snapshot: dict[str, Any]) -> int:
    count = 0
    for current_key, previous_value in [
        ("home_corners", prev.home_corners),
        ("away_corners", prev.away_corners),
        ("home_score", prev.home_score),
        ("away_score", prev.away_score),
    ]:
        current = snapshot[current_key]
        if previous_value is not None and current is not None and current > previous_value:
            count += current - previous_value
    return count


def emit_delta_events(prev: MatchState, snapshot: dict[str, Any]) -> int:
    at_ms = int(time.time() * 1000)
    emitted = 0
    base_payload = snapshot_with_known_state(snapshot, prev)

    home_corners = snapshot["home_corners"]
    away_corners = snapshot["away_corners"]
    if prev.home_corners is not None and home_corners is not None and home_corners > prev.home_corners:
        for sequence in range(prev.home_corners + 1, home_corners + 1):
            payload = dict(base_payload)
            payload["home_corners"] = sequence
            emit(event_payload("corner", payload, "home", sequence, at_ms))
            emitted += 1
    if prev.away_corners is not None and away_corners is not None and away_corners > prev.away_corners:
        for sequence in range(prev.away_corners + 1, away_corners + 1):
            payload = dict(base_payload)
            payload["away_corners"] = sequence
            emit(event_payload("corner", payload, "away", sequence, at_ms))
            emitted += 1

    home_score = snapshot["home_score"]
    away_score = snapshot["away_score"]
    if prev.home_score is not None and home_score is not None and home_score > prev.home_score:
        for sequence in range(prev.home_score + 1, home_score + 1):
            payload = dict(base_payload)
            payload["home_score"] = sequence
            emit(event_payload("goal", payload, "home", sequence, at_ms))
            emitted += 1
    if prev.away_score is not None and away_score is not None and away_score > prev.away_score:
        for sequence in range(prev.away_score + 1, away_score + 1):
            payload = dict(base_payload)
            payload["away_score"] = sequence
            emit(event_payload("goal", payload, "away", sequence, at_ms))
            emitted += 1
    return emitted


def update_state(prev: MatchState | None, snapshot: dict[str, Any], now: float) -> MatchState:
    if prev is None:
        prev = MatchState()
    if snapshot["home_corners"] is not None:
        prev.home_corners = snapshot["home_corners"]
    if snapshot["away_corners"] is not None:
        prev.away_corners = snapshot["away_corners"]
    if snapshot["home_score"] is not None:
        prev.home_score = snapshot["home_score"]
    if snapshot["away_score"] is not None:
        prev.away_score = snapshot["away_score"]
    prev.last_seen = now
    return prev


def league_allowed(snapshot: dict[str, Any], league_ids: set[str]) -> bool:
    if not league_ids:
        return True
    return snapshot["league_id"] in league_ids


def parse_bool(value: Any, fallback: bool = False) -> bool:
    text = clean_text(value).lower()
    if text == "":
        return fallback
    if text in {"1", "true", "yes", "on"}:
        return True
    if text in {"0", "false", "no", "off"}:
        return False
    return fallback


def handle_totalcorner_data(
    rows: list[Any],
    league_ids: set[str],
    metadata: dict[str, dict[str, Any]],
    states: dict[str, MatchState],
    emit_deltas: bool = True,
) -> dict[str, int]:
    now = time.time()
    active_ids: set[str] = set()
    stats = {"rows": 0, "matched": 0, "tracked": len(states), "emitted": 0, "suppressed": 0, "unknown_meta": 0}
    if not isinstance(rows, list):
        return stats
    stats["rows"] = len(rows)
    for row in rows:
        if not isinstance(row, dict):
            continue
        snapshot = match_snapshot(row, metadata)
        if not snapshot:
            continue
        if not snapshot["league_id"]:
            stats["unknown_meta"] += 1
        if not league_allowed(snapshot, league_ids):
            continue
        stats["matched"] += 1
        match_id = snapshot["match_id"]
        active_ids.add(match_id)
        prev = states.get(match_id)
        if prev is not None:
            if emit_deltas:
                stats["emitted"] += emit_delta_events(prev, snapshot)
            else:
                stats["suppressed"] += count_delta_events(prev, snapshot)
        states[match_id] = update_state(prev, snapshot, now)

    for match_id, state in list(states.items()):
        if match_id not in active_ids and now - state.last_seen > 30 * 60:
            del states[match_id]
    stats["tracked"] = len(states)
    return stats


def build_totalcorner_api_url(api_url: str, token: str) -> str:
    parts = urllib.parse.urlsplit(api_url)
    query = dict(urllib.parse.parse_qsl(parts.query, keep_blank_values=True))
    query["token"] = token
    query.setdefault("type", "inplay")
    return urllib.parse.urlunsplit(
        (
            parts.scheme,
            parts.netloc,
            parts.path,
            urllib.parse.urlencode(query),
            parts.fragment,
        )
    )


def load_totalcorner_rows(body: str) -> tuple[list[Any] | None, str]:
    try:
        parsed = json.loads(body)
    except Exception as exc:
        return None, f"json parse failed: {exc!r}"
    if isinstance(parsed, list):
        return parsed, ""
    if not isinstance(parsed, dict):
        return None, "expected JSON list or object"
    success = clean_text(parsed.get("success")).lower()
    if success in {"0", "false", "no"}:
        detail = parsed.get("message") or parsed.get("error") or parsed.get("reason") or "api success=0"
        return None, clean_text(detail)
    rows = parsed.get("data")
    if not isinstance(rows, list):
        return None, "expected API response data list"
    return rows, ""


def fetch_totalcorner_api(api_url: str, token: str, timeout_ms: int) -> tuple[int, str, str]:
    request = urllib.request.Request(
        build_totalcorner_api_url(api_url, token),
        headers={
            "accept": "application/json",
            "user-agent": "rfd-discord-bot/1.0",
            "connection": "close",
        },
        method="GET",
    )
    timeout_seconds = max(1.0, timeout_ms / 1000.0)
    try:
        with urllib.request.urlopen(request, timeout=timeout_seconds) as response:
            body = response.read().decode("utf-8", errors="replace")
            return int(response.status), body, ""
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        return int(exc.code), body, clean_text(body or exc.reason or "http error")
    except Exception as exc:
        return 0, "", repr(exc)


def run_totalcorner_api(args: argparse.Namespace, league_ids: set[str], states: dict[str, MatchState], token: str) -> int:
    metadata: dict[str, dict[str, Any]] = {}
    last_response = time.monotonic()
    last_heartbeat = 0.0
    last_poll_error = 0.0
    problem_started = 0.0
    problem_stage = ""
    problem_status = ""
    fix_attempted_at = 0.0
    fix_failed_emitted = False
    suppress_next_delta = False
    restart_requested = False
    active_poll_count = 0
    last_heartbeat_poll_count = 0

    print(
        f"totalcorner_monitor starting source=api api_url={args.api_url!r} "
        f"token_env={args.api_token_env!r} league_ids={sorted(league_ids)}",
        file=sys.stderr,
        flush=True,
    )

    def mark_totalcorner_healthy(status: int, source: str, suppressed_events: int = 0) -> None:
        nonlocal last_response, last_heartbeat, last_heartbeat_poll_count
        nonlocal problem_started, problem_stage, problem_status, fix_attempted_at, fix_failed_emitted
        nonlocal suppress_next_delta
        now = time.monotonic()
        last_response = now
        if problem_started:
            stale_seconds = int(now - problem_started)
            if fix_attempted_at or stale_seconds >= PROBLEM_ALERT_SECONDS:
                report_system_event(
                    args.api_system_events,
                    "system_recovered",
                    "recovered",
                    "TotalCorner official API polling recovered; primary OnEveryCorner monitoring has resumed.",
                    stage=source,
                    status=str(status),
                    attempt="official_api.retry" if fix_attempted_at else "",
                    stale_seconds=stale_seconds,
                    suppressed_events=suppressed_events,
                )
            problem_started = 0.0
            problem_stage = ""
            problem_status = ""
            fix_attempted_at = 0.0
            fix_failed_emitted = False
            suppress_next_delta = False
        if now - last_heartbeat >= HEARTBEAT_SECONDS:
            polls_since_log = active_poll_count - last_heartbeat_poll_count
            print(
                "totalcorner heartbeat "
                f"source={source} status={status} tracked={len(states)} "
                f"active_polls={polls_since_log} poll_ms={api_poll_ms}",
                file=sys.stderr,
                flush=True,
            )
            last_heartbeat = now
            last_heartbeat_poll_count = active_poll_count

    def mark_totalcorner_problem(status: int, source: str, detail: str = "") -> None:
        nonlocal problem_started, problem_stage, problem_status, fix_attempted_at, fix_failed_emitted
        nonlocal suppress_next_delta, restart_requested, last_poll_error
        now = time.monotonic()
        status_text = str(status)
        if problem_started == 0.0:
            problem_started = now
            problem_stage = source
            problem_status = status_text
            fix_attempted_at = 0.0
            fix_failed_emitted = False
        stale_seconds = int(now - problem_started)
        if stale_seconds >= PROBLEM_ALERT_SECONDS and fix_attempted_at == 0.0:
            fix_attempted_at = now
            suppress_next_delta = True
            report_system_event(
                args.api_system_events,
                "system_fix_attempt",
                "warning",
                "TotalCorner official API polling is unhealthy; retrying with fresh HTTP requests. "
                "The next successful snapshot will be used as a new baseline so missed corners are not replayed.",
                stage=source,
                status=status_text,
                attempt="official_api.retry",
                detail=detail,
                stale_seconds=stale_seconds,
            )
        elif fix_attempted_at and not fix_failed_emitted and now - fix_attempted_at >= FIX_FAILED_SECONDS:
            fix_failed_emitted = True
            suppress_next_delta = True
            restart_requested = True
            report_system_event(
                args.api_system_events,
                "system_fix_failed",
                "error",
                "TotalCorner official API polling is still unhealthy after retrying; restarting the TotalCorner monitor process.",
                stage=problem_stage or source,
                status=problem_status or status_text,
                attempt="restart totalcorner monitor process",
                detail=detail,
                stale_seconds=stale_seconds,
            )
        if now - last_poll_error >= 60:
            print(f"totalcorner unhealthy source={source} status={status_text} detail={detail!r}", file=sys.stderr, flush=True)
            last_poll_error = now

    api_poll_ms = max(args.poll_ms, args.api_min_poll_ms)
    if api_poll_ms != args.poll_ms:
        print(
            "totalcorner api poll interval raised "
            f"requested_ms={args.poll_ms} effective_ms={api_poll_ms} "
            "reason='official API limit is 5 requests per 10 seconds'",
            file=sys.stderr,
            flush=True,
        )
    poll_seconds = max(0.5, api_poll_ms / 1000.0)
    while True:
        loop_started = time.monotonic()
        active_poll_count += 1
        status, body, detail = fetch_totalcorner_api(args.api_url, token, args.timeout_ms)
        if status == 200:
            rows, parse_error = load_totalcorner_rows(body)
            if rows is None:
                mark_totalcorner_problem(status, "api", parse_error)
            else:
                emit_deltas = not suppress_next_delta
                stats = handle_totalcorner_data(rows, league_ids, metadata, states, emit_deltas=emit_deltas)
                suppressed = stats.get("suppressed", 0)
                mark_totalcorner_healthy(status, "api", suppressed_events=suppressed)
                print(
                    "totalcorner data ok source=api rows={rows} matched={matched} tracked={tracked} "
                    "emitted={emitted} suppressed={suppressed} unknown_meta={unknown_meta}".format(**stats),
                    file=sys.stderr,
                    flush=True,
                )
        else:
            mark_totalcorner_problem(status, "api", detail)
        if restart_requested:
            raise RuntimeError("totalcorner api monitor restart requested after failed recovery")
        elapsed = time.monotonic() - loop_started
        if elapsed < poll_seconds:
            time.sleep(poll_seconds - elapsed)


def _selftest() -> int:
    metadata = {
        "194699784": {
            "match_id": "194699784",
            "league_id": "29754",
            "league_name": "World Cup 2026",
            "home_team": "Scotland",
            "away_team": "Morocco",
            "match_name": "Scotland v Morocco",
        }
    }
    states: dict[str, MatchState] = {}
    base_row = {"id": "194699784", "hc": "0", "ac": "0", "hg": "0", "ag": "0", "sta": "04"}
    corner_row = {"id": "194699784", "hc": "1", "ac": "0", "hg": "0", "ag": "0", "sta": "05"}
    goal_row = {"id": "194699784", "hc": "1", "ac": "0", "hg": "1", "ag": "0", "sta": "06"}
    catchup_row = {"id": "194699784", "hc": "3", "ac": "0", "hg": "2", "ag": "0", "sta": "10"}
    print("SELFTEST baseline", handle_totalcorner_data([base_row], {"29754"}, metadata, states), file=sys.stderr)
    print("SELFTEST corner", handle_totalcorner_data([corner_row], {"29754"}, metadata, states), file=sys.stderr)
    print("SELFTEST goal", handle_totalcorner_data([goal_row], {"29754"}, metadata, states), file=sys.stderr)
    catchup_stats = handle_totalcorner_data([catchup_row], {"29754"}, metadata, states, emit_deltas=False)
    print("SELFTEST catchup_suppressed", catchup_stats, file=sys.stderr)
    if catchup_stats["emitted"] != 0 or catchup_stats["suppressed"] != 3:
        print(f"SELFTEST failed catchup suppression: {catchup_stats}", file=sys.stderr)
        return 1
    api_states: dict[str, MatchState] = {}
    api_row = {
        "id": "194907896",
        "h": "Türkiye",
        "a": "Paraguay",
        "l": "World Cup 2026",
        "l_id": "29754",
        "status": "25",
        "hc": "4",
        "ac": "0",
        "hg": "0",
        "ag": "1",
    }
    api_stats = handle_totalcorner_data([api_row], {"29754"}, {}, api_states)
    print("SELFTEST api_row", api_stats, file=sys.stderr)
    api_snapshot = match_snapshot(api_row, {})
    if api_stats["matched"] != 1 or not api_snapshot or api_snapshot["match_name"] != "Türkiye v Paraguay":
        print(f"SELFTEST failed api row parsing: stats={api_stats} snapshot={api_snapshot}", file=sys.stderr)
        return 1
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="https://www.totalcorner.com/match/today")
    parser.add_argument("--api-url", default=os.getenv("ONEVERYCORNER_TOTALCORNER_API_URL", DEFAULT_TOTALCORNER_API_URL))
    parser.add_argument("--api-token-env", default=DEFAULT_TOTALCORNER_API_TOKEN_ENV)
    parser.add_argument(
        "--api-min-poll-ms",
        type=int,
        default=to_int(os.getenv("ONEVERYCORNER_TOTALCORNER_API_MIN_POLL_MS")) or DEFAULT_TOTALCORNER_API_MIN_POLL_MS,
    )
    parser.add_argument(
        "--api-system-events",
        action="store_true",
        default=parse_bool(os.getenv("ONEVERYCORNER_TOTALCORNER_API_SYSTEM_EVENTS"), False),
        help="emit official API failure/recovery events to stdout for Discord alerts; default is stderr logs only",
    )
    parser.add_argument("--source", choices=("auto", "api", "browser"), default=os.getenv("ONEVERYCORNER_TOTALCORNER_SOURCE", "auto"))
    parser.add_argument("--league-ids", default="29754")
    parser.add_argument("--poll-ms", type=int, default=1000)
    parser.add_argument("--timeout-ms", type=int, default=60000)
    parser.add_argument("--locale", default="en-CA")
    parser.add_argument("--os", default="windows")
    parser.add_argument("--selftest", action="store_true", help="run a local parser delta test and exit")
    args = parser.parse_args()
    if args.selftest:
        return _selftest()

    league_ids = parse_csv(args.league_ids)
    states: dict[str, MatchState] = {}
    metadata: dict[str, dict[str, Any]] = {}
    api_token = clean_text(os.getenv(args.api_token_env, ""))
    if args.source == "api" or (args.source == "auto" and api_token):
        if not api_token:
            report_system_event(
                args.api_system_events,
                "system_issue",
                "error",
                "TotalCorner official API source is enabled but no API token is configured.",
                stage="api",
                status="missing_token",
                detail=f"Set {args.api_token_env}.",
            )
            return 1
        return run_totalcorner_api(args, league_ids, states, api_token)

    last_response = time.monotonic()
    last_heartbeat = 0.0
    last_poll_error = 0.0
    last_metadata_refresh = 0.0
    problem_started = 0.0
    problem_stage = ""
    problem_status = ""
    fix_attempted_at = 0.0
    fix_failed_emitted = False
    suppress_next_delta = False
    reload_requested = False
    restart_requested = False
    active_poll_count = 0
    last_heartbeat_poll_count = 0

    from camoufox.sync_api import Camoufox

    print(f"totalcorner_monitor starting url={args.url!r} league_ids={sorted(league_ids)}", file=sys.stderr, flush=True)
    with Camoufox(headless=False, os=args.os, locale=args.locale, humanize=True, block_webrtc=True, window=(1440, 1000)) as browser:
        page = browser.new_page()

        def refresh_metadata(force: bool = False) -> None:
            nonlocal metadata, last_metadata_refresh, last_poll_error
            now = time.monotonic()
            if not force and now - last_metadata_refresh < 30:
                return
            try:
                if force or not metadata:
                    page.locator("tr[data-match_id]").first.wait_for(state="attached", timeout=15000)
                refreshed = extract_metadata(page.content())
            except Exception as exc:
                if now - last_poll_error >= 60:
                    print(f"totalcorner metadata refresh failed: {exc!r}", file=sys.stderr, flush=True)
                    last_poll_error = now
                return
            if refreshed:
                metadata = refreshed
                last_metadata_refresh = now

        def mark_totalcorner_healthy(status: int, source: str, suppressed_events: int = 0) -> None:
            nonlocal last_response, last_heartbeat, last_heartbeat_poll_count
            nonlocal problem_started, problem_stage, problem_status, fix_attempted_at, fix_failed_emitted
            nonlocal suppress_next_delta, reload_requested
            now = time.monotonic()
            last_response = now
            if problem_started:
                stale_seconds = int(now - problem_started)
                if fix_attempted_at or stale_seconds >= PROBLEM_ALERT_SECONDS:
                    emit_system_event(
                        "system_recovered",
                        "recovered",
                        "TotalCorner polling recovered; redundant OnEveryCorner monitoring has resumed.",
                        stage=source,
                        status=str(status),
                        attempt="page.reload" if fix_attempted_at else "",
                        stale_seconds=stale_seconds,
                        suppressed_events=suppressed_events,
                    )
                problem_started = 0.0
                problem_stage = ""
                problem_status = ""
                fix_attempted_at = 0.0
                fix_failed_emitted = False
                suppress_next_delta = False
                reload_requested = False
            if now - last_heartbeat >= HEARTBEAT_SECONDS:
                polls_since_log = active_poll_count - last_heartbeat_poll_count
                print(
                    "totalcorner heartbeat "
                    f"source={source} status={status} metadata={len(metadata)} tracked={len(states)} "
                    f"active_polls={polls_since_log} poll_ms={args.poll_ms}",
                    file=sys.stderr,
                    flush=True,
                )
                last_heartbeat = now
                last_heartbeat_poll_count = active_poll_count

        def mark_totalcorner_problem(status: int, source: str, detail: str = "") -> None:
            nonlocal problem_started, problem_stage, problem_status, fix_attempted_at, fix_failed_emitted
            nonlocal suppress_next_delta, reload_requested, restart_requested, last_poll_error
            now = time.monotonic()
            status_text = str(status)
            if problem_started == 0.0:
                problem_started = now
                problem_stage = source
                problem_status = status_text
                fix_attempted_at = 0.0
                fix_failed_emitted = False
            stale_seconds = int(now - problem_started)
            if stale_seconds >= PROBLEM_ALERT_SECONDS and fix_attempted_at == 0.0:
                fix_attempted_at = now
                suppress_next_delta = True
                reload_requested = True
                emit_system_event(
                    "system_fix_attempt",
                    "warning",
                    "TotalCorner polling is unhealthy; attempting to reload the browser page. "
                    "The next successful snapshot will be used as a new baseline so missed corners are not replayed.",
                    stage=source,
                    status=status_text,
                    attempt="page.reload",
                    detail=detail,
                    stale_seconds=stale_seconds,
                )
            elif fix_attempted_at and not fix_failed_emitted and now - fix_attempted_at >= FIX_FAILED_SECONDS:
                fix_failed_emitted = True
                suppress_next_delta = True
                restart_requested = True
                emit_system_event(
                    "system_fix_failed",
                    "error",
                    "TotalCorner polling is still unhealthy after a page reload; restarting the TotalCorner monitor process.",
                    stage=problem_stage or source,
                    status=problem_status or status_text,
                    attempt="restart totalcorner monitor process",
                    detail=detail,
                    stale_seconds=stale_seconds,
                )
            if now - last_poll_error >= 60:
                print(f"totalcorner unhealthy source={source} status={status_text} detail={detail!r}", file=sys.stderr, flush=True)
                last_poll_error = now

        def process_totalcorner_payload(body: str, source: str) -> None:
            nonlocal suppress_next_delta
            try:
                data = json.loads(body)
            except Exception as exc:
                print(f"totalcorner data parse failed source={source}: {exc!r}", file=sys.stderr, flush=True)
                return
            if not isinstance(data, list):
                print(f"totalcorner data parse failed source={source}: expected list", file=sys.stderr, flush=True)
                return
            refresh_metadata()
            emit_deltas = not suppress_next_delta
            stats = handle_totalcorner_data(data, league_ids, metadata, states, emit_deltas=emit_deltas)
            suppressed = stats.get("suppressed", 0)
            mark_totalcorner_healthy(200, source, suppressed_events=suppressed)
            print(
                "totalcorner data ok source={source} rows={rows} matched={matched} tracked={tracked} "
                "emitted={emitted} suppressed={suppressed} unknown_meta={unknown_meta}".format(
                    source=source,
                    **stats,
                ),
                file=sys.stderr,
                flush=True,
            )

        def active_poll_totalcorner() -> None:
            nonlocal active_poll_count, last_poll_error
            active_poll_count += 1
            fetch_timeout_ms = max(750, min(args.timeout_ms, max(args.poll_ms * 2, args.poll_ms + 500)))
            try:
                result = page.evaluate(
                    """async ({timeoutMs}) => {
                        const controller = new AbortController();
                        const timer = setTimeout(() => controller.abort(), timeoutMs);
                        try {
                            const response = await fetch("/match/api_ongoing_matches?v=" + Date.now(), {
                                method: "GET",
                                credentials: "same-origin",
                                headers: {
                                    "accept": "application/json, text/javascript, */*; q=0.01",
                                    "x-requested-with": "XMLHttpRequest"
                                },
                                signal: controller.signal
                            });
                            const text = await response.text();
                            return {status: response.status, statusText: response.statusText, text};
                        } catch (error) {
                            return {status: 0, error: String(error)};
                        } finally {
                            clearTimeout(timer);
                        }
                    }""",
                    {"timeoutMs": fetch_timeout_ms},
                )
            except Exception as exc:
                now = time.monotonic()
                if now - last_poll_error >= 60:
                    print(f"totalcorner active poll failed: {exc!r}", file=sys.stderr, flush=True)
                    last_poll_error = now
                mark_totalcorner_problem(0, "active", repr(exc))
                return

            if not isinstance(result, dict):
                mark_totalcorner_problem(0, "active", f"unexpected result {result!r}")
                return

            status = to_int(result.get("status")) or 0
            if status == 200:
                process_totalcorner_payload(str(result.get("text") or ""), "active")
                return
            mark_totalcorner_problem(status, "active", str(result.get("error") or result.get("statusText") or ""))

        page.goto(args.url, wait_until="domcontentloaded", timeout=args.timeout_ms)
        refresh_metadata(force=True)

        poll_seconds = max(0.5, args.poll_ms / 1000.0)
        next_poll = time.monotonic() + poll_seconds
        watchdog_seconds = max(60, poll_seconds * 12)
        while True:
            wait_ms = max(0, int((next_poll - time.monotonic()) * 1000))
            if wait_ms:
                page.wait_for_timeout(wait_ms)
            active_poll_totalcorner()
            if reload_requested:
                reload_requested = False
                print("totalcorner monitor reloading page after unhealthy polling", file=sys.stderr, flush=True)
                try:
                    page.reload(wait_until="domcontentloaded", timeout=args.timeout_ms)
                    refresh_metadata(force=True)
                except Exception as exc:
                    emit_system_event(
                        "system_fix_failed",
                        "error",
                        "TotalCorner page reload failed; restarting the TotalCorner monitor process.",
                        stage=problem_stage or "active",
                        status=problem_status,
                        attempt="restart totalcorner monitor process",
                        detail=repr(exc),
                        stale_seconds=int(time.monotonic() - problem_started) if problem_started else 0,
                    )
                    raise RuntimeError("totalcorner page reload failed") from exc
            if restart_requested:
                raise RuntimeError("totalcorner monitor restart requested after failed recovery")
            next_poll += poll_seconds
            if next_poll < time.monotonic():
                next_poll = time.monotonic()
            if time.monotonic() - last_response > watchdog_seconds:
                print("totalcorner monitor watchdog reloading page", file=sys.stderr, flush=True)
                suppress_next_delta = True
                emit_system_event(
                    "system_fix_attempt",
                    "warning",
                    "TotalCorner monitor watchdog has not seen a healthy response; reloading the browser page.",
                    stage="watchdog",
                    status="timeout",
                    attempt="page.reload",
                    stale_seconds=int(time.monotonic() - last_response),
                )
                last_response = time.monotonic()
                try:
                    page.reload(wait_until="domcontentloaded", timeout=args.timeout_ms)
                    refresh_metadata(force=True)
                except Exception as exc:
                    emit_system_event(
                        "system_fix_failed",
                        "error",
                        "TotalCorner watchdog reload failed; restarting the TotalCorner monitor process.",
                        stage="watchdog",
                        status="timeout",
                        attempt="restart totalcorner monitor process",
                        detail=repr(exc),
                    )
                    print(f"totalcorner watchdog reload failed: {exc!r}", file=sys.stderr, flush=True)
                    raise RuntimeError("totalcorner watchdog reload failed") from exc


if __name__ == "__main__":
    raise SystemExit(main())
