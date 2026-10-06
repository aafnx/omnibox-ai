"""Real HTTP integration tests; Windows services are replaced by a test-only engine."""
import base64
import hashlib
import http.cookiejar
import http.client
import http.server
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
PWSH = os.environ.get('PWSH', 'pwsh')
PASSWORD = 'temporary integration password'
API_KEY = 'fixture-only-api-key'

class Upstream(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        self.server.seen_auth = self.headers.get('Authorization')
        body = json.dumps({'object':'list','data':[{'id':'bonsai-2-27b','object':'model'}]}).encode()
        self.send_response(200); self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(body))); self.end_headers(); self.wfile.write(body)

    def do_POST(self):
        self.server.seen_auth = self.headers.get('Authorization')
        data = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        if data.get('stream'):
            self.send_response(200); self.send_header('Content-Type','text/event-stream'); self.end_headers()
            self.wfile.write(b'data: {"choices":[{"delta":{"content":"first"}}],"usage":null,"prompt_progress":{"total":11,"processed":11},"timings":{"predicted_n":3}}\n\n'); self.wfile.flush()
            time.sleep(1.2)
            self.wfile.write(b'data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}\n\ndata: [DONE]\n\n'); self.wfile.flush()
        else:
            reply={'choices':[{'message':{'content':'fixture'}}],'forwarded':data}
            if data.get('fixture_usage',True):reply['usage']={'prompt_tokens':11,'completion_tokens':7,'total_tokens':18}
            body=json.dumps(reply).encode()
            self.send_response(200); self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(body))); self.end_headers(); self.wfile.write(body)

class PanelFixture:
    def __init__(self, proxy=False, gateway=False, shared_port=False, updates=False):
        self.temp = tempfile.TemporaryDirectory(prefix='omnibox-ai-http-')
        self.root = Path(self.temp.name)
        shutil.copytree(ROOT / 'web', self.root / 'web')
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            self.port = sock.getsockname()[1]
        salt = secrets.token_bytes(32)
        credential = dict(salt=base64.b64encode(salt).decode(), hash=base64.b64encode(hashlib.pbkdf2_hmac('sha256', PASSWORD.encode(), salt, 210000)).decode(), iterations=210000)
        panel = dict(schema_version=1, port=self.port, allowed_hosts=['127.0.0.1', 'localhost'], credential=credential, ssh_target='avoq@192.168.1.77')
        if proxy:
            panel.update(public_origin='https://ai.example.com', trusted_proxy_addresses=['127.0.0.1'])
        (self.root / 'panel.json').write_text(json.dumps(panel))
        logs = self.root / 'logs'
        logs.mkdir()
        (logs / 'bonsai-service.err.log').write_text('fixture error log\n')
        (self.root / 'config.json').write_text(json.dumps(dict(context='196608', service='Running', backends=dict(bonsai=dict(log_directory=str(logs), log_prefix='bonsai-service'), ollama=dict(log_directory=str(logs), log_prefix='ollama-service')))))
        self.upstream = None
        if gateway:
            self.upstream = http.server.ThreadingHTTPServer(('127.0.0.1',0), Upstream)
            threading.Thread(target=self.upstream.serve_forever,daemon=True).start()
            with socket.socket() as sock:
                sock.bind(('127.0.0.1',0)); self.api_port=sock.getsockname()[1]
            if shared_port:self.api_port=self.port
            with socket.socket() as sock:
                sock.bind(('127.0.0.1',0)); unavailable=sock.getsockname()[1]
            state=json.loads((self.root/'config.json').read_text())
            state['backends']['bonsai']['base_url']=f'http://127.0.0.1:{self.upstream.server_port}'
            state['backends']['ollama']['base_url']=f'http://127.0.0.1:{unavailable}'
            (self.root/'config.json').write_text(json.dumps(state))
            (self.root/'gateway.json').write_text(json.dumps(dict(schema_version=1,enabled=True,port=self.api_port,base_url=f'http://127.0.0.1:{self.api_port}/v1',backend='bonsai',require_key=True,key_hash=hashlib.sha256(API_KEY.encode()).hexdigest())))
        self.url = f'http://127.0.0.1:{self.port}'
        self.output = (self.root / 'server-output.log').open('w')
        process_env=os.environ.copy()
        if updates:
            fixture_payload=base64.b64encode(b'test fixture payload').decode()
            fixture_sha=hashlib.sha256(b'test fixture payload').hexdigest()
            installer=f"#requires -Version 7.4\n$expectedSha = '{fixture_sha}'\n$payload = '{fixture_payload}'\nif($CheckOnly){{'OmniBox AI 9.9.9 payload OK'}}\nthrow 'Must not execute during check'\n"
            (self.root/'contents.json').write_text(json.dumps(dict(type='file',encoding='base64',content=base64.b64encode(installer.encode()).decode())))
            fake=self.root/'fake-bin';fake.mkdir()
            gh=fake/'gh'
            gh.write_text("#!/usr/bin/env python3\nimport sys,time,pathlib\ntime.sleep(.7)\nprint((pathlib.Path(__file__).parent.parent/'contents.json').read_text())\n")
            gh.chmod(0o755)
            process_env['PATH']=str(fake)+os.pathsep+process_env.get('PATH','')
        self.process = subprocess.Popen([PWSH, '-NoProfile', '-File', str(ROOT / 'tests/http-fixture.ps1'), '-Root', str(self.root)], stdout=self.output, stderr=subprocess.STDOUT, env=process_env)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                self.output.flush()
                raise RuntimeError((self.root / 'server-output.log').read_text())
            try:
                urllib.request.urlopen(self.url, timeout=.5).close()
                break
            except (urllib.error.URLError, TimeoutError, ConnectionResetError):
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
        if self.upstream:
            self.upstream.shutdown(); self.upstream.server_close()
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

    def test_09b_storage_usage_settings_and_cleanup(self):
        self.assertEqual(self.call('/api/storage', auth=False)[0], 401)
        code, data, _ = self.call('/api/storage')
        self.assertEqual(code, 200)
        self.assertGreater(data['logs_bytes'], 0)
        self.assertEqual(data['log_retention_days'], 30)
        self.assertEqual(self.call('/api/storage', {'action': 'save', 'log_retention_days': 7})[0], 403)
        code, data, _ = self.call('/api/storage', {'action': 'save', 'log_retention_days': 7, 'log_warn_mb': 10}, csrf=self.csrf)
        self.assertEqual(code, 200)
        self.assertEqual((data['log_retention_days'], data['log_warn_mb']), (7, 10))
        self.assertEqual(json.loads((self.fixture.root / 'storage.json').read_text())['log_retention_days'], 7)
        self.assertEqual(self.call('/api/storage', {'action': 'save', 'log_retention_days': 9999}, csrf=self.csrf)[0], 400)
        self.assertEqual(self.call('/api/storage', {'action': 'delete', 'path': '/'}, csrf=self.csrf)[0], 400)
        old = self.fixture.root / 'logs' / 'ollama-service.out.log.1'
        old.write_text('old\n')
        stamp = time.time() - 10 * 86400
        os.utime(old, (stamp, stamp))
        code, data, _ = self.call('/api/storage', {'action': 'cleanup'}, csrf=self.csrf)
        self.assertEqual(code, 200)
        self.assertEqual(data['cleanup']['removed'], 1)
        self.assertFalse(old.exists())
        self.assertTrue((self.fixture.root / 'logs' / 'bonsai-service.err.log').exists())

    def test_09c_engine_overview_and_release_lookup(self):
        self.assertEqual(self.call('/api/engines', auth=False)[0], 401)
        code, data, _ = self.call('/api/engines')
        self.assertEqual(code, 200)
        self.assertEqual([e['cli'] for e in data['engines']], ['prismml', 'ollama'])
        self.assertEqual(self.call('/api/engine-releases', {'engine': 'prismml'})[0], 403)
        self.assertEqual(self.call('/api/engine-releases', {'engine': 'kobold'}, csrf=self.csrf)[0], 400)
        code, data, _ = self.call('/api/engine-releases', {'engine': 'prismml'}, csrf=self.csrf)
        self.assertEqual(code, 202)
        deadline = time.monotonic() + 10
        while True:
            state = self.call('/api/engine-releases?id=' + data['id'])[1]
            if state['state'] != 'running' or time.monotonic() > deadline:
                break
            time.sleep(.1)
        self.assertEqual(state['state'], 'done')
        self.assertEqual(state['releases'][0], {'tag': 'b10760', 'published': '2026-10-06T00:00:00Z', 'prerelease': False, 'size': 150})
        code, data, _ = self.call('/api/engine-releases', {'engine': 'ollama'}, csrf=self.csrf)
        while True:
            state = self.call('/api/engine-releases?id=' + data['id'])[1]
            if state['state'] != 'running':
                break
            time.sleep(.1)
        self.assertEqual(state['state'], 'failed')
        self.assertIn('rate limit', state['error'])
        self.assertEqual(self.call('/api/engine-releases?id=unknown')[0], 400)
        code, op, _ = self.call('/api/actions', {'action': 'engine-install', 'engine': 'ollama', 'version': ''}, csrf=self.csrf)
        self.assertEqual(code, 202)
        self.wait_operation(op['id'])

    def test_09a_brand_assets_and_unified_events_are_available(self):
        for path in ('/favicon.svg','/brand-light.svg','/brand-dark.svg'):
            with urllib.request.urlopen(self.fixture.url+path) as response:
                self.assertIn('<svg',response.read().decode())
                self.assertIn('image/svg+xml',response.headers['Content-Type'])
        with urllib.request.urlopen(self.fixture.url+'/inter-400.woff') as response:
            self.assertEqual(response.read(4),b'wOFF')
            self.assertEqual(response.headers['Content-Type'],'font/woff')
        code,data,_=self.call('/api/logs');self.assertEqual(code,200)
        self.assertEqual(len(data['logs']),1)
        self.assertIn('fixture error log',data['logs'][0]['text'])


class GatewayTests(unittest.TestCase):
    call=classmethod(HttpTests.call.__func__)

    @classmethod
    def setUpClass(cls):
        cls.fixture=PanelFixture(gateway=True)
        cls.cookies=http.cookiejar.CookieJar();cls.client=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cls.cookies))
        cls.csrf=cls.call('/api/login',{'password':PASSWORD})[1]['csrf']

    @classmethod
    def tearDownClass(cls):
        cls.fixture.close()

    def setUp(self):
        p=self.fixture.root/'gateway.json';c=json.loads(p.read_text());c.update(backend='bonsai',require_key=True,key_hash=hashlib.sha256(API_KEY.encode()).hexdigest());p.write_text(json.dumps(c))

    def model_request(self, path='/v1/models', body=None, key=API_KEY, port=None, extra=None):
        connection=http.client.HTTPConnection('127.0.0.1',port or self.fixture.api_port,timeout=5)
        headers={} if key is None else {'Authorization':'Bearer '+key}
        if extra:headers.update(extra)
        if body is not None:headers['Content-Type']='application/json'
        try:
            connection.request('GET' if body is None else 'POST',path,body=None if body is None else json.dumps(body).encode(),headers=headers)
            r=connection.getresponse();return r.status,json.loads(r.read()),r.headers
        finally:connection.close()

    def test_01_auth_and_models(self):
        code,data,_=self.model_request(key=None);self.assertEqual(code,401);self.assertIn('message',data['error'])
        self.assertEqual(self.model_request(key='wrong')[0],401)
        self.assertEqual(self.model_request()[1]['data'][0]['id'],'bonsai-2-27b')

    def test_02_gateway_credentials_are_never_exposed_in_view(self):
        code,data,_=self.call('/api/gateway');self.assertEqual(code,200);self.assertTrue(data['has_key'])
        self.assertNotIn('key_hash',data);self.assertNotIn(API_KEY,json.dumps(data))
        self.assertEqual(self.call('/gateway.json')[0],404)

    def test_03_request_and_tools_are_forwarded_without_gateway_key(self):
        body={'model':'bonsai-2-27b','messages':[{'role':'user','content':'test'}],'stream':False,'tools':[{'type':'function','function':{'name':'fixture'}}]}
        code,data,_=self.model_request('/v1/chat/completions',body);self.assertEqual(code,200);self.assertEqual(data['forwarded'],body)
        self.assertIsNone(self.fixture.upstream.seen_auth)

    def test_04_stream_is_incremental_and_panel_stays_available(self):
        connection=http.client.HTTPConnection('127.0.0.1',self.fixture.api_port,timeout=5)
        try:
            started=time.monotonic();connection.request('POST','/v1/chat/completions',json.dumps({'model':'bonsai-2-27b','stream':True}),{'Content-Type':'application/json','Authorization':'Bearer '+API_KEY})
            response=connection.getresponse();self.assertEqual(response.status,200);self.assertIn('first',response.readline().decode());self.assertLess(time.monotonic()-started,.8)
            before=time.monotonic();self.assertEqual(self.call('/api/session')[0],200);self.assertLess(time.monotonic()-before,.4)
            self.assertIn('[DONE]',response.read().decode());self.assertGreater(time.monotonic()-started,1)
        finally:connection.close()

    def test_05_key_rotation_revokes_old_key(self):
        code,data,_=self.call('/api/gateway',{'action':'generate'},csrf=self.csrf);self.assertEqual(code,200);self.assertTrue(data['key'].startswith('obai-'))
        self.assertNotIn(data['key'],(self.fixture.root/'gateway.json').read_text())
        self.assertEqual(self.model_request(key=API_KEY)[0],401);self.assertEqual(self.model_request(key=data['key'])[0],200)

    def test_06_optional_key_and_backend_selection(self):
        c=self.call('/api/gateway')[1]
        self.assertEqual(self.call('/api/gateway',{'action':'save','base_url':c['base_url'],'backend':'bonsai','require_key':False},csrf=self.csrf)[0],200)
        self.assertEqual(self.model_request(key=None)[0],200)
        self.assertEqual(self.model_request(key='local')[0],200)
        self.assertEqual(self.call('/api/gateway',{'action':'save','base_url':c['base_url'],'backend':'ollama','require_key':False},csrf=self.csrf)[0],200)
        self.assertEqual(self.model_request(key=None)[0],503)

    def test_07_control_routes_and_queries_are_not_proxied(self):
        self.assertEqual(self.model_request('/api/gateway')[0],404)
        self.assertEqual(self.model_request('/v1/models?url=http://other')[0],404)
        self.assertEqual(self.call('/api/gateway',{'action':'generate'})[0],403)

    def test_08_oversized_body_rejected_before_upload(self):
        connection=http.client.HTTPConnection('127.0.0.1',self.fixture.api_port,timeout=5)
        try:
            connection.putrequest('POST','/v1/chat/completions');connection.putheader('Authorization','Bearer '+API_KEY);connection.putheader('Content-Type','application/json');connection.putheader('Content-Length',str(9*1024*1024));connection.endheaders()
            r=connection.getresponse();self.assertEqual(r.status,413);r.read()
        finally:connection.close()

    def test_09_catalog_runs_in_background(self):
        code,data,_=self.call('/api/catalog',{'mode':'search','value':'uncensored'},csrf=self.csrf);self.assertEqual(code,202)
        self.assertEqual(self.call('/api/session')[0],200)
        for _ in range(30):
            result=self.call('/api/catalog?id='+data['id'])[1]
            if result['state']=='done':break
            time.sleep(.05)
        self.assertEqual(result['state'],'done');self.assertEqual(result['data']['models'][0]['id'],'fixture/model')

    def test_10_same_panel_port_routes_v1_with_independent_api_auth(self):
        port=self.fixture.port
        self.assertEqual(self.model_request(port=port,key=None)[0],401)
        self.assertEqual(self.model_request(port=port)[1]['data'][0]['id'],'bonsai-2-27b')
        self.assertEqual(self.model_request(port=port,key='wrong')[0],401)
        self.assertEqual(self.call('/api/gateway',auth=False)[0],401)
        self.assertEqual(self.call('/api/session')[0],200)

    def test_11_nas_can_forward_both_panel_and_api_to_one_upstream(self):
        body={'model':'bonsai-2-27b','messages':[{'role':'user','content':'same NAS upstream'}]}
        forwarded={'X-Forwarded-Host':'llm.fnx.red','X-Forwarded-Proto':'https'}
        code,data,_=self.model_request('/v1/chat/completions',body,port=self.fixture.port,extra=forwarded)
        self.assertEqual(code,200);self.assertEqual(data['forwarded'],body)
        self.assertEqual(self.model_request('/v1/models?other=1',port=self.fixture.port)[0],404)
        self.assertEqual(self.call('/')[0],200)

    def test_12_shared_port_streaming_is_incremental(self):
        connection=http.client.HTTPConnection('127.0.0.1',self.fixture.port,timeout=5)
        try:
            started=time.monotonic();connection.request('POST','/v1/chat/completions',json.dumps({'model':'bonsai-2-27b','stream':True}),{'Content-Type':'application/json','Authorization':'Bearer '+API_KEY})
            response=connection.getresponse();self.assertEqual(response.status,200);self.assertIn('first',response.readline().decode());self.assertLess(time.monotonic()-started,.8)
            self.assertEqual(self.call('/api/session')[0],200);self.assertIn('[DONE]',response.read().decode())
        finally:connection.close()

    def test_13_one_listener_port_starts_without_a_secondary_api_port(self):
        fixture=PanelFixture(gateway=True,shared_port=True)
        try:
            request=urllib.request.Request(fixture.url+'/v1/models',headers={'Authorization':'Bearer '+API_KEY})
            with urllib.request.urlopen(request,timeout=5) as response:
                self.assertEqual(json.loads(response.read())['data'][0]['id'],'bonsai-2-27b')
            with urllib.request.urlopen(fixture.url,timeout=5) as response:self.assertEqual(response.status,200)
        finally:fixture.close()

    def test_14_model_pin_and_single_output_sequence(self):
        self.assertEqual(self.model_request('/v1/chat/completions',{'model':'another-model'})[0],409)
        self.assertEqual(self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b','n':2})[0],400)
        self.assertEqual(self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b','n':[]})[0],400)
        self.assertEqual(self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b','options':{'num_ctx':999999}})[0],400)

    def test_15_request_journal_stores_usage_without_content_or_keys(self):
        secret='private fixture text must never enter metadata log'
        self.assertEqual(self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b','messages':[{'role':'user','content':secret}]})[0],200)
        data=self.call('/api/activity')[1];r=data['requests'][0]
        self.assertEqual((r['input_tokens'],r['output_tokens'],r['total_tokens']),(11,7,18))
        self.assertEqual(r['context_used'],18);self.assertEqual(r['context_remaining'],196590)
        log=(self.fixture.root/'logs/requests/requests.jsonl').read_text()
        self.assertNotIn(secret,log);self.assertNotIn(API_KEY,log);self.assertNotIn('messages',log);self.assertNotIn('choices',log)
        self.assertEqual(self.call('/api/activity',auth=False)[0],401)

    def test_16_live_usage_and_parallel_generation_rejection(self):
        connection=http.client.HTTPConnection('127.0.0.1',self.fixture.port,timeout=5)
        try:
            connection.request('POST','/v1/chat/completions',json.dumps({'model':'bonsai-2-27b','stream':True}),{'Content-Type':'application/json','Authorization':'Bearer '+API_KEY})
            response=connection.getresponse();self.assertEqual(response.status,200);self.assertIn('first',response.readline().decode())
            active=self.call('/api/activity')[1]['active'];self.assertEqual(active['input_tokens'],11);self.assertEqual(active['output_tokens'],3)
            self.assertEqual(self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b'})[0],409)
            self.assertEqual(self.call('/api/actions',{'action':'start','backend':'bonsai'},csrf=self.csrf)[0],400)
            self.assertIn('[DONE]',response.read().decode())
            completed=self.call('/api/activity')[1];self.assertIsNone(completed['active']);self.assertEqual(completed['requests'][0]['total_tokens'],18)
        finally:connection.close()

    def test_17_missing_usage_stays_unknown(self):
        self.assertEqual(self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b','fixture_usage':False})[0],200)
        r=self.call('/api/activity')[1]['requests'][0]
        self.assertIsNone(r['input_tokens']);self.assertIsNone(r['output_tokens']);self.assertIsNone(r['total_tokens']);self.assertIsNone(r['context_remaining'])

    def test_18_explicit_session_identifier_is_hashed_and_aggregated(self):
        header={'X-Omnibox-Session':'fixture-session-A'}
        for _ in range(2):self.assertEqual(self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b'},extra=header)[0],200)
        data=self.call('/api/activity')[1];a,b=data['requests'][:2];self.assertEqual(a['session_id'],b['session_id'])
        s=next(s for s in data['sessions'] if s['id']==a['session_id']);self.assertEqual(s['requests'],2);self.assertEqual(s['total_tokens'],36)
        self.assertNotIn('fixture-session-A',json.dumps(data))

    def test_19_stop_cancels_generation_and_leaves_panel_available(self):
        connection=http.client.HTTPConnection('127.0.0.1',self.fixture.port,timeout=5)
        try:
            connection.request('POST','/v1/chat/completions',json.dumps({'model':'bonsai-2-27b','stream':True}),{'Content-Type':'application/json','Authorization':'Bearer '+API_KEY})
            response=connection.getresponse();self.assertEqual(response.status,200);response.readline()
            self.assertEqual(self.call('/api/actions',{'action':'stop','backend':'bonsai'},csrf=self.csrf)[0],202)
            try:response.read()
            except http.client.IncompleteRead:pass
            for _ in range(40):
                state=self.call('/api/status')[1]
                if not any(o['state']=='running' for o in state['operations']):break
                time.sleep(.05)
            self.assertEqual(self.call('/api/session')[0],200)
            self.assertEqual(self.call('/api/activity')[1]['requests'][0]['state'],'failed')
        finally:
            connection.close()
            p=self.fixture.root/'config.json';c=json.loads(p.read_text());c['service']='Running';p.write_text(json.dumps(c))

    def test_20_telemetry_is_authenticated_and_runs_in_a_background_job(self):
        self.assertEqual(self.call('/api/telemetry',auth=False)[0],401)
        for _ in range(30):
            code,data,_=self.call('/api/telemetry');self.assertEqual(code,200)
            if data.get('sample'):break
            time.sleep(.05)
        self.assertEqual(data['sample']['ram_total_gb'],32);self.assertEqual(data['sample']['cpu_percent'],22)
        self.assertIsNone(data['sample']['cpu_temperature']);self.assertEqual(self.call('/api/session')[0],200)
        self.assertEqual(self.call('/api/telemetry?once=1',auth=False)[0],401)
        code,data,_=self.call('/api/telemetry?once=1');self.assertEqual(code,200);self.assertIn('collecting',data)

    def test_21_ollama_cpu_placement_is_injected_and_client_override_is_rejected(self):
        path=self.fixture.root/'config.json';original=path.read_text();config=json.loads(original)
        config['gateway_type']='ollama';config['gateway_placement']='cpu';path.write_text(json.dumps(config))
        try:
            code,data,_=self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b','messages':[]})
            self.assertEqual(code,200)
            self.assertEqual(data['forwarded']['options'],{'num_ctx':196608,'num_gpu':0})
            self.assertEqual(self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b','options':{'num_gpu':99}})[0],400)
        finally:path.write_text(original)


    def test_22_optional_content_journal_and_viewer(self):
        secret='content journal fixture text'
        content=self.fixture.root/'logs'/'requests'/'content.jsonl'
        self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b','messages':[{'role':'user','content':'metadata only'}]})
        self.assertFalse(content.exists())
        self.assertEqual(self.call('/api/storage',{'action':'save','request_log':'content'},csrf=self.csrf)[0],200)
        self.assertEqual(self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b','messages':[{'role':'user','content':secret}]})[0],200)
        connection=http.client.HTTPConnection('127.0.0.1',self.fixture.api_port,timeout=10)
        try:
            connection.request('POST','/v1/chat/completions',body=json.dumps({'model':'bonsai-2-27b','stream':True,'messages':[{'role':'user','content':'stream question'}]}).encode(),headers={'Authorization':'Bearer '+API_KEY,'Content-Type':'application/json'})
            response=connection.getresponse();self.assertEqual(response.status,200);response.read()
        finally:connection.close()
        text=content.read_text();lines=[json.loads(line) for line in text.splitlines()]
        self.assertEqual(len(lines),2)
        self.assertEqual(lines[0]['request']['messages'][0]['content'],secret)
        self.assertEqual(lines[0]['response']['choices'][0]['message']['content'],'fixture')
        self.assertEqual(lines[1]['request']['messages'][0]['content'],'stream question')
        self.assertEqual(lines[1]['response_text'],'first')
        self.assertNotIn(API_KEY,text);self.assertNotIn('stream_options',text)
        self.assertNotIn(secret,(self.fixture.root/'logs'/'requests'/'requests.jsonl').read_text())
        newest=self.call('/api/activity')[1]['requests'][0]['id']
        code,data,_=self.call('/api/request-content?id='+newest);self.assertEqual(code,200);self.assertEqual(data['response_text'],'first')
        self.assertEqual(self.call('/api/request-content?id='+newest,auth=False)[0],401)
        self.assertEqual(self.call('/api/request-content?id=../../panel.json')[0],400)
        self.assertEqual(self.call('/api/storage')[1]['request_log'],'content')
        self.assertEqual(self.call('/api/storage',{'action':'save','request_log':'metadata'},csrf=self.csrf)[0],200)
        self.assertFalse(content.exists())
        self.model_request('/v1/chat/completions',{'model':'bonsai-2-27b','messages':[{'role':'user','content':'after disable'}]})
        self.assertFalse(content.exists())
        self.assertEqual(self.call('/api/storage',{'action':'save','request_log':'everything'},csrf=self.csrf)[0],400)

class ProxyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture = PanelFixture(proxy=True)

    @classmethod
    def tearDownClass(cls):
        cls.fixture.close()

    def request(self, path, body=None, *, source='127.0.0.1', forwarded=True, origin=None, extra=None, cookie=None):
        headers = {}
        if forwarded:
            headers.update({'X-Forwarded-Proto':'https','X-Forwarded-Host':'ai.example.com','X-Forwarded-For':'198.51.100.10'})
        if body is not None:
            headers['Content-Type'] = 'application/json'
            headers['Origin'] = origin or 'https://ai.example.com'
        if extra:
            headers.update(extra)
        if cookie:
            headers['Cookie'] = cookie
        connection = http.client.HTTPConnection('127.0.0.1', self.fixture.port, timeout=5, source_address=(source, 0))
        try:
            connection.request('GET' if body is None else 'POST', path, body=None if body is None else json.dumps(body).encode(), headers=headers)
            response = connection.getresponse()
            return response.status, json.loads(response.read()), response.headers
        finally:
            connection.close()

    def setUp(self):
        code, data, headers = self.request('/api/login', {'password':PASSWORD})
        self.assertEqual(code, 200, data)
        self.csrf = data['csrf']
        self.cookie_header = headers['Set-Cookie']
        self.cookie = self.cookie_header.split(';')[0]

    def test_01_https_login_and_session(self):
        self.assertIn('; Secure', self.cookie_header)
        self.assertIn('HttpOnly', self.cookie_header)
        self.assertEqual(self.request('/api/status', cookie=self.cookie)[0], 200)

    def test_02_https_origin_and_csrf(self):
        self.assertEqual(self.request('/api/logout', {}, cookie=self.cookie)[0], 403)
        code, _, headers = self.request('/api/logout', {}, cookie=self.cookie, extra={'X-Omnibox-CSRF':self.csrf})
        self.assertEqual(code, 200)
        self.assertIn('Secure', headers['Set-Cookie'])

    def test_03_wrong_origin_is_rejected(self):
        self.assertEqual(self.request('/api/login', {'password':PASSWORD}, origin='https://other.example.com')[0], 403)
        self.assertEqual(self.request('/api/login', {'password':PASSWORD}, origin=self.fixture.url)[0], 403)

    def test_04_wrong_forwarded_host_is_rejected(self):
        self.assertEqual(self.request('/api/login', {'password':PASSWORD}, extra={'X-Forwarded-Host':'other.example.com'})[0], 400)

    def test_05_downgrade_or_missing_proto_is_rejected(self):
        self.assertEqual(self.request('/api/session', cookie=self.cookie, extra={'X-Forwarded-Proto':'http'})[0], 400)
        self.assertEqual(self.request('/api/session', cookie=self.cookie, forwarded=False)[0], 400)

    def test_06_secure_session_cannot_be_reused_directly(self):
        self.assertEqual(self.request('/api/session', source='127.0.0.2', forwarded=False, cookie=self.cookie)[0], 401)

    def test_07_untrusted_forwarding_headers_do_not_enable_https(self):
        self.assertEqual(self.request('/api/login', {'password':PASSWORD}, source='127.0.0.2')[0], 403)
        code, _, headers = self.request('/api/login', {'password':PASSWORD}, source='127.0.0.2', origin=self.fixture.url)
        self.assertEqual(code, 200)
        self.assertNotIn('Secure', headers['Set-Cookie'])

    def test_08_proxy_rate_limit_is_per_client(self):
        for _ in range(6):
            self.assertEqual(self.request('/api/login', {'password':'wrong'}, extra={'X-Forwarded-For':'198.51.100.41'})[0], 401)
        self.assertEqual(self.request('/api/login', {'password':PASSWORD}, extra={'X-Forwarded-For':'198.51.100.41'})[0], 429)
        self.assertEqual(self.request('/api/login', {'password':PASSWORD}, extra={'X-Forwarded-For':'198.51.100.42'})[0], 200)

@unittest.skipIf(os.name=='nt','The gh test executable is a Linux fixture; native Windows service updates are tested on Windows separately.')
class UpdateTests(unittest.TestCase):
    call=classmethod(HttpTests.call.__func__)
    @classmethod
    def setUpClass(cls):
        cls.fixture=PanelFixture(updates=True)
        cls.cookies=http.cookiejar.CookieJar();cls.client=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cls.cookies))
        cls.csrf=cls.call('/api/login',{'password':PASSWORD})[1]['csrf']
    @classmethod
    def tearDownClass(cls): cls.fixture.close()
    def wait_update(self):
        deadline=time.monotonic()+8
        while time.monotonic()<deadline:
            code,data,_=self.call('/api/updates');self.assertEqual(code,200)
            if data['phase'] not in ('checking','login'): return data
            time.sleep(.1)
        self.fail('Update did not finish')
    def test_01_auth_csrf_and_origin(self):
        self.assertEqual(self.call('/api/updates',auth=False)[0],401)
        self.assertEqual(self.call('/api/updates',{'action':'check'},auth=False)[0],401)
        self.assertEqual(self.call('/api/updates',{'action':'check'})[0],403)
        self.assertEqual(self.call('/api/updates',{'action':'check'},csrf=self.csrf,origin='http://evil.example')[0],403)
    def test_02_no_arbitrary_commands_download_source_or_path(self):
        for body in ({'action':'exec'},{'action':'check','url':'https://evil'},{'action':'apply','root':'/tmp/evil'},{'action':'token','token':'short'},{'action':['check']}):
            self.assertEqual(self.call('/api/updates',body,csrf=self.csrf)[0],400)
    def test_03_check_is_async_and_duplicate_is_rejected(self):
        code,data,_=self.call('/api/updates',{'action':'check'},csrf=self.csrf)
        self.assertEqual(code,202);self.assertEqual(data['phase'],'checking')
        self.assertEqual(self.call('/api/updates',{'action':'check'},csrf=self.csrf)[0],400)
        start=time.monotonic();self.assertEqual(self.call('/api/session')[0],200);self.assertLess(time.monotonic()-start,.3)
        data=self.wait_update();self.assertEqual(data['phase'],'ready',data);self.assertEqual(data['latest'],'9.9.9')
        self.assertTrue((self.fixture.root/'updates/download/installer.ps1').exists())
        self.assertFalse((self.fixture.root/'updates/result.json').exists())
        for key in ('token','file','sha256','bytes'):self.assertNotIn(key,data)
    def test_04_token_stays_one_use_and_private_files_are_not_http_assets(self):
        secret='fixture-only-secret-not-real'
        code,data,_=self.call('/api/updates',{'action':'token','token':secret},csrf=self.csrf)
        self.assertEqual(code,202);self.assertNotIn(secret,json.dumps(data))
        data=self.wait_update();self.assertEqual(data['phase'],'ready',data)
        self.assertNotIn(secret,json.dumps(data));self.assertFalse(data['authenticated'])
        self.assertFalse((self.fixture.root/'updates/auth/github.dpapi').exists())
        self.assertEqual(self.call('/updates/download/installer.ps1')[0],404)
        self.assertEqual(self.call('/updates/auth/github.dpapi')[0],404)
    def test_05_generation_control_guard_rejects_install_during_model_operation(self):
        code,op,_=self.call('/api/actions',{'action':'download','backend':'bonsai','url':'fixture'},csrf=self.csrf)
        self.assertEqual(code,202)
        code,data,_=self.call('/api/updates',{'action':'apply'},csrf=self.csrf)
        self.assertEqual(code,400);self.assertIn('Дождитесь',data['error'])
        HttpTests.wait_operation(self,op['id'])
    def test_06_corrupt_package_and_cancel_are_safe(self):
        path=self.fixture.root/'contents.json';old=path.read_text();package=json.loads(old)
        package['content']=base64.b64encode(b'#requires -Version 7.4\n'+b'x'*150).decode();path.write_text(json.dumps(package))
        try:
            self.assertEqual(self.call('/api/updates',{'action':'check'},csrf=self.csrf)[0],202)
            self.assertEqual(self.wait_update()['phase'],'failed')
            self.assertEqual(self.call('/api/updates',{'action':'apply'},csrf=self.csrf)[0],400)
        finally:path.write_text(old)
        self.assertEqual(self.call('/api/updates',{'action':'check'},csrf=self.csrf)[0],202)
        self.assertEqual(self.call('/api/updates',{'action':'cancel'},csrf=self.csrf)[0],202)
        self.assertEqual(self.wait_update()['phase'],'failed')
    def test_07_forget_and_status_survive_worker_result(self):
        self.assertEqual(self.call('/api/updates',{'action':'forget'},csrf=self.csrf)[0],202)
        result=self.fixture.root/'updates/result.json'
        result.write_text(json.dumps(dict(phase='installing',message='Fixture installation',version='9.9.9',service='fixture')))
        try:
            self.assertEqual(self.call('/api/updates')[1]['phase'],'installing')
            self.assertEqual(self.call('/api/actions',{'action':'start','backend':'bonsai'},csrf=self.csrf)[0],400)
            self.assertEqual(self.call('/api/updates',{'action':'check'},csrf=self.csrf)[0],400)
            result.write_text(json.dumps(dict(phase='failed',message='Fixture recovery',version='9.9.9')))
            # A newly started manager (idle) exposes persisted results, rather than dropping them.
        finally:result.unlink()

if __name__ == '__main__':
    unittest.main(verbosity=2)
