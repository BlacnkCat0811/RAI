#!/usr/bin/env python3
"""Repository-only secret guard. Emits file paths, never matched values.
This pattern audit is evidence of checks, not a credential leak-proof claim.
"""
import pathlib,re,subprocess,sys
root=pathlib.Path(__file__).resolve().parents[1]
try:
 repository=pathlib.Path(subprocess.check_output(['git','rev-parse','--show-toplevel'],cwd=root,stderr=subprocess.DEVNULL,text=True).strip()).resolve()
except subprocess.CalledProcessError:
 raise SystemExit('Secret audit requires the original Git checkout; remote source snapshots intentionally exclude .git')
if repository!=root:raise SystemExit('Secret audit must run at its own repository root')
names=subprocess.check_output(['git','ls-files','--cached','--others','--exclude-standard','-z'],cwd=root).decode().split('\0')
findings=[]
for name in filter(None,names):
 p=root/name
 if not p.is_file():continue
 if p.name=='.env' or p.suffix in ['.key','.p12','.pfx','.mobileprovision'] or '/secrets/' in '/'+name:findings.append((name,'credential file tracked or unignored'));continue
 data=p.read_bytes()
 if re.search(rb'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----',data):findings.append((name,'private key block'))
 for match in re.finditer(rb'postgres(?:ql)?://[^\s/:]+:([^\s@]+)@',data):
  password=match.group(1)
  if any(c in password for c in [b'$',b'<',b'{',b'%']):continue
  if name in ['deploy/dev/compose.yaml','.env.example','scripts/test-p1-compose.sh'] and password==b'development-only':continue
  # This workflow starts a fresh synthetic PostgreSQL service on the runner;
  # its URI must use exactly that service password, loopback and rai_test DB.
  if name=='.github/workflows/server.yml':
   ci_password=re.search(rb'POSTGRES_PASSWORD:\s*([^\s]+)',data)
   if ci_password and password==ci_password.group(1) and match.group(0).startswith(b'postgres://rai_test:') and data[match.end():].startswith(b'127.0.0.1:5432/rai_test?'):continue
  if name.endswith('_test.go') and password in [b'example',b'example-password',b'pass',b'p',b'password']:continue
  findings.append((name,'literal password-bearing database URI'))
for path,reason in findings:print(path+': '+reason,file=sys.stderr)
if findings:raise SystemExit(1)
print('P7_SECRET_AUDIT=PASS repository paths/private-key blocks/literal DB passwords; runtime synthetic secrets excluded')
