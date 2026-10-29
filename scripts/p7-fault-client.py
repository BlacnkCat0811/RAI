#!/usr/bin/env python3
"""Original-identity retry harness for destructive, isolated P7 tests only."""
import gzip,hashlib,json,os,pathlib,sys,time,urllib.request
from importlib.machinery import SourceFileLoader
w=SourceFileLoader('p7_workload',str(pathlib.Path(__file__).with_name('p7-workload.py'))).load_module()
mode=sys.argv[1];p=w.state
if mode=='prepare':
 prior=p/'fault-last-envelope.json'
 env=json.loads((prior if prior.exists() else p/'sample-envelope.json').read_text())
 count=int(os.environ.get('RAI_P7_FAULT_EVENTS','1'));assert count in [1,1000]
 events=[{'op':'upsert','kind':'sample','type':'HKQuantityTypeIdentifierHeartRate','uuid':w.uid(),'startUs':1793059200000000+i*1000000,'endUs':1793059200000000+i*1000000,'quantity':{'value':123,'unit':'count/min'},'source':{'bundleID':'synthetic.p7.fault'}} for i in range(count)]
 decoded=b''.join((json.dumps(event,separators=(',',':'))+'\n').encode() for event in events);wire=gzip.compress(decoded,mtime=0)
 env.update(batchID=w.uid(),clientSeq=env['clientSeq']+1,previousBatchID=env['batchID'],pageID=w.uid(),eventCount=count,wireBytes=len(wire),decodedBytes=len(decoded),payloadSHA256=hashlib.sha256(wire).hexdigest())
 w.call('POST','/v2/sync/batches',env)
 (p/'fault-current.json').write_text(json.dumps(env));(p/'fault-current.gz').write_bytes(wire)
elif mode in ['send','retry']:
 env=json.loads((p/'fault-current.json').read_text());wire=(p/'fault-current.gz').read_bytes()
 def submit():return w.call('PUT','/v2/sync/batches/'+env['batchID']+'/body',data=wire,extra={'Content-Type':'application/x-rai-health-delta+ndjson','Content-Encoding':'gzip'})
 if mode=='send':
  try:submit();(p/'fault-observed-status').write_text('200')
  except Exception as e:(p/'fault-observed-status').write_text('503' if 'status=503' in str(e) else 'unknown')
 else:
  first=submit();assert first['ingestStatus']=='committed' and first['counts']['inserted']==env['eventCount']
  assert submit()==first
  (p/'fault-last-envelope.json').write_text(json.dumps(env))
  print('P7_FAULT_RETRY=PASS original identity committed once with stable receipt')
elif mode=='worker':
 h=w.call('GET','/v2/handshake');stream=w.uid();fp='sha256:'+hashlib.sha256(stream.encode()).hexdigest();at=int(time.time())*1000000-3600000000
 w.call('POST','/v2/sync/streams',{'datasetID':h['datasetID'],'datasetEpoch':h['datasetEpoch'],'collectorEpoch':w.collector,'streamID':stream,'sampleType':'HKWorkoutTypeIdentifier','queryFingerprint':fp})
 event={'op':'upsert','kind':'workout','type':'HKWorkoutTypeIdentifier','uuid':w.uid(),'startUs':at,'endUs':at+1800000000,'source':{'bundleID':'synthetic.p7.worker-crash'},'workout':{'activityType':37,'durationUs':1800000000}}
 decoded=(json.dumps(event)+'\n').encode();wire=gzip.compress(decoded,mtime=0)
 env={'protocolVersion':2,'datasetID':h['datasetID'],'datasetEpoch':h['datasetEpoch'],'collectorEpoch':w.collector,'streamID':stream,'batchID':w.uid(),'clientSeq':1,'previousBatchID':None,'pageID':w.uid(),'fragmentIndex':0,'fragmentCount':1,'queryFingerprint':fp,'contentEncoding':'gzip','wireBytes':len(wire),'decodedBytes':len(decoded),'eventCount':1,'payloadSHA256':hashlib.sha256(wire).hexdigest()}
 w.call('POST','/v2/sync/batches',env);r=w.call('PUT','/v2/sync/batches/'+env['batchID']+'/body',data=wire,extra={'Content-Type':'application/x-rai-health-delta+ndjson','Content-Encoding':'gzip'})
 assert r['analysisJobIDs'];(p/'worker-job').write_text(r['analysisJobIDs'][0])
elif mode=='unavailable':
 with urllib.request.urlopen(w.base+'/health',timeout=3) as r:assert r.status==200
 try:urllib.request.urlopen(w.base+'/ready',timeout=5)
 except urllib.error.HTTPError as e:assert e.code==503
 else:raise AssertionError('readiness incorrectly reports database available')
 try:w.call('GET','/v2/handshake')
 except RuntimeError:pass
 else:raise AssertionError('database interruption not observed')
 print('P7_NETWORK_FAILURE=PASS liveness available; readiness/authentication fail safely')
else:raise SystemExit('unknown fault mode')
