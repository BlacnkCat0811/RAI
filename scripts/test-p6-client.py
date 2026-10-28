#!/usr/bin/env python3
"""Keep a real fnOS API tunnel alive for synthetic Swift + simulator acceptance."""
import json,os,pathlib,shlex,socket,subprocess,sys,time
root=pathlib.Path(__file__).resolve().parents[1]
meta=next(json.loads(x.split('=',1)[1]) for x in pathlib.Path(sys.argv[1]).read_text().splitlines() if x.startswith('P6_CLIENT_READY='))
private=pathlib.Path('/tmp/rai-p6-native-acceptance')
private.mkdir(mode=0o700,exist_ok=False)
ssh=['ssh','-T','-o','BatchMode=yes','rai-fnos']
tunnel=None
try:
 config=json.loads(subprocess.check_output(ssh+['cat '+shlex.quote(meta['configuration'])]))
 with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
 tunnel=subprocess.Popen(['ssh','-N','-o','BatchMode=yes','-o','ExitOnForwardFailure=yes','-L',f'127.0.0.1:{port}:127.0.0.1:{meta["port"]}','rai-fnos'])
 for _ in range(100):
  try:
   with socket.create_connection(('127.0.0.1',port),timeout=.2):break
  except OSError:time.sleep(.1)
 else:raise RuntimeError('tunnel unavailable')
 config['baseURL']=f'http://127.0.0.1:{port}'
 for filename,value in [('config.json',config),('config-app.json',{'configuration':{'baseURL':config['baseURL'],'collectorEpoch':config['collectorEpoch']},'token':config['token']})]:
  p=private/filename;p.write_text(json.dumps(value));p.chmod(0o600)
 probe=str(root/'ios/RAI/.build/debug/ProductProbe')
 for mode in ['seed','verify']:
  subprocess.run([probe,mode,str(private/'config.json'),str(private/'state')],check=True,timeout=480)
 print('P6_SWIFT_FNOS_READY=PASS simulator may connect using private config path',flush=True)
 for _ in range(7200):
  if (private/'done').exists():break
  if tunnel.poll() is not None:raise RuntimeError('tunnel exited')
  time.sleep(1)
 else:raise RuntimeError('simulator acceptance timed out')
finally:
 if tunnel is not None:tunnel.terminate();tunnel.wait(timeout=10)
 subprocess.run(ssh+['touch '+shlex.quote(meta['done'])],check=True)
 # Private credentials never enter repository evidence. Caller copies redacted
 # reports before signalling done; remove the private workspace and test token.
 import shutil
 shutil.rmtree(private)
