import json, os, pathlib, subprocess, sys, tempfile, threading, unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
ROOT=pathlib.Path(os.environ['TIKU_PROJECT_ROOT']) if 'TIKU_PROJECT_ROOT' in os.environ else pathlib.Path(__file__).resolve().parents[2]
SCRIPT=ROOT/'scripts/ci/http-smoke.py'
class SmokeContract(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory(prefix='tiku-http-unit-')
  self.dist=pathlib.Path(self.temp.name)
  (self.dist/'js').mkdir();(self.dist/'css').mkdir()
  self.html=b'<!doctype html><div id="app"></div><script src="/js/app.fixture.js"></script><link rel="stylesheet" href="/css/app.fixture.css">'
  (self.dist/'index.html').write_bytes(self.html)
  (self.dist/'js/app.fixture.js').write_bytes(b'console.log("fixture")')
  (self.dist/'css/app.fixture.css').write_bytes(b'body{margin:0}')
  self.responses={
   '/healthz':(200,'application/json',b'{"status":"ok"}'),
   '/readyz':(200,'application/json',b'{"status":"ready"}'),
   '/adapter-service/__ci_unknown__':(404,'application/json',b'{"code":"NOT_FOUND"}'),
   '/adapter-service/user/info':(401,'application/json',b'{"message":"not authorized"}'),
   '/adapter-service/system-settings':(401,'application/json',b'{"message":"not authorized"}'),
   '/adapter-service/access-tokens':(401,'application/json',b'{"message":"not authorized"}'),
  }
  owner=self
  class Handler(BaseHTTPRequestHandler):
   def do_GET(self):
    item=owner.responses.get(self.path)
    if item is None:
     if self.path.startswith(('/js/','/css/')):
      file=owner.dist/self.path.lstrip('/')
      item=(200,'application/javascript' if self.path.endswith('.js') else 'text/css',file.read_bytes())
     else:item=(200,'text/html',owner.html)
    status,mime,body=item
    self.send_response(status);self.send_header('Content-Type',mime);self.send_header('Cache-Control','no-store')
    if status==302:self.send_header('Location','/redirected')
    self.end_headers();self.wfile.write(body)
   def log_message(self,*args):pass
  self.server=ThreadingHTTPServer(('127.0.0.1',0),Handler)
  self.thread=threading.Thread(target=self.server.serve_forever,daemon=True);self.thread.start()
  self.url='http://127.0.0.1:'+str(self.server.server_port)
 def tearDown(self):
  self.server.shutdown();self.server.server_close();self.thread.join();self.temp.cleanup()
 def run_gate(self,url=None):
  return subprocess.run([sys.executable,str(SCRIPT),'--check-url',url or self.url,'--dist',str(self.dist)],capture_output=True,text=True,timeout=20)
 def test_real_http_and_matching_assets_pass(self):
  p=self.run_gate();self.assertEqual(p.returncode,0,p.stderr+p.stdout)
  self.assertTrue(json.loads(p.stdout)['passed'])
 def test_api_spa_fallback_is_rejected(self):
  self.responses['/adapter-service/__ci_unknown__']=(200,'text/html',self.html)
  p=self.run_gate();self.assertEqual(p.returncode,1,p.stderr+p.stdout)
  self.assertIn('api-404',p.stdout)
 def test_missing_authentication_is_rejected(self):
  self.responses['/adapter-service/user/info']=(200,'application/json',b'{}')
  p=self.run_gate();self.assertEqual(p.returncode,1,p.stderr+p.stdout)
  self.assertIn('auth-user-info',p.stdout)
 def test_stale_embedded_asset_is_rejected(self):
  self.responses['/js/app.fixture.js']=(200,'application/javascript',b'wrong old bundle')
  p=self.run_gate();self.assertEqual(p.returncode,1,p.stderr+p.stdout)
  self.assertIn('asset-integrity',p.stdout)
 def test_false_readiness_is_rejected(self):
  self.responses['/readyz']=(503,'application/json',b'{"status":"not ready"}')
  p=self.run_gate();self.assertEqual(p.returncode,1,p.stderr+p.stdout)
  self.assertIn('readiness',p.stdout)
 def test_missing_entry_script_is_rejected_even_when_js_files_exist(self):
  self.html=b'<!doctype html><div id="app"></div>'
  (self.dist/'index.html').write_bytes(self.html)
  p=self.run_gate();self.assertEqual(p.returncode,1,p.stderr+p.stdout)
  self.assertIn('entry-script',p.stdout)
 def test_missing_index_asset_reference_is_rejected(self):
  self.html=b'<div id="app"></div><script src="/js/missing.js"></script>'
  (self.dist/'index.html').write_bytes(self.html)
  self.responses['/js/missing.js']=(200,'text/html',self.html)
  p=self.run_gate();self.assertEqual(p.returncode,1,p.stderr+p.stdout)
  self.assertIn('entry-reference',p.stdout)
 def test_health_redirect_does_not_escape_the_direct_probe_contract(self):
  self.responses['/healthz']=(302,'application/json',b'')
  self.responses['/redirected']=(200,'application/json',b'{"status":"ok"}')
  p=self.run_gate();self.assertEqual(p.returncode,1,p.stderr+p.stdout)
  self.assertIn('liveness',p.stdout)
 def test_non_loopback_targets_are_rejected(self):
  p=self.run_gate('http://example.com')
  self.assertEqual(p.returncode,2,p.stderr+p.stdout);self.assertIn('loopback',p.stderr)
if __name__=='__main__':unittest.main()
