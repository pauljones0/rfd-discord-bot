import base64
import gzip
import hashlib
import importlib.util
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("state", Path(__file__).with_name("import_state.py"))
state = importlib.util.module_from_spec(spec)
spec.loader.exec_module(state)

class ImportStateTests(unittest.TestCase):
    def test_commands_preserve_subscriptions_and_exclude_receipts(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "fixture.sqlite"
            with sqlite3.connect(path) as db:
                db.executescript((ROOT / "src/schema.sql").read_text())
                if state.BOT == "rfd":
                    db.execute("INSERT INTO settings VALUES('discord-application-id','1001')")
                    db.execute("INSERT INTO deals VALUES('fixture',?,0,0)",[b'{}'])
                    db.execute("INSERT INTO subscriptions VALUES('1','42','rfd_all','{}')")
                else:
                    db.execute("INSERT INTO documents VALUES('crux_companies','fixture','{}','now','now')")
                    db.execute("INSERT INTO documents VALUES('subscriptions','fixture',?,'now','now')", [json.dumps({'subscriptionType':'crux'}).encode()])
            full, raw = state.snapshot(path, '1001')
            commands, reduced = state.snapshot(path, '1001', commands=True)
            table = 'subscriptions' if state.BOT == 'rfd' else 'documents'
            self.assertEqual(len(commands[table]),1)
            if state.BOT == 'rfd':
                self.assertEqual(commands['deals'],[])
                with self.assertRaises(ValueError): state.snapshot(path,'1002')
            else:
                self.assertEqual(commands['documents'][0][0],'subscriptions')
                self.assertEqual(commands['crux_notification_outbox'],[])
            name, writes = state.checkpoint_writes('test','fixture-fixture', 'fixture:1001',raw)
            self.assertEqual(writes[-1]['currentDocument'], {'exists':False})
            manifest = json.loads(writes[-1]['update']['fields']['payload']['stringValue'])
            chunks = [base64.b64decode(w['update']['fields']['data']['bytesValue']) for w in writes[:-1]]
            self.assertEqual(gzip.decompress(b''.join(chunks)),raw)
            self.assertEqual(manifest['hashes'], [hashlib.sha256(c).hexdigest() for c in chunks])
            self.assertLess(len(reduced),len(raw))

    def test_invalid_namespace_is_rejected_before_remote_calls(self):
        with self.assertRaises(ValueError): state.checkpoint_writes('test','../../foreign','identity',b'{}')

if __name__ == '__main__': unittest.main()
