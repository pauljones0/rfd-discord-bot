#!/usr/bin/env python3
"""Offline processor + SQLite + REST + Gateway benchmark. No live credentials."""
import argparse, base64, hashlib, http.server, json, os, pathlib, selectors, socket, statistics, struct, subprocess, tempfile, threading, time
from datetime import datetime, timedelta, timezone
from urllib.parse import urlparse, parse_qs
class Fixture(http.server.ThreadingHTTPServer):
    daemon_threads=True
    def __init__(self):
        super().__init__(('127.0.0.1',0), Handler)
        self.base=f'http://127.0.0.1:{self.server_port}'
        self.polls=0; self.sends=0; self.callbacks=0; self.beats=0; self.quotes=0
        self.now=datetime.now(timezone.utc).replace(microsecond=0)-timedelta(hours=1)
    def get_request(self):
        sock, address = super().get_request()
        sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        return sock, address
    def seed(self):
        deals=[]
        for i in range(2000):
            pub=(self.now-timedelta(seconds=i)).isoformat().replace('+00:00','Z')
            identity=hashlib.sha256(pub.encode()).hexdigest()
            post=f'https://forums.redflagdeals.com/fixture-sale-{12345+i}'
            deals.append({'Title':f'SSD model X{i:04} 1TB sale 20% off','DocumentID':identity,'PostURL':post,'ActualDealURL':f'https://example.invalid/product/{i}','Description':'Fixture description','PublishedTimestamp':pub,'LastUpdated':pub,'DiscordLastUpdatedTime':(self.now+timedelta(hours=1)).isoformat().replace('+00:00','Z'),'DiscordMessageIDs':{} if i==0 else {'42':str(900000+i)},'DiscordMessageApplicationIDs':{} if i==0 else {'42':'1001'},'Threads':[{'DocumentID':identity,'PostURL':post,'LikeCount':42,'CommentCount':10}],'SearchTokens':[]})
        return deals
class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*args): pass
    def reply(self,body,kind='text/html',status=200):
        if isinstance(body,dict): body=json.dumps(body).encode();kind='application/json'
        elif isinstance(body,str):body=body.encode()
        self.send_response(status);self.send_header('Content-Type',kind);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
    def do_POST(self):
        body=self.rfile.read(int(self.headers.get('Content-Length','0')))
        if '/interactions/' in self.path:
            p=json.loads(body);assert p['type']==4
            self.server.callbacks+=1;self.reply(b'',status=204)
        elif '/channels/42/messages' in self.path:
            p=json.loads(body);assert p['allowed_mentions']['parse']==[]
            self.server.sends+=1;self.reply({'id':str(950000+self.server.sends),'channel_id':'42'})
        else:self.reply({'error':'unexpected POST'},status=400)
    def do_PATCH(self):self.do_POST()
    def do_GET(self):
        s=self.server;u=urlparse(self.path)
        if self.headers.get('Upgrade','').lower()=='websocket': return self.websocket()
        if '/gateway' in u.path:return self.reply({'url':s.base.replace('http:','ws:')+'/gateway','shards':1,'session_start_limit':{'total':1000,'remaining':1000,'reset_after':3600000,'max_concurrency':1}})
        if '/finance/quote/' in u.path:
            s.quotes+=1;return self.reply('<div class="gO24Ff">Fixture</div><span jsname="Pdsbrc"><span>CA$1.23</span></span><div class="KxsRFb"><div class="SwQK7">Mkt. cap</div><div class="dO6ijd">12.3M</div></div>')
        if u.path=='/companies':
            q=parse_qs(u.query);page=int(next(iter(q.values()))[0]) if q else 1
            if page==1:s.polls+=1
            cards=[]
            for i in range((page-1)*100,page*100):
                score=5 if i<5 and s.polls>=2 else 3
                cards.append(f"<a class='company-card' href='/companies/fixture-{i}'><div class='cc_content'><div class='cc_title-text'>Company Name</div><div class='body'>Fixture Company {i:03}</div></div><div class='cc_content'><div class='cc_title-text'>Ticker</div><div class='body'>TSX:F{i:03}</div></div><span fs-cmssort-field='crux'>{score}</span></a>")
            return self.reply("<div fs-cmsload-element='list'>"+''.join(cards)+f"</div><div class='w-page-count' aria-label='Page {page} of 5'>{page} / 5</div>")
        if u.path.startswith('/hot-deals'):
            s.polls+=1; cards=[]
            for i in range(200):
                pub=(s.now-timedelta(seconds=i)).isoformat().replace('+00:00','Z')
                cards.append(f"<li class='topic-card topic'><a class='topic-card-info thread_info' href='https://forums.redflagdeals.com/fixture-sale-{12345+i}'><h3 class='thread_title'>SSD model X{i:04} 1TB sale 20% off</h3><time class='topic_time' datetime='{pub}'></time></a><div class='thread_extra_info'><span class='votes'>{42+s.polls}</span><span class='posts'>10</span></div></li>")
            return self.reply(''.join(cards))
        if u.path.startswith('/fixture-sale-'):
            i=int(u.path.rsplit('-',1)[1])-12345
            return self.reply(f"<div class='deal_link'><a href='https://example.invalid/product/{i}'>Buy</a></div><script type='application/ld+json'>{{\"@type\":\"DiscussionForumPosting\",\"text\":\"Fixture description\"}}</script>")
        return self.reply({'unexpected':self.path},status=404)
    def websocket(self):
        s=self.server;key=self.headers['Sec-WebSocket-Key'];digest=base64.b64encode(hashlib.sha1((key+'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
        self.send_response(101);self.send_header('Upgrade','websocket');self.send_header('Connection','Upgrade');self.send_header('Sec-WebSocket-Accept',digest);self.end_headers()
        def send(v,opcode=1):
            p=json.dumps(v).encode() if opcode==1 else v
            h=bytes([128|opcode,len(p)]) if len(p)<126 else bytes([128|opcode,126])+struct.pack('!H',len(p))
            self.wfile.write(h+p);self.wfile.flush()
        def exact(n):
            b=self.rfile.read(n)
            if len(b)!=n:raise EOFError
            return b
        try:
            send({'op':10,'d':{'heartbeat_interval':1000}})
            while True:
                first,second=exact(2);op=first&15;size=second&127
                if size==126:size=struct.unpack('!H',exact(2))[0]
                elif size==127:size=struct.unpack('!Q',exact(8))[0]
                mask=exact(4) if second&128 else b'\0'*4
                p=exact(size);p=bytes(b^mask[i%4] for i,b in enumerate(p))
                if op==8:send(p,8);break
                if op==9:send(p,10);continue
                if op!=1:continue
                v=json.loads(p)
                if v['op']==1:s.beats+=1;send({'op':11,'d':None})
                elif v['op']==2:
                    send({'op':0,'t':'READY','s':1,'d':{'v':10,'user':{'id':'1001','username':'fixture','bot':True},'session_id':'fixture','resume_gateway_url':s.base.replace('http:','ws:')+'/gateway','guilds':[]}})
                    send({'op':0,'t':'INTERACTION_CREATE','s':2,'d':{'id':'100000','application_id':'1001','token':'fixture-token','type':2,'guild_id':'1','member':{'permissions':'32','user':{'id':'1002','username':'fixture'}},'data':{'name':'rfd' if 'rfd' in self.server.kind else 'deals','options':[{'name':'list','type':1}]}}})
        except (EOFError,ConnectionError,socket.error):pass

def sample(pid):
    stat=pathlib.Path(f'/proc/{pid}/stat').read_text().split(') ',1)[1].split()
    cpu=(int(stat[11])+int(stat[12]))/os.sysconf('SC_CLK_TCK')
    info={line.split(':')[0]:line.split(':')[1].strip() for line in pathlib.Path(f'/proc/{pid}/status').read_text().splitlines() if ':' in line}
    return {'cpu_s':cpu,'rss_mib':int(info['VmRSS'].split()[0])/1024,'peak_mib':int(info['VmHWM'].split()[0])/1024}
def phase(p,expected,timeout=120):
    sel=selectors.DefaultSelector();sel.register(p.stdout,selectors.EVENT_READ)
    try:
        deadline=time.monotonic()+timeout
        while time.monotonic()<deadline:
            if not sel.select(max(0,deadline-time.monotonic())):break
            line=p.stdout.readline()
            if not line:raise RuntimeError(f'fixture exited {p.poll()} before {expected}')
            try:v=json.loads(line)
            except ValueError:continue
            if v.get('phase')==expected:return
        raise TimeoutError(f'fixture did not reach {expected}')
    finally:sel.close()
def main():
    parser=argparse.ArgumentParser();parser.add_argument('--kind',choices=['crux','rfd'],required=True);parser.add_argument('--go',required=True);parser.add_argument('--rust',required=True);parser.add_argument('--polls',type=int,default=20);parser.add_argument('--runs',type=int,default=5);parser.add_argument('--container',help='Run the Rust fixture image with a 128 MiB cgroup and read-only filesystem');parser.add_argument('--runner',help='Optional user-mode emulator for the Rust binary');parser.add_argument('--output',required=True);args=parser.parse_args()
    results=[]
    for run in range(args.runs):
        # Alternate ordering to reduce thermal/cache bias. Each process has fresh disk state.
        for lang in (['go','rust'] if run%2==0 else ['rust','go']):
            server=Fixture();server.kind=args.kind;thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            with tempfile.TemporaryDirectory(prefix='bot-fixture-') as tmp:
                tmp=pathlib.Path(tmp);seed=tmp/'seed.json';seed.write_text(json.dumps(server.seed()));err=open(tmp/'stderr','w+')
                env={'PATH':os.environ['PATH'],'DISCORD_BOT_TOKEN':'fixture-only','DISCORD_APP_ID':'1001','CRUX_ENABLED':'true','DISCORD_GATEWAY_ENABLED':'true','LOCAL_SCHEDULER_ENABLED':'false','GOMEMLIMIT':'160MiB' if args.kind=='crux' else '256MiB','GOMAXPROCS':'1'}
                binary=str(pathlib.Path(getattr(args,lang)).resolve());cmd=[binary,server.base,str(tmp/'bot.sqlite'),str(seed)]
                container=None
                if lang=='rust' and args.runner:cmd=[args.runner]+cmd
                if lang=='rust' and args.container:
                    container=f'codex-{args.kind}-fixture-{os.getpid()}-{run}'
                    cmd=['docker','run','--rm','--name',container,'--network','host','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--memory','128m','--pids-limit','64','--user',f'{os.getuid()}:{os.getgid()}','-v',f'{tmp}:{tmp}','-w',str(tmp),'-i']
                    for key,value in env.items():cmd+=['-e',f'{key}={value}']
                    cmd+=[args.container,server.base,str(tmp/'bot.sqlite'),str(seed)]
                p=subprocess.Popen(cmd,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=err,text=True,cwd=tmp,env=env)
                try:
                    phase(p,'ready');time.sleep(2);measured_pid=int(subprocess.check_output(['docker','inspect','--format','{{.State.Pid}}',container],text=True)) if container else p.pid;initial=sample(measured_pid);p.stdin.write(json.dumps({'polls':args.polls})+'\n');p.stdin.flush();started=time.monotonic();phase(p,'done');elapsed=time.monotonic()-started;end=sample(measured_pid);time.sleep(2);idle=sample(measured_pid)
                    assert server.sends==1 and server.callbacks==1 and server.beats>=2,(server.sends,server.callbacks,server.beats)
                    assert server.polls==args.polls+3 and server.quotes==(5 if args.kind=='crux' else 0),(server.polls,server.quotes,args.kind)
                    item={'language':lang,'run':run,'polls':args.polls,'cpu_s':round(end['cpu_s']-initial['cpu_s'],4),'elapsed_s':round(elapsed,4),'idle_rss_mib':round(idle['rss_mib'],3),'peak_rss_mib':round(end['peak_mib'],3),'idle_cpu_s':round(idle['cpu_s']-end['cpu_s'],4),'fixture_sends':server.sends,'fixture_quotes':server.quotes,'fixture_callbacks':server.callbacks,'fixture_heartbeats':server.beats}
                    results.append(item);print(json.dumps(item),flush=True);p.stdin.close();p.wait(timeout=15);assert p.returncode==0
                except BaseException:
                    p.kill();p.wait();
                    if container:subprocess.run(['docker','rm','-f',container],check=False,stdout=subprocess.DEVNULL)
                    err.seek(0);print(err.read());raise
                finally:err.close();server.shutdown();server.server_close()
    out={'kind':args.kind,'platform':os.uname().machine,'polls_per_run':args.polls,'runs_per_language':args.runs,'rust_runner':args.runner,'rust_container':args.container,'raw':results,'medians':{lang:{key:statistics.median(r[key] for r in results if r['language']==lang) for key in ['cpu_s','elapsed_s','idle_rss_mib','peak_rss_mib','idle_cpu_s']} for lang in ['go','rust']}}
    pathlib.Path(args.output).write_text(json.dumps(out,indent=2)+'\n');print(json.dumps(out['medians'],indent=2))
if __name__=='__main__':main()
