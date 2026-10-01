#!/usr/bin/env python3
"""Export a consistent Firestore checkpoint to a NEW private native SQLite file."""
import argparse
import base64
import gzip
import hashlib
import io
import json
import os
import re
import sqlite3
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
import import_state


def read_checkpoint(config, rpc):
    project, namespace = config['GCP_PROJECT'], config['FIRESTORE_NAMESPACE']
    mode, app_id = config['BOT_DEPLOYMENT_MODE'], config['DISCORD_APP_ID']
    if mode not in ('fixture', 'production') or not namespace.endswith('-' + mode):
        raise ValueError('mode/namespace mismatch')
    if not all(re.fullmatch(r'[a-zA-Z0-9_-]{1,100}', v) for v in (project, namespace)):
        raise ValueError('invalid project or namespace')
    root = f'projects/{project}/databases/(default)/documents'
    name = root + '/bot_checkpoints/' + namespace
    transaction = rpc('POST', root + ':beginTransaction', {'options': {'readOnly': {}}})['transaction']
    query = '?transaction=' + urllib.parse.quote(transaction, safe='')
    try:
        doc = rpc('GET', name + query)
        manifest = json.loads(doc['fields']['payload']['stringValue'])
        if manifest['format'] != 1 or manifest['identity'] != f'{import_state.BOT}:{mode}:{app_id}':
            raise ValueError('foreign checkpoint identity/format')
        hashes = manifest['hashes']
        if not isinstance(hashes, list) or not 1 <= len(hashes) <= 12:
            raise ValueError('invalid checkpoint chunk count')
        pieces = []
        for index, digest in enumerate(hashes):
            chunk = rpc('GET', name + f'/bot_checkpoint_chunks/{index}' + query)
            piece = base64.b64decode(chunk['fields']['data']['bytesValue'], validate=True)
            if len(piece) > import_state.CHUNK or hashlib.sha256(piece).hexdigest() != digest:
                raise ValueError('checkpoint chunk hash/size mismatch')
            pieces.append(piece)
        compressed = b''.join(pieces)
        if len(compressed) != manifest['compressed_bytes']:
            raise ValueError('checkpoint compressed size mismatch')
        with gzip.GzipFile(fileobj=io.BytesIO(compressed)) as stream:
            raw = stream.read(32 * 1024 * 1024 + 1)
        if len(raw) > 32 * 1024 * 1024 or len(raw) != manifest['raw_bytes']:
            raise ValueError('checkpoint raw size mismatch')
        data = json.loads(raw)
        if set(data) != set(import_state.TABLES):
            raise ValueError('unexpected checkpoint tables')
        return data, doc['updateTime']
    finally:
        rpc('POST', root + ':rollback', {'transaction': transaction})


def write_snapshot(data, target, schema):
    if target.exists() or target.is_symlink():
        raise ValueError('refusing to overwrite existing destination')
    target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, temporary = tempfile.mkstemp(prefix='.bot-export-', dir=target.parent)
    os.close(fd)
    try:
        with sqlite3.connect(temporary) as db:
            db.executescript(schema)
            with db:
                for table in import_state.TABLES:
                    width = len(db.execute(f'PRAGMA table_info({table})').fetchall())
                    rows = data[table]
                    if not isinstance(rows, list) or any(not isinstance(row, list) or len(row) != width or any(isinstance(v, (dict, list, bool)) for v in row) for row in rows):
                        raise ValueError('invalid checkpoint row schema')
                    db.executemany(f'INSERT INTO {table} VALUES ({",".join("?" for _ in range(width))})', rows)
            if db.execute('PRAGMA integrity_check').fetchone()[0] != 'ok':
                raise ValueError('restored SQLite integrity check failed')
            db.execute('PRAGMA wal_checkpoint(TRUNCATE)')
            db.execute('PRAGMA journal_mode=DELETE')
        with open(temporary, 'rb') as output:
            os.fsync(output.fileno())
        # Atomic no-clobber publication; the temporary file is already mode 0600.
        os.link(temporary, target)
    finally:
        for suffix in ('', '-wal', '-shm', '-journal'):
            try:
                os.unlink(temporary + suffix)
            except FileNotFoundError:
                pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists() or args.output.is_symlink():
        raise ValueError('refusing to overwrite existing destination')
    config = json.loads(args.config.read_text())
    token = subprocess.check_output(['gcloud', 'auth', 'print-access-token'], text=True).strip()
    headers = {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'}
    def rpc(method, path, body=None):
        request = urllib.request.Request('https://firestore.googleapis.com/v1/' + path, data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError('Firestore export HTTP ' + str(error.code)) from None
    data, update = read_checkpoint(config, rpc)
    schema = (Path(__file__).resolve().parents[2] / 'src/schema.sql').read_text()
    write_snapshot(data, args.output, schema)
    print(json.dumps({'bot': import_state.BOT, 'namespace': config['FIRESTORE_NAMESPACE'], 'checkpoint_update_time': update, 'rows': {k: len(v) for k, v in data.items()}}))


if __name__ == '__main__':
    main()
