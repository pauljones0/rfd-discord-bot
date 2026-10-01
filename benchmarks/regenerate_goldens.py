#!/usr/bin/env python3
"""Regenerate Go parity outputs locally. Default checks; --write updates goldens."""
import argparse,json,os,pathlib,subprocess,tempfile
p=argparse.ArgumentParser();p.add_argument('--write',action='store_true');args=p.parse_args()
root=pathlib.Path(__file__).resolve().parents[1];reference=root/'benchmarks/go-reference';fixtures=root/'tests/fixtures';kind='rfd'
with tempfile.TemporaryDirectory(prefix='bot-go-goldens-') as tmp:
    tmp=pathlib.Path(tmp)
    targets=[('scraper', 'go-rfd-parse.json', 'TestExportRustParity'), ('util', 'go-rfd-urls.json', 'TestExportRustParity'), ('processor', 'go-rfd-reconcile.json', 'TestExportRustParity'), ('notifier', 'go-rfd-render.json', 'TestExportRustRenderParity')]
    for package,filename,test in targets:
        env=dict(os.environ,RUST_PARITY_OUTPUT=str(tmp/filename),RUST_PARITY_INPUT=str(fixtures/'go-rfd-reconcile.json'))
        subprocess.run(['go','test','./internal/'+package,'-count=1','-run','^'+test+'$'],cwd=reference,env=env,check=True)
    if kind=='crux':
        source=fixtures/'go-crux-pages.json';inputs=json.loads(source.read_text())
        output=subprocess.check_output(['go','run','./cmd/parity'],input=json.dumps(inputs).encode(),cwd=reference)
        (tmp/source.name).write_bytes(output)
    for generated in sorted(tmp.iterdir()):
        destination=fixtures/generated.name
        if args.write:destination.write_bytes(generated.read_bytes())
        elif json.loads(destination.read_text())!=json.loads(generated.read_text()):raise SystemExit('Go golden differs: '+destination.name)
        print(('Updated ' if args.write else 'Matched ')+destination.name)
