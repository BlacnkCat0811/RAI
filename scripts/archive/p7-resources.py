#!/usr/bin/env python3
"""Read-only cgroup/host sampling for an explicitly selected Compose project.
No DB credentials, health data, container environment, or other projects read.
"""
import argparse,datetime,json,pathlib,shutil,subprocess,time
p=argparse.ArgumentParser();p.add_argument('--project',required=True);p.add_argument('--seconds',type=int,default=30);p.add_argument('--disk-path',default='.');a=p.parse_args()
assert 5<=a.seconds<=300
def run(args):return subprocess.check_output(args,text=True).strip()
def discover():
 result={}
 for service in ['api','worker','postgres']:
  cid=run(['docker','ps','-q','--filter','label=com.docker.compose.project='+a.project,'--filter','label=com.docker.compose.service='+service])
  assert cid and '\n' not in cid,'one running instance per service required'
  result[service]=cid
 return result
ids=discover()
def snapshot():
 out={}
 for service,cid in ids.items():
  if service in ['api','worker']:v=json.loads(run(['docker','exec',cid,'/rai','resources']))
  else:
   cpu=dict(line.split() for line in run(['docker','exec',cid,'cat','/sys/fs/cgroup/cpu.stat']).splitlines())
   v={'cpu.stat':{k:int(value) for k,value in cpu.items()},'memory.current':int(run(['docker','exec',cid,'cat','/sys/fs/cgroup/memory.current'])),'memory.peak':int(run(['docker','exec',cid,'cat','/sys/fs/cgroup/memory.peak']))}
  out[service]=v
 return out
start=time.monotonic();before=snapshot();samples=[before]
while time.monotonic()-start<a.seconds:
 time.sleep(min(5,max(0,a.seconds-(time.monotonic()-start))));assert discover()==ids,'container changed during resource sample';samples.append(snapshot())
elapsed=time.monotonic()-start;after=samples[-1];memory={}
for line in pathlib.Path('/proc/meminfo').read_text().splitlines():
 fields=line.split()
 if fields[0] in ['MemTotal:','MemAvailable:','SwapFree:']:memory[fields[0][:-1]+'_bytes']=int(fields[1])*1024
disk=shutil.disk_usage(a.disk_path);result={}
for name in ids:
 delta=after[name]['cpu.stat']['usage_usec']-before[name]['cpu.stat']['usage_usec'];assert delta>=0
 result[name]={'average_cpu_percent_of_one_core':delta/elapsed/10000,'max_sampled_memory_current_bytes':max(s[name]['memory.current'] for s in samples),'cgroup_memory_peak_bytes':max(s[name]['memory.peak'] for s in samples),'cpu_throttled_usec_delta':after[name]['cpu.stat']['throttled_usec']-before[name]['cpu.stat']['throttled_usec']}
print('P7_RESOURCE_WINDOW='+json.dumps({'seconds':elapsed,'samples':len(samples),'services':result,'max_sampled_total_container_bytes':max(sum(v['memory.current'] for v in s.values()) for s in samples),'host_memory':memory,'host_disk':{'total_bytes':disk.total,'free_bytes':disk.free},'scope':'cgroup counters include brief probes; CPU is sampled-window average; memory.peak since current container cgroup start'}))
