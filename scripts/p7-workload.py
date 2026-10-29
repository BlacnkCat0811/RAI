#!/usr/bin/env python3
"""Synthetic authenticated HTTPS workload. Original IDs/receipts remain private."""
import concurrent.futures,datetime,gzip,hashlib,json,math,os,pathlib,re,ssl,statistics,sys,time,urllib.error,urllib.request,uuid
base=os.environ['RAI_E2E_URL'];token=os.environ['RAI_E2E_TOKEN'];collector=os.environ['RAI_E2E_COLLECTOR'];state=pathlib.Path(os.environ['RAI_E2E_STATE_DIR'])
def uid():return str(uuid.uuid4())
def call(method,path,value=None,data=None,extra=None):
 body=data if data is not None else None if value is None else json.dumps(value,separators=(',',':')).encode()
 headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'};headers.update(extra or {})
 request=urllib.request.Request(base+path,data=body,method=method,headers=headers)
 for attempt in range(12):
  try:
   with urllib.request.urlopen(request,timeout=30) as response:return json.load(response)
  except urllib.error.HTTPError as e:
   if e.code==429 and attempt<11:time.sleep(max(1,int(e.headers.get('Retry-After','1'))));continue
   try:code=json.loads(e.read(512)).get('code','unknown')
   except (ValueError,AttributeError):code='unknown'
   if not isinstance(code,str) or not re.fullmatch(r'[a-z0-9_]{1,80}',code):code='unknown'
   raise RuntimeError('HTTP workload failed status='+str(e.code)+' code='+code) from None
def wait_plan(predicate):
 for _ in range(120):
  p=call('GET','/v2/training/plans/current')
  if predicate(p):return p
  time.sleep(1)
 raise RuntimeError('training publication deadline exceeded')
class SyncLoad:
 def __init__(self):
  self.hello=call('GET','/v2/handshake');self.stream=uid();self.seq=1;self.previous=None;self.fp='sha256:'+hashlib.sha256(self.stream.encode()).hexdigest()
  call('POST','/v2/sync/streams',{'datasetID':self.hello['datasetID'],'datasetEpoch':self.hello['datasetEpoch'],'collectorEpoch':collector,'streamID':self.stream,'sampleType':'HKQuantityTypeIdentifierHeartRate','queryFingerprint':self.fp})
 def upload(self,count,uuid_override=None):
  at=int(time.time())*1000000
  events=[{'op':'upsert','kind':'sample','type':'HKQuantityTypeIdentifierHeartRate','uuid':uuid_override or uid(),'startUs':at+i*1000000,'endUs':at+i*1000000,'quantity':{'value':120,'unit':'count/min'},'source':{'bundleID':'synthetic.p7.capacity'}} for i in range(count)]
  decoded=b''.join((json.dumps(e,separators=(',',':'))+'\n').encode() for e in events);wire=gzip.compress(decoded,mtime=0)
  env={'protocolVersion':2,'datasetID':self.hello['datasetID'],'datasetEpoch':self.hello['datasetEpoch'],'collectorEpoch':collector,'streamID':self.stream,'batchID':uid(),'clientSeq':self.seq,'previousBatchID':self.previous,'pageID':uid(),'fragmentIndex':0,'fragmentCount':1,'queryFingerprint':self.fp,'contentEncoding':'gzip','wireBytes':len(wire),'decodedBytes':len(decoded),'eventCount':count,'payloadSHA256':hashlib.sha256(wire).hexdigest()}
  started=time.monotonic();call('POST','/v2/sync/batches',env);ack=call('PUT','/v2/sync/batches/'+env['batchID']+'/body',data=wire,extra={'Content-Type':'application/x-rai-health-delta+ndjson','Content-Encoding':'gzip'})
  assert ack['ingestStatus']=='committed'
  assert ack['counts']['suppressed' if uuid_override else 'inserted']==count
  self.seq+=1;self.previous=env['batchID']
  return {'ms':(time.monotonic()-started)*1000,'wire_bytes':len(wire),'decoded_bytes':len(decoded)}
def performance():
 load=SyncLoad();out={}
 for total in [1000,10000]:
  started=time.monotonic();observations=[load.upload(1000) for _ in range(total//1000)]
  out[str(total)]={'samples':total,'batches':len(observations),'total_ms':(time.monotonic()-started)*1000,'observations':observations,'includes_admission_backoff':True}
 print('P7_SYNC_PERFORMANCE='+json.dumps(out))
 # Four-hour / 14,400-point GPS route, via the unchanged P3 HTTPS protocol.
 h=load.hello;stream=uid();fp='sha256:'+hashlib.sha256(stream.encode()).hexdigest();start=int(time.time())*1000000-14400*1000000;workout=uid()
 call('POST','/v2/sync/streams',{'datasetID':h['datasetID'],'datasetEpoch':h['datasetEpoch'],'collectorEpoch':collector,'streamID':stream,'sampleType':'HKWorkoutTypeIdentifier','queryFingerprint':fp})
 event={'op':'upsert','kind':'workout','type':'HKWorkoutTypeIdentifier','uuid':workout,'startUs':start,'endUs':start+14400*1000000,'source':{'bundleID':'synthetic.p7.long-route'},'workout':{'activityType':37,'durationUs':14400*1000000}}
 decoded=(json.dumps(event)+'\n').encode();wire=gzip.compress(decoded,mtime=0);batch=uid()
 env={'protocolVersion':2,'datasetID':h['datasetID'],'datasetEpoch':h['datasetEpoch'],'collectorEpoch':collector,'streamID':stream,'batchID':batch,'clientSeq':1,'previousBatchID':None,'pageID':uid(),'fragmentIndex':0,'fragmentCount':1,'queryFingerprint':fp,'contentEncoding':'gzip','wireBytes':len(wire),'decodedBytes':len(decoded),'eventCount':1,'payloadSHA256':hashlib.sha256(wire).hexdigest()}
 call('POST','/v2/sync/batches',env);call('PUT','/v2/sync/batches/'+batch+'/body',data=wire,extra={'Content-Type':'application/x-rai-health-delta+ndjson','Content-Encoding':'gzip'})
 route=uid();call('POST','/v2/routes',{'protocolVersion':2,'datasetID':h['datasetID'],'datasetEpoch':h['datasetEpoch'],'collectorEpoch':collector,'routeID':route,'workoutID':workout,'source':{'bundleID':'synthetic.p7.long-route'},'expectedChunkCount':3,'expectedPointCount':14400,'firstRecordedAtUs':start,'lastRecordedAtUs':start+14399*1000000})
 started=time.monotonic();chunks=[]
 for index in range(3):
  points=[{'sequence':i,'recordedAtUs':start+i*1000000,'latitude':30.0,'longitude':120.0+i*0.00001,'horizontalAccuracy':5.0} for i in range(index*5000,min((index+1)*5000,14400))]
  decoded=b''.join((json.dumps(point,separators=(',',':'))+'\n').encode() for point in points);wire=gzip.compress(decoded,mtime=0);at=time.monotonic()
  ack=call('PUT','/v2/routes/'+route+'/chunks/'+str(index),data=wire,extra={'Content-Type':'application/x-rai-health-route+ndjson','Content-Encoding':'gzip'})
  chunks.append({'points':len(points),'wire_bytes':len(wire),'ms':(time.monotonic()-at)*1000})
 sealed=call('POST','/v2/routes/'+route+'/seal',{'protocolVersion':2,'chunkCount':3,'totalPoints':14400,'manifestSHA256':ack['manifestSHA256'],'payloadManifestSHA256':ack['payloadManifestSHA256']})
 assert sealed['ingestStatus']=='sealed'
 print('P7_LONG_ROUTE='+json.dumps({'synthetic_workout_seconds':14400,'points':14400,'chunks':chunks,'chunk_upload_and_seal_ms':(time.monotonic()-started)*1000,'includes_admission_backoff':True}))
def seed():
 # Negative TLS controls: neither unknown CA nor incorrect hostname is accepted.
 try:urllib.request.urlopen(base+'/health',context=ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT),timeout=3)
 except urllib.error.URLError:pass
 else:raise RuntimeError('untrusted TLS accepted')
 h=call('GET','/v2/handshake');workout=call('GET','/v2/workouts')['items'][0]['workoutID']
 for _ in range(120):
  try:
   analysis=call('GET','/v2/analysis/workouts/'+workout)
   if analysis['metrics']:break
  except RuntimeError:pass
  time.sleep(1)
 else:raise RuntimeError('analysis not published')
 mon=datetime.datetime.now(datetime.timezone.utc).date();mon-=datetime.timedelta(days=mon.weekday());plan=uid()
 request={'commandID':uid(),'planID':plan,'config':{'startDate':str(mon),'weeks':8,'goal':{'type':'running','date':str(mon+datetime.timedelta(days=55)),'distanceM':21097.5},'availableDays':[1,3,5,7],'longRunDay':7,'baselineWeeklyM':30000,'previousLongM':12000}}
 first=call('POST','/v2/training/plans',request);assert call('POST','/v2/training/plans',request)==first
 p=wait_plan(lambda p:p['effective'] is not None);eff=p['effective'];sid=eff['sessions'][0]['sessionID']
 override={'commandID':uid(),'expectedOverrideRevision':0,'overrideID':uid(),'sessionID':sid,'operation':'edit','path':'distanceM','value':7000}
 eff=call('POST','/v2/training/plans/'+plan+'/overrides',override)
 execution=uid();ex=call('POST','/v2/training/executions',{'commandID':uid(),'executionID':execution,'planID':plan,'effectiveVersionID':eff['effectiveVersionID'],'sessionID':sid})
 snapshot=ex['snapshot'];ex=call('POST','/v2/training/executions/'+execution+'/changes',{'commandID':uid(),'expectedRevision':1,'operation':'complete'})
 ex=call('POST','/v2/training/executions/'+execution+'/changes',{'commandID':uid(),'expectedRevision':2,'operation':'associate','workoutID':workout})
 p=wait_plan(lambda p:p['effective']['baseVersionID']!=eff['baseVersionID'] and p['generationState']=='succeeded')
 assert next(s for s in p['effective']['sessions'] if s['sessionID']==sid)['distanceM']==7000
 # Explicit unknown delete establishes a permanent UUID barrier via formal P3.
 sample_stream=uid();fp='sha256:'+hashlib.sha256(sample_stream.encode()).hexdigest();deleted=uid()
 call('POST','/v2/sync/streams',{'datasetID':h['datasetID'],'datasetEpoch':h['datasetEpoch'],'collectorEpoch':collector,'streamID':sample_stream,'sampleType':'HKQuantityTypeIdentifierHeartRate','queryFingerprint':fp})
 decoded=(json.dumps({'op':'delete','kind':'sample','type':'HKQuantityTypeIdentifierHeartRate','uuid':deleted})+'\n').encode();wire=gzip.compress(decoded,mtime=0)
 env={'protocolVersion':2,'datasetID':h['datasetID'],'datasetEpoch':h['datasetEpoch'],'collectorEpoch':collector,'streamID':sample_stream,'batchID':uid(),'clientSeq':1,'previousBatchID':None,'pageID':uid(),'fragmentIndex':0,'fragmentCount':1,'queryFingerprint':fp,'contentEncoding':'gzip','wireBytes':len(wire),'decodedBytes':len(decoded),'eventCount':1,'payloadSHA256':hashlib.sha256(wire).hexdigest()}
 call('POST','/v2/sync/batches',env);receipt=call('PUT','/v2/sync/batches/'+env['batchID']+'/body',data=wire,extra={'Content-Type':'application/x-rai-health-delta+ndjson','Content-Encoding':'gzip'})
 assert receipt['counts']['tombstoned']==1
 (state/'p7-private.json').write_text(json.dumps({'plan':plan,'session':sid,'effective':p['effective'],'execution':execution,'snapshot':snapshot,'deleted':deleted,'override':override,'originalEffective':eff,'hello':h}))
 print('P7_SEED=PASS (P3 durable/delete barrier, P4 publication, P5 override/execution/feedback)')
def verify():
 s=json.loads((state/'p7-private.json').read_text());p=call('GET','/v2/training/plans/'+s['plan'])
 assert p['effective']==s['effective'];ex=call('GET','/v2/training/executions/'+s['execution']);assert ex['snapshot']==s['snapshot'] and ex['feedback']
 assert call('POST','/v2/training/plans/'+s['plan']+'/overrides',s['override'])==s['originalEffective']
 SyncLoad().upload(1,uuid_override=s['deleted'])
 print('P7_RESTORED_API=PASS original override receipt, immutable Effective and execution snapshot')
def capture():
 s=json.loads((state/'p7-private.json').read_text());p=call('GET','/v2/training/plans/'+s['plan']);s['effective']=p['effective']
 ex=call('GET','/v2/training/executions/'+s['execution']);assert ex['snapshot']==s['snapshot']
 (state/'p7-private.json').write_text(json.dumps(s))
def settle():
 workout=call('GET','/v2/workouts')['items'][0]['workoutID']
 for _ in range(120):
  try:
   call('GET','/v2/analysis/workouts/'+workout);call('GET','/v2/analysis/state')
   if call('GET','/v2/training/plans/current')['generationState']=='succeeded':return
  except RuntimeError:pass
  time.sleep(1)
 raise RuntimeError('restored jobs did not settle')
def soak():
 s=json.loads((state/'p7-private.json').read_text());latencies=[];started=time.monotonic();errors=0;error_codes={};load=SyncLoad();uploads=0
 # Bounded 180s rehearsal: concurrent API reads, periodic formal sync/recalc.
 def read_once(i):
  at=time.monotonic();call('GET',['/v2/product/overview','/v2/workouts','/v2/analysis/state','/v2/training/plans/current'][i%4]);return (time.monotonic()-at)*1000
 for turn in range(18):
  with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
   for future in [pool.submit(read_once,turn*8+i) for i in range(8)]:
    try:latencies.append(future.result())
    except RuntimeError as error:errors+=1;error_codes[str(error)]=error_codes.get(str(error),0)+1
  if turn%3==0:
   load.upload(10);uploads+=1
   prior=call('GET','/v2/training/plans/'+s['plan'])['effective']['baseVersionID']
   call('POST','/v2/training/plans/'+s['plan']+'/changes',{'commandID':uid(),'operation':'recalculate','expectedConfigRevision':1})
   # Current-result reads intentionally hide dirty publications. Require each
   # real asynchronous regeneration to finish before the next stable-read
   # window; do not count a 404 as success or weaken the zero-error assertion.
   published=wait_plan(lambda p:p['generationState']=='succeeded' and p['effective']['baseVersionID']!=prior)
   assert next(x for x in published['effective']['sessions'] if x['sessionID']==s['session'])['distanceM']==7000
   settle()
  time.sleep(10)
 if errors:print('P7_SOAK_FAILURE='+json.dumps({'errors':errors,'codes':error_codes}))
 assert errors==0
 p=wait_plan(lambda p:p['generationState']=='succeeded');assert next(x for x in p['effective']['sessions'] if x['sessionID']==s['session'])['distanceM']==7000
 values=sorted(latencies);print('P7_SOAK='+json.dumps({'seconds':round(time.monotonic()-started,3),'reads':len(values),'median_ms':statistics.median(values),'p95_ms':values[math.ceil(.95*len(values))-1],'p95_method':'nearest rank','errors':errors,'incremental_uploads':uploads,'scope':'bounded synthetic HTTPS stable-publication reads, formal sync and six individually confirmed training regenerations; not mixed dirty-state availability or long-term endurance'}))
if __name__=='__main__':{'seed':seed,'verify':verify,'soak':soak,'performance':performance,'capture':capture,'settle':settle}[sys.argv[1]]()
