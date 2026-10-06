"""Real HTTP integration tests; Windows services are replaced by a test-only engine."""
import base64
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile
import time
import unittest
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
PWSH = os.environ.get('PWSH', 'pwsh')
PASSWORD = 'temporary integration password'

class PanelFixture:
    def __init__(self):
        self.temp = tempfile.TemporaryDirectory(prefix='omnibox-ai-http-')
        self.root = Path(self.temp.name)
        shutil.copytree(ROOT / 'web', self.root / 'web')
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            self.port = sock.getsockname()[1]
        salt = secrets.token_bytes(32)
        credential = dict(salt=base64.b64encode(salt).decode(), hash=base64.b64encode(hashlib.pbkdf2_hmac('sha256', PASSWORD.encode(), salt, 210000)).decode(), iterations=210000)
        (self.root / 'panel.json').write_text(json.dumps(dict(schema_version=1, port=self.port, allowed_hosts=['127.0.0.1', 'localhost'], credential=credential, ssh_target='avoq@192.168.1.77')))
        logs = self.root / 'logs'
        logs.mkdir()
        (logs / 'bonsai-service.err.log').write_text('fixture error log\n')
        (self.root / 'config.json').write_text(json.dumps(dict(context='196608', service='Running', backends=dict(bonsai=dict(log_directory=str(logs), log_prefix='bonsai-service'), ollama=dict(log_directory=str(logs), log_prefix='ollama-service')))))
        self.url = f'http://127.0.0.1:{self.port}'
        self.output = (self.root / 'server-output.log').open('w')
        self.process = subprocess.Popen([PWSH, '-NoProfile', '-File', str(ROOT / 'tests/http-fixture.ps1'), '-Root', str(self.root)], stdout=self.output, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                self.output.flush()
                raise RuntimeError((self.root / 'server-output.log').read_text())
            try:
                urllib.request.urlopen(self.url, timeout=.5).close()
                break
            except (urllib.error.URLError, TimeoutError):
                time.sleep(.05)
        else:
            self.close()
            raise RuntimeError('Panel did not start')

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
        self.output.close()
        self.temp.cleanup()

class HttpTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture = PanelFixture()
        cls.cookies = http.cookiejar.CookieJar()
        cls.client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cls.cookies))
        status, data, _ = cls.call('/api/login', {'password': PASSWORD})
        if status != 200:
            raise RuntimeError(data)
        cls.csrf = data['csrf']

    @classmethod
    def tearDownClass(cls):
        cls.fixture.close()

    @classmethod
    def call(cls, path, body=None, *, auth=True, csrf=None, origin=None, method=None):
        headers = {}
        if body is not None:
            headers['Content-Type'] = 'application/json'
        if csrf is not None:
            headers['X-Omnibox-CSRF'] = csrf
        if origin:
            headers['Origin'] = origin
        request = urllib.request.Request(cls.fixture.url + path, data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
        opener = cls.client if auth else urllib.request.build_opener()
        try:
            response = opener.open(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read().decode()
            data = json.loads(raw) if response.headers.get('Content-Type', '').startswith('application/json') else raw
            return response.status, data, response.headers

    def wait_operation(self, identifier):
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            _, state, _ = self.call('/api/status')
            op = next(x for x in state['operations'] if x['id'] == identifier)
            if op['state'] != 'running':
                self.assertEqual(op['state'], 'done', op)
                return
            time.sleep(.1)
        self.fail('Operation did not finish')

    def test_01_static_ui_and_security_headers(self):
        code, html, headers = self.call('/', auth=False)
        self.assertEqual(code, 200)
        self.assertIn('Остановить все серверы', html)
        self.assertNotIn('Перед игрой', html)
        self.assertIn("script-src 'self'", headers['Content-Security-Policy'])
        self.assertEqual(headers['X-Frame-Options'], 'DENY')
        self.assertEqual(headers['Cache-Control'], 'no-store')
        self.assertEqual(self.call('/app.js', auth=False)[0], 200)

    def test_02_status_requires_login(self):
        self.assertEqual(self.call('/api/status', auth=False)[0], 401)

    def test_03_wrong_password(self):
        self.assertEqual(self.call('/api/login', {'password': 'incorrect'}, auth=False)[0], 401)

    def test_04_authenticated_status_hides_credentials(self):
        code, data, _ = self.call('/api/status?gpu=1')
        self.assertEqual(code, 200)
        self.assertEqual(len(data['backends']), 2)
        self.assertEqual(data['gpu'][0]['used'], 15020)
        self.assertNotIn('credential', json.dumps(data))
        self.assertNotIn('hash', json.dumps(data))

    def test_05_actions_require_csrf(self):
        self.assertEqual(self.call('/api/actions', {'action': 'stopall'})[0], 403)

    def test_06_cross_origin_action_is_rejected(self):
        self.assertEqual(self.call('/api/actions', {'action': 'stopall'}, csrf=self.csrf, origin='http://evil.example')[0], 403)

    def test_07_unknown_and_oversize_requests_are_rejected(self):
        self.assertEqual(self.call('/api/actions', {'action': 'exec'}, csrf=self.csrf)[0], 400)
        self.assertEqual(self.call('/api/actions', {'action': 'start', 'extra': 'x' * 20000}, csrf=self.csrf)[0], 400)

    def test_08_worker_is_async_and_control_busy_is_rejected(self):
        code, op, _ = self.call('/api/actions', {'action': 'apply', 'backend': 'bonsai', 'settings': {'context': '128k'}}, csrf=self.csrf)
        self.assertEqual(code, 202)
        self.assertEqual(self.call('/api/actions', {'action': 'start'}, csrf=self.csrf)[0], 400)
        started = time.monotonic()
        self.assertEqual(self.call('/api/session')[0], 200)
        self.assertLess(time.monotonic() - started, .3)
        self.wait_operation(op['id'])
        self.assertEqual(self.call('/api/status')[1]['backends'][0]['settings']['context'], '131072')

    def test_09_stop_is_available_during_download(self):
        code, download, _ = self.call('/api/actions', {'action': 'download', 'backend': 'bonsai', 'url': 'fixture'}, csrf=self.csrf)
        self.assertEqual(code, 202)
        code, stop, _ = self.call('/api/actions', {'action': 'stopall'}, csrf=self.csrf)
        self.assertEqual(code, 202)
        self.wait_operation(stop['id'])
        self.wait_operation(download['id'])
        self.assertEqual(self.call('/api/status')[1]['backends'][0]['service'], 'Stopped')

    def test_10_logs_are_scoped_and_path_traversal_fails(self):
        code, data, _ = self.call('/api/logs?backend=bonsai')
        self.assertEqual(code, 200)
        self.assertIn('fixture error log', data['logs'][0]['text'])
        self.assertEqual(self.call('/api/logs?backend=../../panel.json')[0], 400)
        self.assertEqual(self.call('/panel.json')[0], 404)
        self.assertEqual(self.call('/config.json')[0], 404)

    def test_11_no_cors_preflight_or_arbitrary_methods(self):
        self.assertEqual(self.call('/api/actions', method='OPTIONS')[0], 405)

    def test_12_logout_invalidates_session(self):
        self.assertEqual(self.call('/api/logout', {}, csrf=self.csrf)[0], 200)
        self.assertEqual(self.call('/api/session')[0], 401)

    def test_13_login_rate_limit(self):
        self.assertEqual(self.call('/api/login', {'password': PASSWORD}, auth=False)[0], 200)
        for _ in range(6):
            self.assertEqual(self.call('/api/login', {'password': 'incorrect'}, auth=False)[0], 401)
        self.assertEqual(self.call('/api/login', {'password': PASSWORD}, auth=False)[0], 429)

if __name__ == '__main__':
    unittest.main(verbosity=2)
