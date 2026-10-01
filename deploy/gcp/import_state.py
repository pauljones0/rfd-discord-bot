#!/usr/bin/env python3
"""Seed an EMPTY Firestore namespace from a stopped, private SQLite snapshot.
Uses the logged-in gcloud identity. Runtime never uses this tool or a key file.
"""
import argparse
import base64
import gzip
import hashlib
import json
import re
import sqlite3
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

BOT = "rfd"
TABLES = ['deals', 'subscriptions', 'settings']
CHUNK = 512 * 1024


def snapshot(path, app_id, commands=False):
    source = path.resolve(strict=True)
    with sqlite3.connect("file:" + urllib.parse.quote(str(source)) + "?mode=ro", uri=True) as db:
        if db.execute("PRAGMA integrity_check").fetchone()[0] != "ok":
            raise ValueError("source integrity check failed")
        # database/sql saved Go []byte JSON as SQLite BLOBs in TEXT columns.
        # Convert only those known JSON/text columns and require valid UTF-8.
        blob_positions = {'deals': {1}, 'subscriptions': {3}, 'settings': {1}}
        expected_columns = {'deals': ['id', 'payload', 'published_at', 'updated_at'], 'subscriptions': ['guild_id', 'channel_id', 'filter', 'payload'], 'settings': ['key', 'payload']}
        data = {}
        for table in TABLES:
            if [column[1] for column in db.execute(f"PRAGMA table_info({table})")] != expected_columns[table]:
                raise ValueError("unexpected source columns/order")
            rows = []
            for source_row in db.execute(f"SELECT * FROM {table} ORDER BY rowid"):
                row = list(source_row)
                for index, value in enumerate(row):
                    if isinstance(value, bytes):
                        if index not in blob_positions[table]:
                            raise ValueError("unexpected binary value outside a legacy JSON column")
                        row[index] = value.decode("utf-8", errors="strict")
                rows.append(row)
            data[table] = rows
        expected = {'deals': 4, 'subscriptions': 4, 'settings': 2}
        if any(len(row) != expected[table] or any(isinstance(v, bytes) for v in row) for table, rows in data.items() for row in rows):
            raise ValueError("unexpected source schema")
        if BOT == "rfd":
            binding = dict(data["settings"]).get("discord-application-id")
            if binding != app_id:
                raise ValueError("RFD snapshot application binding does not match")
            if commands:
                data["deals"] = []
                data["settings"] = [["discord-application-id", binding]]
        elif commands:
            data["documents"] = [row for row in data["documents"] if row[0] == "subscriptions" and json.loads(row[2]).get("subscriptionType") == "crux"]
            data["crux_notification_outbox"] = []
    raw = json.dumps(data, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode()
    if len(raw) > 32 * 1024 * 1024:
        raise ValueError("snapshot exceeds 32 MiB raw limit")
    return data, raw


def checkpoint_writes(project, namespace, identity, raw):
    if not re.fullmatch(r"[a-zA-Z0-9_-]{1,100}", project) or not re.fullmatch(r"[a-zA-Z0-9_-]{1,100}", namespace):
        raise ValueError("invalid project or namespace")
    name = f"projects/{project}/databases/(default)/documents/bot_checkpoints/{namespace}"
    compressed = gzip.compress(raw, compresslevel=1, mtime=0)
    if len(compressed) > CHUNK * 12:
        raise ValueError("snapshot exceeds compressed checkpoint limit")
    pieces = [compressed[i:i+CHUNK] for i in range(0, len(compressed), CHUNK)]
    hashes = [hashlib.sha256(piece).hexdigest() for piece in pieces]
    manifest = {"format": 1, "identity": identity, "hashes": hashes, "owner": "", "expires": 0, "raw_bytes": len(raw), "compressed_bytes": len(compressed)}
    writes = [{"update": {"name": f"{name}/bot_checkpoint_chunks/{i}", "fields": {"data": {"bytesValue": base64.b64encode(piece).decode()}}}} for i, piece in enumerate(pieces)]
    writes.append({"update": {"name": name, "fields": {"payload": {"stringValue": json.dumps(manifest, separators=(",", ":"))}}}, "currentDocument": {"exists": False}})
    return name, writes


def import_snapshot(config, source):
    project, namespace = config["GCP_PROJECT"], config["FIRESTORE_NAMESPACE"]
    mode, app_id = config["BOT_DEPLOYMENT_MODE"], config["DISCORD_APP_ID"]
    if mode not in ("fixture", "production") or not namespace.endswith("-" + mode):
        raise ValueError("mode/namespace mismatch")
    commands = config.get("BOT_REQUEST_ROLE") == "commands"
    data, raw = snapshot(source, app_id, commands)
    name, writes = checkpoint_writes(project, namespace, f"{BOT}:{mode}:{app_id}", raw)
    token = subprocess.check_output(["gcloud", "auth", "print-access-token"], text=True).strip()
    headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json"}
    url = "https://firestore.googleapis.com/v1/" + name
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=30):
            raise ValueError("destination already exists; refusing to overwrite")
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise RuntimeError("Firestore destination lookup HTTP " + str(error.code)) from None
    root = name.split("/bot_checkpoints/")[0]
    request = urllib.request.Request("https://firestore.googleapis.com/v1/" + root + ":commit", data=json.dumps({"writes": writes}, separators=(",", ":")).encode(), headers=headers, method="POST")
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            result = json.load(response)
    except urllib.error.HTTPError as error:
        raise RuntimeError("Atomic import failed HTTP " + str(error.code) + "; retry checks destination before writing") from None
    if len(result.get("writeResults", [])) != len(writes):
        raise RuntimeError("unexpected import acknowledgement; inspect before retry")
    return {"bot": BOT, "namespace": namespace, "commands_only": commands, "rows": {k: len(v) for k, v in data.items()}, "raw_bytes": len(raw), "chunks": len(writes)-1}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True, help="Private deployment JSON")
    parser.add_argument("--snapshot", type=Path, required=True, help="Stopped producer snapshot; never a running database")
    args = parser.parse_args()
    print(json.dumps(import_snapshot(json.loads(args.config.read_text()), args.snapshot), sort_keys=True))


if __name__ == "__main__":
    main()
