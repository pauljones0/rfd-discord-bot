#!/usr/bin/env python
"""Monitor Scoremer live score AJAX updates and emit OnEveryCorner JSON events.

The Go server starts this under xvfb-run with Camoufox. stdout is reserved for
newline-delimited JSON events; diagnostics go to stderr.
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from dataclasses import dataclass
from typing import Any
from urllib.parse import parse_qs



ACTIVE_POLL_HEADER = "x-oneverycorner-poll"
HEARTBEAT_SECONDS = 15.0
PROBLEM_ALERT_SECONDS = 30.0
FIX_FAILED_SECONDS = 75.0

DEFAULT_FILTERS = {
    "filter": "",
    "filter_league_id": 0,
    "filter_league_ids": [0, 3559],
    "filter_country_ids": [],
    "filter_daxiao_ids": [],
    "filter_jiaoqiu_ids": [],
    "filter_rangfen_ids": [],
    "filter_time": [],
    "hidden_race_ids": [],
    "is_hidden_for_time_filter_cnt": {"0": 1},
}


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
    try:
        return int(str(value).strip())
    except ValueError:
        try:
            return int(float(str(value).strip()))
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
    text = str(value).strip().replace(":", "-")
    if "-" not in text:
        return None, None
    left, right = text.split("-", 1)
    return to_int(left), to_int(right)


def nested_get(data: dict[str, Any], *path: str) -> Any:
    cur: Any = data
    for key in path:
        if not isinstance(cur, dict):
            return None
        cur = cur.get(key)
    return cur


def match_snapshot(row: dict[str, Any]) -> dict[str, Any] | None:
    match_id = str(row.get("id") or "").strip()
    if not match_id:
        return None

    league = row.get("league") or {}
    host = row.get("host") or {}
    guest = row.get("guest") or {}
    rd = row.get("rd") or {}
    score_home, score_away = parse_scoreline(row.get("ss"))

    home_team = str(host.get("n") or "").strip()
    away_team = str(guest.get("n") or "").strip()
    if home_team and away_team:
        match_name = f"{home_team} v {away_team}"
    else:
        match_name = home_team or away_team

    return {
        "match_id": match_id,
        "match_name": match_name,
        "league_id": str(league.get("i") or "").strip(),
        "league_name": str(league.get("fn") or league.get("n") or "").strip(),
        "home_team": home_team,
        "away_team": away_team,
        "status": str(row.get("status") or "").strip(),
        "home_corners": first_int(rd.get("hc"), row.get("hc"), nested_get(row, "score", "hc")),
        "away_corners": first_int(rd.get("gc"), row.get("gc"), nested_get(row, "score", "gc")),
        "home_score": first_int(rd.get("hg"), row.get("hg"), score_home),
        "away_score": first_int(rd.get("gg"), row.get("gg"), score_away),
    }


def int_or_zero(value: Any) -> int:
    parsed = to_int(value)
    if parsed is None:
        return 0
    return parsed


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
        "scoremer emit type={type} match_id={match_id} match={match_name!r} team={team} "
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
        "scoremer system type={type} severity={severity} stage={stage!r} status={status!r} "
        "attempt={attempt!r} stale_seconds={stale_seconds} suppressed_events={suppressed_events} "
        "message={message!r}".format(**payload),
        file=sys.stderr,
        flush=True,
    )


def count_delta_events(prev: MatchState, snapshot: dict[str, Any]) -> int:
    count = 0
    home_corners = snapshot["home_corners"]
    away_corners = snapshot["away_corners"]
    if prev.home_corners is not None and home_corners is not None and home_corners > prev.home_corners:
        count += home_corners - prev.home_corners
    if prev.away_corners is not None and away_corners is not None and away_corners > prev.away_corners:
        count += away_corners - prev.away_corners

    home_score = snapshot["home_score"]
    away_score = snapshot["away_score"]
    if prev.home_score is not None and home_score is not None and home_score > prev.home_score:
        count += home_score - prev.home_score
    if prev.away_score is not None and away_score is not None and away_score > prev.away_score:
        count += away_score - prev.away_score
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


def handle_score_data(data: dict[str, Any], league_ids: set[str], states: dict[str, MatchState], emit_deltas: bool = True) -> dict[str, int]:
    now = time.time()
    active_ids: set[str] = set()
    rows = data.get("rs") or []
    stats = {"rows": 0, "matched": 0, "tracked": len(states), "emitted": 0, "suppressed": 0}
    if not isinstance(rows, list):
        return stats
    stats["rows"] = len(rows)
    for row in rows:
        if not isinstance(row, dict):
            continue
        snapshot = match_snapshot(row)
        if not snapshot:
            continue
        if league_ids and snapshot["league_id"] not in league_ids:
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

    # Remove stale matches so a new fixture with the same ID cannot inherit old counts.
    for match_id, state in list(states.items()):
        if match_id not in active_ids and now - state.last_seen > 30 * 60:
            del states[match_id]
    stats["tracked"] = len(states)
    return stats


def first_query_value(payload: str | None, name: str) -> str:
    if not payload:
        return ""
    values = parse_qs(payload, keep_blank_values=True).get(name)
    if not values:
        return ""
    return str(values[0]).strip()


def _selftest() -> int:
    states: dict[str, MatchState] = {}
    base_row = {
        "id": "1533093",
        "league": {"i": "3559", "fn": "World Cup"},
        "host": {"n": "Uzbekistan"},
        "guest": {"n": "Colombia"},
        "status": "LIVE",
        "rd": {"hc": 0, "gc": 0, "hg": 0, "gg": 0},
    }
    corner_row = dict(base_row)
    corner_row["rd"] = {"hc": 1, "gc": 0, "hg": 0, "gg": 0}
    goal_row = dict(base_row)
    goal_row["rd"] = {"hc": 1, "gc": 0, "hg": 1, "gg": 0}
    catchup_row = dict(base_row)
    catchup_row["rd"] = {"hc": 3, "gc": 0, "hg": 2, "gg": 0}
    print("SELFTEST baseline", handle_score_data({"rs": [base_row]}, {"3559"}, states), file=sys.stderr)
    print("SELFTEST corner", handle_score_data({"rs": [corner_row]}, {"3559"}, states), file=sys.stderr)
    print("SELFTEST goal", handle_score_data({"rs": [goal_row]}, {"3559"}, states), file=sys.stderr)
    catchup_stats = handle_score_data({"rs": [catchup_row]}, {"3559"}, states, emit_deltas=False)
    print("SELFTEST catchup_suppressed", catchup_stats, file=sys.stderr)
    if catchup_stats["emitted"] != 0 or catchup_stats["suppressed"] != 3:
        print(f"SELFTEST failed catchup suppression: {catchup_stats}", file=sys.stderr)
        return 1
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="https://lv.scoremer.com/")
    parser.add_argument("--league-ids", default="3559")
    parser.add_argument("--poll-ms", type=int, default=5000)
    parser.add_argument("--timeout-ms", type=int, default=60000)
    parser.add_argument("--locale", default="en-CA")
    parser.add_argument("--os", default="windows")
    parser.add_argument("--selftest", action="store_true", help="run a local parser delta test and exit")
    args = parser.parse_args()
    if args.selftest:
        return _selftest()

    league_ids = parse_csv(args.league_ids)
    states: dict[str, MatchState] = {}
    last_response = time.monotonic()
    last_heartbeat = 0.0
    last_poll_error = 0.0
    problem_started = 0.0
    problem_stage = ""
    problem_status = ""
    fix_attempted_at = 0.0
    fix_failed_emitted = False
    suppress_next_delta = False
    reload_requested = False
    restart_requested = False
    token_wait_started = 0.0
    active_poll_count = 0
    last_heartbeat_poll_count = 0
    csrf_token = ""
    last_mt = 0
    filters = dict(DEFAULT_FILTERS)
    filters["filter_league_ids"] = [0] + [int(v) for v in league_ids if v.isdigit()]

    from camoufox.sync_api import Camoufox

    print(f"scoremer_monitor starting url={args.url!r} league_ids={sorted(league_ids)}", file=sys.stderr, flush=True)
    with Camoufox(headless=False, os=args.os, locale=args.locale, humanize=True, block_webrtc=True, window=(1440, 1000)) as browser:
        page = browser.new_page()
        page.add_init_script(
            "localStorage.setItem('filters', JSON.stringify(%s));" % json.dumps(filters, separators=(",", ":"))
        )

        def remember_scoremer_request(request) -> None:
            nonlocal csrf_token, last_mt
            if "/ajax/score/data" not in request.url:
                return
            token = first_query_value(request.post_data, "csrf_token")
            if token:
                csrf_token = token
            mt = to_int(first_query_value(request.post_data, "mt"))
            if mt is not None:
                last_mt = mt

        def is_active_poll_response(response) -> bool:
            try:
                headers = response.request.headers
            except Exception:
                return False
            return str(headers.get(ACTIVE_POLL_HEADER, "")).strip() == "1"

        def mark_scoremer_healthy(status: int, source: str, suppressed_events: int = 0) -> None:
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
                        "Scoremer polling recovered; normal OnEveryCorner monitoring has resumed.",
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
                    "scoremer heartbeat "
                    f"source={source} status={status} mt={last_mt} tracked={len(states)} "
                    f"active_polls={polls_since_log} poll_ms={args.poll_ms}",
                    file=sys.stderr,
                    flush=True,
                )
                last_heartbeat = now
                last_heartbeat_poll_count = active_poll_count

        def note_scoremer_response(status: int, source: str) -> None:
            mark_scoremer_healthy(status, source)

        def mark_scoremer_problem(status: int, source: str, detail: str = "") -> None:
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
                    "Scoremer polling is unhealthy; attempting to reload the browser page. "
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
                    "Scoremer polling is still unhealthy after a page reload; restarting the Scoremer monitor process.",
                    stage=problem_stage or source,
                    status=problem_status or status_text,
                    attempt="restart scoremer monitor process",
                    detail=detail,
                    stale_seconds=stale_seconds,
                )
            if now - last_poll_error >= 60:
                print(
                    f"scoremer unhealthy source={source} status={status_text} detail={detail!r} mt={last_mt}",
                    file=sys.stderr,
                    flush=True,
                )
                last_poll_error = now

        def process_scoremer_payload(body: str, source: str) -> None:
            nonlocal last_mt, suppress_next_delta
            try:
                data = json.loads(body)
            except Exception as exc:
                print(f"scoremer data parse failed source={source}: {exc!r}", file=sys.stderr, flush=True)
                return
            if not isinstance(data, dict):
                print(f"scoremer data parse failed source={source}: expected object", file=sys.stderr, flush=True)
                return
            mt = to_int(data.get("mt"))
            if mt is not None:
                last_mt = mt
            emit_deltas = not suppress_next_delta
            stats = handle_score_data(data, league_ids, states, emit_deltas=emit_deltas)
            suppressed = stats.get("suppressed", 0)
            mark_scoremer_healthy(200, source, suppressed_events=suppressed)
            print(
                "scoremer data ok source={source} rows={rows} matched={matched} tracked={tracked} "
                "emitted={emitted} suppressed={suppressed} mt={mt}".format(
                    source=source,
                    mt=last_mt,
                    **stats,
                ),
                file=sys.stderr,
                flush=True,
            )

        def active_poll_scoremer() -> None:
            nonlocal active_poll_count, csrf_token, last_poll_error, token_wait_started
            active_poll_count += 1
            fetch_timeout_ms = max(750, min(args.timeout_ms, max(args.poll_ms * 2, args.poll_ms + 500)))
            try:
                result = page.evaluate(
                    """async ({mt, csrfToken, timeoutMs, pollHeader}) => {
                        const tokenFromPage = () => {
                            const meta = document.querySelector('meta[name="csrf-token"], meta[name="csrf_token"]');
                            if (meta && meta.content) return meta.content;
                            const input = document.querySelector('input[name="csrf_token"], input[name="_token"]');
                            if (input && input.value) return input.value;
                            if (typeof csrf_token !== "undefined" && csrf_token) return csrf_token;
                            return window.csrf_token || window.csrfToken || window.CSRF_TOKEN || "";
                        };
                        const token = csrfToken || tokenFromPage();
                        if (!token) return {status: 0, error: "missing csrf_token"};

                        const numericMt = Number(mt);
                        const body = new URLSearchParams();
                        body.set("mt", Number.isFinite(numericMt) ? String(numericMt) : "0");
                        body.set("csrf_token", token);
                        if (!numericMt) body.set("nr", "1");

                        const controller = new AbortController();
                        const timer = setTimeout(() => controller.abort(), timeoutMs);
                        try {
                            const response = await fetch("/ajax/score/data", {
                                method: "POST",
                                credentials: "same-origin",
                                headers: {
                                    "accept": "application/json, text/javascript, */*; q=0.01",
                                    "content-type": "application/x-www-form-urlencoded; charset=UTF-8",
                                    "x-requested-with": "XMLHttpRequest",
                                    [pollHeader]: "1"
                                },
                                body,
                                signal: controller.signal
                            });
                            const text = response.status === 304 ? "" : await response.text();
                            return {
                                status: response.status,
                                statusText: response.statusText,
                                text,
                                csrfToken: token
                            };
                        } catch (error) {
                            return {status: 0, error: String(error), csrfToken: token};
                        } finally {
                            clearTimeout(timer);
                        }
                    }""",
                    {
                        "mt": last_mt,
                        "csrfToken": csrf_token,
                        "timeoutMs": fetch_timeout_ms,
                        "pollHeader": ACTIVE_POLL_HEADER,
                    },
                )
            except Exception as exc:
                now = time.monotonic()
                if now - last_poll_error >= 60:
                    print(f"scoremer active poll failed: {exc!r}", file=sys.stderr, flush=True)
                    last_poll_error = now
                mark_scoremer_problem(0, "active", repr(exc))
                return

            if not isinstance(result, dict):
                now = time.monotonic()
                if now - last_poll_error >= 60:
                    print(f"scoremer active poll failed: unexpected result {result!r}", file=sys.stderr, flush=True)
                    last_poll_error = now
                mark_scoremer_problem(0, "active", f"unexpected result {result!r}")
                return

            token = str(result.get("csrfToken") or "").strip()
            if token:
                csrf_token = token
            status = to_int(result.get("status")) or 0
            if status == 200:
                token_wait_started = 0.0
                process_scoremer_payload(str(result.get("text") or ""), "active")
                return
            if status in (204, 304):
                token_wait_started = 0.0
                note_scoremer_response(status, "active")
                return

            now = time.monotonic()
            if status == 0 and result.get("error") == "missing csrf_token":
                if token_wait_started == 0.0:
                    token_wait_started = now
                    return
                if now - token_wait_started >= HEARTBEAT_SECONDS and now - last_poll_error >= 60:
                    print("scoremer active poll waiting for csrf_token", file=sys.stderr, flush=True)
                    last_poll_error = now
                    mark_scoremer_problem(status, "active", "missing csrf_token")
                return
            if now - last_poll_error >= 60:
                print(
                    f"scoremer active poll failed status={status} error={result.get('error')!r} mt={last_mt}",
                    file=sys.stderr,
                    flush=True,
                )
                last_poll_error = now
            mark_scoremer_problem(status, "active", str(result.get("error") or ""))

        page.on("request", remember_scoremer_request)

        def on_response(response) -> None:
            if "/ajax/score/data" not in response.url:
                return
            if is_active_poll_response(response):
                return
            if response.status != 200:
                if response.status in (204, 304):
                    note_scoremer_response(response.status, "page")
                else:
                    mark_scoremer_problem(response.status, "page", response.status_text)
                return
            try:
                body = response.text()
            except Exception as exc:
                print(f"scoremer data read failed source=page: {exc!r}", file=sys.stderr, flush=True)
                return
            process_scoremer_payload(body, "page")

        page.on("response", on_response)
        page.goto(args.url, wait_until="domcontentloaded", timeout=args.timeout_ms)

        poll_seconds = max(0.25, args.poll_ms / 1000.0)
        next_poll = time.monotonic() + poll_seconds
        watchdog_seconds = max(60, poll_seconds * 12)
        while True:
            wait_ms = max(0, int((next_poll - time.monotonic()) * 1000))
            if wait_ms:
                page.wait_for_timeout(wait_ms)
            active_poll_scoremer()
            if reload_requested:
                reload_requested = False
                print("scoremer monitor reloading page after unhealthy polling", file=sys.stderr, flush=True)
                try:
                    page.reload(wait_until="domcontentloaded", timeout=args.timeout_ms)
                except Exception as exc:
                    emit_system_event(
                        "system_fix_failed",
                        "error",
                        "Scoremer page reload failed; restarting the Scoremer monitor process.",
                        stage=problem_stage or "active",
                        status=problem_status,
                        attempt="restart scoremer monitor process",
                        detail=repr(exc),
                        stale_seconds=int(time.monotonic() - problem_started) if problem_started else 0,
                    )
                    raise RuntimeError("scoremer page reload failed") from exc
            if restart_requested:
                raise RuntimeError("scoremer monitor restart requested after failed recovery")
            next_poll += poll_seconds
            if next_poll < time.monotonic():
                next_poll = time.monotonic()
            if time.monotonic() - last_response > watchdog_seconds:
                print("scoremer monitor watchdog reloading page", file=sys.stderr, flush=True)
                suppress_next_delta = True
                emit_system_event(
                    "system_fix_attempt",
                    "warning",
                    "Scoremer monitor watchdog has not seen a healthy response; reloading the browser page.",
                    stage="watchdog",
                    status="timeout",
                    attempt="page.reload",
                    stale_seconds=int(time.monotonic() - last_response),
                )
                last_response = time.monotonic()
                try:
                    page.reload(wait_until="domcontentloaded", timeout=args.timeout_ms)
                except Exception as exc:
                    emit_system_event(
                        "system_fix_failed",
                        "error",
                        "Scoremer watchdog reload failed; restarting the Scoremer monitor process.",
                        stage="watchdog",
                        status="timeout",
                        attempt="restart scoremer monitor process",
                        detail=repr(exc),
                    )
                    print(f"scoremer watchdog reload failed: {exc!r}", file=sys.stderr, flush=True)
                    raise RuntimeError("scoremer watchdog reload failed") from exc


if __name__ == "__main__":
    raise SystemExit(main())
