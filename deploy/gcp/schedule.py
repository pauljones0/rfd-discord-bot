#!/usr/bin/env python3
"""Create one recurring HTTP scheduler for an already migrated worker service."""
import argparse
import json
import subprocess
import urllib.error
import urllib.request
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--project',required=True)
    parser.add_argument('--service',required=True)
    parser.add_argument('--config',type=Path,required=True)
    parser.add_argument('--schedule',required=True)
    args=parser.parse_args()
    config=json.loads(args.config.read_text())
    if config['GCP_PROJECT'] != args.project or config['BOT_DEPLOYMENT_MODE'] != 'production' or config['BOT_REQUEST_ROLE'] != 'worker':
        raise ValueError('scheduler only accepts a production worker configuration')
    project=args.project
    jobs=json.loads(subprocess.check_output(['gcloud','scheduler','jobs','list','--project='+project,'--location=us-central1','--format=json'],text=True))
    name=f'projects/{project}/locations/us-central1/jobs/{args.service}-tick'
    if any(job['name']==name for job in jobs):
        raise ValueError('job already exists; refusing to change an active schedule')
    if len(jobs)>=3:
        raise ValueError('three free scheduler jobs already allocated')
    url=subprocess.check_output(['gcloud','run','services','describe',args.service,'--project='+project,'--region=us-central1','--format=value(status.url)'],text=True).strip()
    token=subprocess.check_output(['gcloud','auth','print-access-token'],text=True).strip()
    data={'name':name,'description':'Rust Discord bot request-driven poller','schedule':args.schedule,'timeZone':'Etc/UTC','attemptDeadline':'1800s','retryConfig':{'retryCount':0},'httpTarget':{'uri':url+'/tick','httpMethod':'POST','headers':{'Content-Type':'application/json','X-Bot-Scheduler-Token':config['SCHEDULER_TOKEN']},'body':'e30=','oidcToken':{'serviceAccountEmail':args.service+'-runtime@'+project+'.iam.gserviceaccount.com','audience':url}}}
    request=urllib.request.Request('https://cloudscheduler.googleapis.com/v1/projects/'+project+'/locations/us-central1/jobs', data=json.dumps(data).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'},method='POST')
    try:
        with urllib.request.urlopen(request,timeout=30) as response: result=json.load(response)
    except urllib.error.HTTPError as error:
        raise RuntimeError('scheduler create HTTP '+str(error.code)) from None
    print(json.dumps({'job':result['name'],'schedule':result['schedule'],'state':result['state']}))


if __name__=='__main__':main()
