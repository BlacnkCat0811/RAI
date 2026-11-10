#!/usr/bin/env python3
"""Read-only release preparation checks. Does not start or change services."""
import argparse,hashlib,json,os,pathlib,re,stat,subprocess,sys
parser=argparse.ArgumentParser();parser.add_argument('--synthetic',action='store_true');args=parser.parse_args()
root=pathlib.Path(__file__).resolve().parents[1]
def run(cmd):return subprocess.check_output(cmd,stderr=subprocess.DEVNULL)
try:
 project=os.environ['RAI_PROJECT'];directory=pathlib.Path(os.environ['RAI_SECRETS_DIR'])
 assert re.fullmatch(r'rai-[a-z0-9][a-z0-9_-]{2,60}',project),'distinct rai-* project required'
 assert directory.is_absolute() and not directory.is_symlink() and directory.stat().st_mode&0o077==0,'secret directory must be private'
 config=json.loads(run(['docker','compose','-f',str(root/'deploy/release/compose.yaml'),'config','--format','json']))
 assert not config['services']['postgres'].get('ports'),'database must not publish a port'
 assert config['networks']['database']['internal']
 for name,secret in config['secrets'].items():
  file=pathlib.Path(secret['file']);mode=file.stat().st_mode
  assert file.is_file() and not file.is_symlink(),f'invalid secret file: {name}'
  assert (name in ['tls_cert','tls_ca'] or mode&0o007==0) and not mode&0o022,f'insecure secret permissions: {name}'
  if name in ['api_dsn','worker_dsn','owner_dsn','monitor_dsn','tls_key'] and sys.platform=='linux':
   assert file.stat().st_uid==65532 or (file.stat().st_gid==65532 and mode&0o040),f'nonroot process cannot read: {name}'
  if name.endswith('_password'):assert file.stat().st_size>=33,f'nonrandom or empty secret: {name}'
 for service in ['api','worker']:
  c=config['services'][service]
  assert c['read_only'] and c['environment']['RAI_ENFORCE_RUNTIME_ROLE']=='1'
  assert c['image']==config['services']['api']['image']
  if not args.synthetic:assert re.search(r'@sha256:[0-9a-f]{64}$',c['image']),'reviewed image digest required'
 host=os.environ['RAI_TLS_SERVER_NAME']
 run(['openssl','verify','-CAfile',str(directory/'tls_ca'),'-untrusted',str(directory/'tls_cert'),'-verify_hostname',host,str(directory/'tls_cert')])
 run(['openssl','x509','-in',str(directory/'tls_cert'),'-checkend','604800','-noout'])
 certkey=run(['openssl','x509','-in',str(directory/'tls_cert'),'-pubkey','-noout'])
 privatekey=run(['openssl','pkey','-in',str(directory/'tls_key'),'-pubout'])
 assert certkey==privatekey,'certificate and key mismatch'
 # Validate identity separation without printing credential-bearing URLs.
 import urllib.parse
 for name,user in [('api','rai_api_login'),('worker','rai_worker_login'),('owner','rai_owner'),('monitor','rai_monitor_login')]:
  u=urllib.parse.urlsplit((directory/(name+'_dsn')).read_text().strip())
  assert u.hostname=='postgres' and u.username==user and u.path=='/rai_v2' and len(u.password or '')>=32,'invalid role-specific DSN'
 print(json.dumps({'preflight':'PASS','synthetic':args.synthetic,'tls_chain_hostname_expiry':'PASS','secrets':'permission/identity checks passed','production_authorization':False}))
except (AssertionError,KeyError,OSError,subprocess.SubprocessError,ValueError) as e:
 # Never include URL parser errors or subprocess output containing secrets.
 detail=str(e) if isinstance(e,AssertionError) else type(e).__name__
 print('P7_PREFLIGHT=FAIL: '+detail,file=sys.stderr);sys.exit(1)
