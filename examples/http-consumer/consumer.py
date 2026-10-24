#!/usr/bin/env python3
"""独立的无 AI 处理程序：验证附件，登记结果，并可选择回复原会话。"""
import hashlib
import hmac
import json
import os
import sqlite3
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from socketserver import TCPServer

API = os.environ.get('INBOX_URL', 'http://127.0.0.1:8787').rstrip('/')
TOKEN = os.environ['INBOX_TOKEN']
SECRET = os.environ['WEBHOOK_SECRET']
REPLY = os.environ.get('INBOX_REPLY', 'false').lower() == 'true'
DATA = Path(os.environ.get('CONSUMER_DATA', './consumer-data'))
DATA.mkdir(mode=0o700, parents=True, exist_ok=True)
db = sqlite3.connect(DATA / 'events.db')
db.execute('CREATE TABLE IF NOT EXISTS processed (event_id TEXT PRIMARY KEY)')
os.chmod(DATA / 'events.db', 0o600)


def api(path, body=None):
    headers = {'Authorization': 'Bearer ' + TOKEN}
    encoded = None
    if body is not None:
        encoded = json.dumps(body, ensure_ascii=False).encode()
        headers['Content-Type'] = 'application/json'
    request = urllib.request.Request(API + path, data=encoded, headers=headers)
    return urllib.request.urlopen(request, timeout=8)


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200 if self.path == '/healthz' else 404)
        self.end_headers()

    def do_POST(self):
        if self.path != '/inbox':
            self.send_error(404)
            return
        try:
            length = int(self.headers.get('Content-Length', '0'))
            stamp = self.headers.get('X-WeCom-Inbox-Timestamp', '')
            if not 0 < length <= 1048576 or abs(time.time() - int(stamp)) > 300:
                raise ValueError('invalid size or timestamp')
            body = self.rfile.read(length)
            signature = 'sha256=' + hmac.new(SECRET.encode(), stamp.encode() + b'.' + body, hashlib.sha256).hexdigest()
            if not hmac.compare_digest(signature, self.headers.get('X-WeCom-Inbox-Signature', '')):
                raise ValueError('invalid signature')
            event = json.loads(body)
            event_id = event['event_id']
            if event_id != self.headers.get('X-WeCom-Inbox-Event'):
                raise ValueError('event ID mismatch')
            if db.execute('SELECT 1 FROM processed WHERE event_id=?', (event_id,)).fetchone():
                self.send_response(204)
                self.end_headers()
                return
            message = event['message']
            verified = 0
            for attachment in message['attachments']:
                if attachment['status'] != 'ready':
                    continue
                digest = hashlib.sha256()
                size = 0
                with api('/v1/attachments/' + attachment['id']) as response:
                    while chunk := response.read(65536):
                        digest.update(chunk)
                        size += len(chunk)
                if digest.hexdigest() != attachment['sha256'] or size != attachment['size']:
                    raise ValueError('attachment integrity mismatch')
                verified += 1
            failed = any(a['status'] != 'ready' for a in message['attachments'])
            result = {
                'idempotency_key': event_id,
                'status': 'failed' if failed else 'completed',
                'summary': f'独立处理程序已校验 {verified} 个附件 / Independent consumer verified {verified} attachment(s).'
                           + (' 部分附件不可用 / Some attachments unavailable.' if failed else ''),
                'reply': REPLY,
            }
            with api('/v1/messages/' + str(message['id']) + '/results', result) as response:
                response.read()
            db.execute('INSERT OR IGNORE INTO processed VALUES (?)', (event_id,))
            db.commit()
            self.send_response(204)
            self.end_headers()
        except (ValueError, KeyError, TypeError, urllib.error.URLError, sqlite3.Error):
            # 不把消息、凭据或签名地址写进日志。
            self.send_error(503, 'Processing failed; event can be retried')

    def log_message(self, fmt, *args):
        print('consumer:', args[1] if len(args) > 1 else 'request', flush=True)


class LoopbackServer(HTTPServer):
    def server_bind(self):
        # 本机监听直接使用绑定地址，不让启动依赖可能阻塞的反向 DNS。
        TCPServer.server_bind(self)
        self.server_name, self.server_port = self.server_address[:2]


if __name__ == '__main__':
    server = LoopbackServer(('127.0.0.1', int(os.environ.get('CONSUMER_PORT', '8790'))), Handler)
    print('Consumer ready; no AI provider configured.', flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        db.close()
