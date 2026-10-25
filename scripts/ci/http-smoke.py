#!/usr/bin/env python3
import argparse, hashlib, json, os, pathlib, socket, subprocess, sys, tempfile, time
import urllib.error, urllib.parse, urllib.request
from html.parser import HTMLParser
class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,req,fp,code,msg,headers,newurl):return None
class EntryReferences(HTMLParser):
 def __init__(self):super().__init__();self.references=[];self.script_count=0
 def handle_starttag(self,tag,attrs):
  attributes=dict(attrs)
  if tag=="script" and attributes.get("src"):
   self.script_count+=1;self.references.append(attributes["src"])
  if tag=="link" and attributes.get("rel")=="stylesheet" and attributes.get("href"):self.references.append(attributes["href"])
def check_url(value):
 parsed=urllib.parse.urlsplit(value)
 if parsed.scheme!='http' or parsed.hostname not in ['127.0.0.1','::1'] or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ['','/']:
  raise ValueError('Smoke target must be a plain HTTP loopback origin')
 return value.rstrip('/')
def verify(base,dist):
 checks=[]
 opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
 def fetch(path):
  try:
   with opener.open(base+path,timeout=5) as response:return response.status,response.headers,response.read()
  except urllib.error.HTTPError as error:return error.code,error.headers,error.read()
 def expect(name,path,status,mime,validator=lambda body:True,no_store=False):
  try:
   code,headers,body=fetch(path)
   passed=code==status and mime in headers.get('Content-Type','') and validator(body)
   if no_store:passed=passed and headers.get('Cache-Control')=='no-store'
   checks.append({'name':name,'path':path,'status':code,'content_type':headers.get('Content-Type',''),'bytes':len(body),'sha256':hashlib.sha256(body).hexdigest(),'passed':bool(passed)})
  except Exception as error:checks.append({'name':name,'path':path,'passed':False,'error_type':type(error).__name__})
 expect('liveness','/healthz',200,'application/json',lambda b:json.loads(b)=={'status':'ok'},True)
 expect('readiness','/readyz',200,'application/json',lambda b:json.loads(b)=={'status':'ready'},True)
 expect('api-404','/adapter-service/__ci_unknown__',404,'application/json',lambda b:json.loads(b).get('code')=='NOT_FOUND')
 for path in ['user/info','system-settings','access-tokens']:
  expect('auth-'+path.replace('/','-'),'/adapter-service/'+path,401,'application/json')
 index=(dist/'index.html').read_bytes()
 entries=EntryReferences();entries.feed(index.decode('utf8'))
 checks.append({'name':'entry-script','passed':entries.script_count>0})
 for ref in entries.references:
  parsed=urllib.parse.urlsplit(ref)
  target=(dist/parsed.path.lstrip('/')).resolve()
  valid=not parsed.scheme and not parsed.netloc and target.is_relative_to(dist.resolve()) and target.is_file() and target.suffix in ['.js','.css']
  checks.append({'name':'entry-reference','path':parsed.path,'passed':bool(valid)})
 for path in ['/login','/adapter/component','/adapter/profile','/adapter/recharge/result']:
  expect('spa-refresh',path,200,'text/html',lambda body:body==index)
 assets=sorted(p for p in dist.rglob('*') if p.is_file() and p.suffix in ['.js','.css'])
 if not assets:raise ValueError('Missing built JS/CSS assets')
 for p in assets:
  if not p.resolve().is_relative_to(dist.resolve()):raise ValueError('Built asset escaped dist')
  expected=hashlib.sha256(p.read_bytes()).hexdigest()
  path='/'+urllib.parse.quote(p.relative_to(dist).as_posix())
  expect('asset-integrity',path,200,'javascript' if p.suffix=='.js' else 'text/css',lambda body,want=expected:hashlib.sha256(body).hexdigest()==want)
 return {'passed':all(c['passed'] for c in checks),'checks':checks,'scope':'Isolated HTTP/binary integration, not browser rendering or paid-provider acceptance'}
def main():
 parser=argparse.ArgumentParser()
 source=parser.add_mutually_exclusive_group(required=True)
 source.add_argument('--check-url');source.add_argument('--binary')
 parser.add_argument('--dist',required=True,type=pathlib.Path)
 parser.add_argument('--log-dir',type=pathlib.Path)
 args=parser.parse_args()
 dist=args.dist.resolve()
 try:
  if args.check_url:
   result=verify(check_url(args.check_url),dist)
  else:
   binary=pathlib.Path(args.binary).resolve()
   if not binary.is_file():raise ValueError('Missing service binary')
   probe=socket.socket()
   try:probe.bind(('127.0.0.1',8060))
   finally:probe.close()
   log_dir=(args.log_dir or pathlib.Path(tempfile.mkdtemp(prefix='tiku-ci-logs-'))).resolve()
   log_dir.mkdir(parents=True,exist_ok=True)
   with tempfile.TemporaryDirectory(prefix='tiku-ci-runtime-') as temp:
    cwd=pathlib.Path(temp);(cwd/'data').mkdir()
    (cwd/'config.yaml').write_text('jwtSecret: ""\nlimit:\n  enable: true\n  duration: 60\n  requests: 120\nmysql: ""\ndatabase:\n  path: "data/tiku.db"\napi: []\nelasticsearch:\n  url: ""\nrecordEmptyAnswer: false\n')
    env={'PATH':os.environ.get('PATH','/usr/bin:/bin'),'GIN_MODE':'release','JWT_SECRET':'CI-ONLY-SYNTHETIC-JWT-SECRET-DO-NOT-DEPLOY','TIKU_ADMIN_PASSWORD':'CI-Only-Synthetic-Password-261013','TRUSTED_PROXIES':'127.0.0.1,::1'}
    with (log_dir/'service.stdout.txt').open('wb') as out,(log_dir/'service.stderr.txt').open('wb') as err:
     process=subprocess.Popen([str(binary),'-log-dir',str(log_dir/'files')],cwd=cwd,env=env,stdout=out,stderr=err)
     try:
      deadline=time.monotonic()+30
      while True:
       if process.poll() is not None:raise RuntimeError('Isolated service exited before readiness: '+str(process.returncode))
       try:
        request=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect()).open('http://127.0.0.1:8060/readyz',timeout=1)
        with request:ready=request.status==200
        if ready:break
       except (OSError,urllib.error.URLError):pass
       if time.monotonic()>deadline:raise RuntimeError('Isolated service readiness timeout')
       time.sleep(.1)
      result=verify('http://127.0.0.1:8060',dist)
     finally:
      if process.poll() is None:
       process.terminate()
       try:process.wait(timeout=12)
       except subprocess.TimeoutExpired:process.kill();process.wait()
     result['service_exit_status']=process.returncode
     result['passed']=result['passed'] and process.returncode==0
   result['synthetic_environment_only']=True
  print(json.dumps(result,ensure_ascii=False,indent=2))
  return 0 if result['passed'] else 1
 except Exception as error:
  print('HTTP smoke error: '+str(error),file=sys.stderr)
  return 2
if __name__=='__main__':sys.exit(main())
