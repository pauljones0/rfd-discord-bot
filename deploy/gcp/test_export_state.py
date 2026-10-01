import base64
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path
import export_state
import import_state

ROOT = Path(__file__).resolve().parents[2]

class ExportStateTests(unittest.TestCase):
    def fixture(self):
        if import_state.BOT == 'rfd':
            data = {'deals': [['fixture', json.dumps({'Threads':[{'DocumentID':'thread'}],'DiscordMessages':{'42':'receipt'}}),0,0]], 'subscriptions': [['1','42','rfd_all','{}']], 'settings': [['discord-application-id','1001']]}
        else:
            data = {'documents': [['subscriptions','1',json.dumps({'subscriptionType':'crux'}),'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z']], 'crux_notification_outbox': [['delivery','42','[]',0,'2026-01-01T00:00:00Z',None,None,None,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',None]]}
        raw = json.dumps(data).encode()
        name, writes = import_state.checkpoint_writes('test','bot-fixture', f'{import_state.BOT}:fixture:1001',raw)
        docs = {w['update']['name']:w['update'] for w in writes}
        docs[name]['updateTime'] = 'fixture-time'
        calls = []
        def rpc(method,path,body=None):
            calls.append((method,path,body))
            if path.endswith(':beginTransaction'):
                self.assertEqual(body,{'options':{'readOnly':{}}})
                return {'transaction':'/consistent+snapshot='}
            if path.endswith(':rollback'):
                self.assertEqual(body,{'transaction':'/consistent+snapshot='})
                return {}
            clean, query = path.split('?')
            self.assertEqual(query,'transaction=%2Fconsistent%2Bsnapshot%3D')
            return docs[clean]
        return data, docs, calls, rpc

    def test_consistent_restore_preserves_receipts_pending_and_private_permissions(self):
        expected, docs, calls, rpc = self.fixture()
        config = {'GCP_PROJECT':'test','FIRESTORE_NAMESPACE':'bot-fixture','BOT_DEPLOYMENT_MODE':'fixture','DISCORD_APP_ID':'1001'}
        data, update = export_state.read_checkpoint(config,rpc)
        self.assertEqual(data,expected)
        self.assertEqual(update,'fixture-time')
        self.assertTrue(calls[-1][1].endswith(':rollback'))
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)/'state.sqlite'
            export_state.write_snapshot(data,target,(ROOT/'src/schema.sql').read_text())
            self.assertEqual(target.stat().st_mode & 0o777,0o600)
            with sqlite3.connect(target) as db:
                for table,rows in data.items():
                    self.assertEqual(db.execute(f'SELECT * FROM {table}').fetchall(),[tuple(row) for row in rows])
                if import_state.BOT == 'rfd':
                    self.assertEqual(db.execute('SELECT * FROM deal_threads').fetchall(),[('thread','fixture')])
            original = target.read_bytes()
            with self.assertRaises(ValueError):export_state.write_snapshot(data,target,'')
            self.assertEqual(target.read_bytes(),original)

    def test_corrupt_chunk_and_foreign_identity_fail_and_release_transaction(self):
        for corruption in ('chunk','identity'):
            _, docs, calls, rpc = self.fixture()
            manifest = next(doc for path,doc in docs.items() if path.endswith('/bot-fixture'))
            if corruption == 'chunk':
                next(doc for path,doc in docs.items() if path.endswith('/0'))['fields']['data']['bytesValue'] = base64.b64encode(b'bad').decode()
            else:
                value = json.loads(manifest['fields']['payload']['stringValue'])
                value['identity'] = 'foreign'
                manifest['fields']['payload']['stringValue'] = json.dumps(value)
            config = {'GCP_PROJECT':'test','FIRESTORE_NAMESPACE':'bot-fixture','BOT_DEPLOYMENT_MODE':'fixture','DISCORD_APP_ID':'1001'}
            with self.assertRaises(ValueError):export_state.read_checkpoint(config,rpc)
            self.assertTrue(calls[-1][1].endswith(':rollback'))

if __name__ == '__main__':unittest.main()
